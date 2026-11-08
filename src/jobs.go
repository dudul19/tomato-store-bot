package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func (b *Bot) jobs(ctx context.Context) {
	every := func(name string, d time.Duration, first time.Duration, fn func()) {
		go func() {
			sleepCtx(ctx, first)
			for ctx.Err() == nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("job %s panic: %v", name, r)
							b.db.logError("job:"+name, fmt.Errorf("%v", r))
						}
					}()
					fn()
				}()
				sleepCtx(ctx, d)
			}
		}()
	}
	every("payments", 5*time.Second, 3*time.Second, b.pollPayments)
	every("late-payments", 2*time.Minute, time.Minute, b.latePayments)
	every("servers", 2*time.Minute, 5*time.Second, func() { b.checkServers(false) })
	every("orders", 2*time.Minute, 30*time.Second, b.recoverOrders)
	every("expiry", 10*time.Minute, 20*time.Second, b.expiryJob)
	every("backup", 10*time.Minute, 2*time.Minute, b.backupJob)
	every("prune", 6*time.Hour, 5*time.Minute, b.prune)
	go b.broadcastWorker(ctx)
}

// ---------------------------------------------------------------- payments

// pollPayments checks each pending invoice at most every 5 s (QRIS) or
// 60 s (VA, which takes longer to pay), max 8 in parallel.
func (b *Bot) pollPayments() {
	list, err := b.db.pendingInvoices()
	if err != nil || len(list) == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, inv := range list {
		gap := int64(5)
		if inv.Method != "qris" {
			gap = 60
		}
		if now()-inv.CheckedAt < gap && now() < inv.ExpiresAt {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(inv *Invoice) {
			defer func() { <-sem; wg.Done() }()
			b.checkInvoice(inv)
		}(inv)
	}
	wg.Wait()
}

// latePayments — a QR paid in the last seconds before it expired or was
// cancelled is still credited.
func (b *Bot) latePayments() {
	rows, err := b.db.Query(`SELECT `+invoiceCols+` FROM invoices WHERE status IN ('expired','cancelled') AND created_at>? AND checked_at<? LIMIT 50`,
		now()-3600, now()-110)
	if err != nil {
		return
	}
	var list []*Invoice
	for rows.Next() {
		if inv, err := scanInvoice(rows); err == nil {
			list = append(list, inv)
		}
	}
	rows.Close()
	for _, inv := range list {
		_, _ = b.db.Exec(`UPDATE invoices SET checked_at=? WHERE id=?`, now(), inv.ID)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		st, _ := b.pay.status(ctx, inv)
		cancel()
		if st == payPaid {
			if credited, bal, cb := b.creditInvoice(inv); credited {
				inv.MsgID = 0
				b.paidMessage(inv, bal, cb)
			}
		}
	}
}

// ---------------------------------------------------------------- servers

func (b *Bot) checkServers(force bool) {
	srv, err := b.db.servers(true)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, s := range srv {
		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
			lat, err := b.api(s).Ping()
			t := now()
			if err == nil {
				_, _ = b.db.Exec(`UPDATE servers SET status='online', last_check=?, last_error='', latency_ms=?, down_since=0 WHERE id=?`, t, lat.Milliseconds(), s.ID)
				_, _ = b.db.Exec(`INSERT INTO health_logs(server_id, ok, latency_ms, created_at) VALUES(?,1,?,?)`, s.ID, lat.Milliseconds(), t)
				if s.Status == "offline" {
					b.notifyAdmins(newCard(b.t("srv_up_title")).kv("Server", s.Name).kv(b.t("lbl_down_for"), durText(t-s.DownSince)).String())
				}
				return
			}
			msg := trimRunes(err.Error(), 200)
			_, _ = b.db.Exec(`INSERT INTO health_logs(server_id, ok, error, created_at) VALUES(?,0,?,?)`, s.ID, msg, t)
			// Two failed checks in a row before "offline" (avoids flapping).
			fails := b.db.int(`SELECT COUNT(*) FROM (SELECT ok FROM health_logs WHERE server_id=? ORDER BY id DESC LIMIT 2) WHERE ok=0`, s.ID)
			if fails < 2 && !force {
				_, _ = b.db.Exec(`UPDATE servers SET last_check=?, last_error=? WHERE id=?`, t, msg, s.ID)
				return
			}
			if s.Status != "offline" {
				_, _ = b.db.Exec(`UPDATE servers SET status='offline', last_check=?, last_error=?, down_since=? WHERE id=?`, t, msg, t, s.ID)
				b.notifyAdmins(newCard(b.t("srv_down_title")).kv("Server", s.Name).kv("Error", msg).kv(b.t("lbl_since"), fmtTime(t)).
					line().text(b.t("srv_down_body")).String())
			} else {
				_, _ = b.db.Exec(`UPDATE servers SET last_check=?, last_error=? WHERE id=?`, t, msg, s.ID)
			}
		}(s)
	}
	wg.Wait()
}

func durText(sec int64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%dh %dm", sec/3600, sec%3600/60)
}

// ---------------------------------------------------------------- expiry

func (b *Bot) expiryJob() {
	t := now()
	// Expired accounts: Tomato removes them itself; the bot only updates
	// its records and tells the buyer (once).
	rows, err := b.db.Query(`SELECT a.id, a.user_id, a.proto, a.username, a.trial, u.notify FROM accounts a JOIN users u ON u.id=a.user_id
		WHERE a.status='active' AND a.expires_at<=?`, t)
	if err == nil {
		type exp struct {
			id, user      int64
			proto, name   string
			trial, notify bool
		}
		var list []exp
		for rows.Next() {
			var e exp
			if rows.Scan(&e.id, &e.user, &e.proto, &e.name, &e.trial, &e.notify) == nil {
				list = append(list, e)
			}
		}
		rows.Close()
		for _, e := range list {
			_, _ = b.db.Exec(`UPDATE accounts SET status='expired', updated_at=? WHERE id=?`, t, e.id)
			if !e.trial && e.notify {
				b.send(e.user, newCard(b.t("expired_title")).kv(b.t("lbl_proto"), protoLabel[e.proto]).kv(b.t("lbl_username"), e.name).
					line().text(b.t("expired_body")).String(), kbd(row(ok(b.t("btn_buy"), "buy"))))
			}
		}
	}
	// Reminders: for each "days before" threshold crossed since the last one.
	days := b.set.List("remind_days")
	sort.Slice(days, func(i, j int) bool { return days[i] > days[j] })
	if len(days) == 0 {
		return
	}
	maxD := days[0]
	rows, err = b.db.Query(`SELECT a.id, a.user_id, a.server_id, a.proto, a.username, a.expires_at, a.notified_at FROM accounts a JOIN users u ON u.id=a.user_id
		WHERE a.status='active' AND a.trial=0 AND u.notify=1 AND u.blocked=0 AND a.expires_at>? AND a.expires_at<=?`, t, t+maxD*86400)
	if err != nil {
		return
	}
	type rem struct {
		id, user, server, exp, notified int64
		proto, name                     string
	}
	var list []rem
	for rows.Next() {
		var r rem
		if rows.Scan(&r.id, &r.user, &r.server, &r.proto, &r.name, &r.exp, &r.notified) == nil {
			list = append(list, r)
		}
	}
	rows.Close()
	for _, r := range list {
		due := false
		for _, d := range days {
			mark := r.exp - d*86400
			if t >= mark && r.notified < mark {
				due = true
			}
		}
		if !due {
			continue
		}
		_, _ = b.db.Exec(`UPDATE accounts SET notified_at=? WHERE id=?`, t, r.id)
		name := ""
		if s, err := b.db.server(r.server); err == nil {
			name = s.Name
		}
		b.send(r.user, newCard(b.t("remind_title")).kv(b.t("lbl_proto"), protoLabel[r.proto]).kv(b.t("lbl_username"), r.name).
			kv("Server", name).kv("Expired", b.expText(r.exp)).String(),
			kbd(pair(ok(b.t("btn_renew"), fmt.Sprintf("acc:r:%d", r.id)), pri(b.t("btn_my_accounts"), "acc"))))
	}
}

// ---------------------------------------------------------------- backup

func (b *Bot) backup() (string, error) {
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(backupDir, "store-"+time.Now().Format("20060102-150405")+".db")
	if err := b.db.backupTo(path); err != nil {
		return "", err
	}
	b.pruneBackups()
	return path, nil
}

func (b *Bot) pruneBackups() {
	keep := time.Duration(b.set.Int("backup_keep_days")) * 24 * time.Hour
	ents, _ := os.ReadDir(backupDir)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "store-") || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > keep {
			_ = os.Remove(filepath.Join(backupDir, e.Name()))
		}
	}
}

func (b *Bot) backupJob() {
	h := b.set.Int("backup_hours")
	if h <= 0 {
		return
	}
	if last := b.lastBackup(); now()-last < h*3600 {
		return
	}
	path, err := b.backup()
	if err != nil {
		b.db.logError("backup", err)
		return
	}
	if !b.set.Bool("backup_send") {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 45<<20 {
		return
	}
	caption := newCard("AUTO BACKUP").kv(b.t("lbl_store"), b.set.Str("store_name")).kv(b.t("lbl_time"), fmtTime(now())).String()
	for _, a := range b.cfg.Admins {
		if err := b.tg.SendDocument(a, filepath.Base(path), data, caption); err != nil {
			b.db.logError("backup_send", err)
		}
	}
}

// restoreFrom validates an uploaded .db and swaps it in on restart.
func (b *Bot) restoreFrom(m *Message) {
	chat := m.Chat.ID
	if !strings.HasSuffix(strings.ToLower(m.Document.FileName), ".db") || m.Document.FileSize > 45<<20 {
		b.send(chat, b.t("restore_bad"), nil)
		return
	}
	data, err := b.tg.Download(m.Document.FileID, 45<<20)
	if err != nil {
		b.send(chat, b.t("txt_invalid", "msg", err.Error()), nil)
		return
	}
	tmp := dbFile + ".upload"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		b.send(chat, b.t("txt_invalid", "msg", err.Error()), nil)
		return
	}
	test, err := openDB(tmp)
	if err != nil || test.int(`SELECT COUNT(*) FROM sqlite_master WHERE name='users'`) == 0 {
		if test != nil {
			test.Close()
		}
		_ = os.Remove(tmp)
		b.send(chat, b.t("restore_bad"), nil)
		return
	}
	users := test.int(`SELECT COUNT(*) FROM users`)
	test.Close()
	_ = os.Remove(tmp + "-wal")
	_ = os.Remove(tmp + "-shm")
	b.clearState(m.From.ID)
	if _, err := b.backup(); err != nil {
		b.db.logError("backup", err)
	}
	b.db.audit(m.From.ID, "restore", m.Document.FileName, fmt.Sprint(users))
	b.send(chat, newCard("RESTORE").kv("File", m.Document.FileName).kv("User", num(users)).line().text(b.t("restore_done")).String(), nil)
	// systemd restarts the service; applyPendingRestore swaps the file.
	go func() {
		time.Sleep(2 * time.Second)
		if b.stop != nil {
			b.stop()
		}
	}()
}

// applyPendingRestore runs before the database is opened.
func applyPendingRestore() {
	up := dbFile + ".upload"
	if _, err := os.Stat(up); err != nil {
		return
	}
	for _, s := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(dbFile + s)
	}
	if err := os.Rename(up, dbFile); err != nil {
		log.Printf("restore: %v", err)
		return
	}
	log.Printf("restore: database replaced from upload")
}

// ---------------------------------------------------------------- prune

func (b *Bot) prune() {
	t := now()
	for _, q := range []string{
		`DELETE FROM error_logs WHERE created_at<?`,
		`DELETE FROM health_logs WHERE created_at<?`,
		`DELETE FROM audit_logs WHERE created_at<?`,
	} {
		_, _ = b.db.Exec(q, t-30*86400)
	}
	_, _ = b.db.Exec(`DELETE FROM accounts WHERE status IN ('expired','deleted') AND updated_at<?`, t-90*86400)
	_, _ = b.db.Exec(`DELETE FROM invoices WHERE status IN ('expired','cancelled') AND created_at<?`, t-30*86400)
	_, _ = b.db.Exec(`DELETE FROM trial_logs WHERE created_at<?`, t-30*86400)
	_, _ = b.db.Exec(`PRAGMA optimize`)
}

// ---------------------------------------------------------------- broadcast

// broadcastWorker sends one broadcast at a time, resuming after restarts
// (cursor = last user id), at the configured rate.
func (b *Bot) broadcastWorker(ctx context.Context) {
	for ctx.Err() == nil {
		var id, cursor int64
		var kind, text, photo, target string
		err := b.db.QueryRow(`SELECT id, kind, text, photo, target, cursor FROM broadcasts WHERE status IN ('queued','running') ORDER BY id LIMIT 1`).
			Scan(&id, &kind, &text, &photo, &target, &cursor)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-b.bcWake:
			case <-time.After(time.Minute):
			}
			continue
		}
		_, _ = b.db.Exec(`UPDATE broadcasts SET status='running' WHERE id=? AND status='queued'`, id)
		q := `SELECT id FROM users WHERE id>? AND blocked=0 AND banned=0`
		if target == "reseller" {
			q += ` AND role='reseller'`
		}
		rows, err := b.db.Query(q+` ORDER BY id LIMIT 200`, cursor)
		if err != nil {
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		var ids []int64
		for rows.Next() {
			var uid int64
			if rows.Scan(&uid) == nil {
				ids = append(ids, uid)
			}
		}
		rows.Close()
		if len(ids) == 0 {
			_, _ = b.db.Exec(`UPDATE broadcasts SET status='done', finished_at=? WHERE id=? AND status='running'`, now(), id)
			b.broadcastFinished(id)
			continue
		}
		rate := b.set.Int("broadcast_rate")
		if rate < 1 {
			rate = 1
		}
		gap := time.Second / time.Duration(rate)
		for _, uid := range ids {
			if ctx.Err() != nil {
				return
			}
			var st string
			_ = b.db.QueryRow(`SELECT status FROM broadcasts WHERE id=?`, id).Scan(&st)
			if st != "running" {
				break
			}
			var err error
			if photo != "" {
				_, err = b.tg.SendPhotoID(uid, photo, text, nil)
			} else {
				_, err = b.tg.Send(uid, text, nil)
			}
			col := "sent"
			switch {
			case isForbidden(err):
				col = "blocked"
				_, _ = b.db.Exec(`UPDATE users SET blocked=1 WHERE id=?`, uid)
			case err != nil:
				col = "failed"
			}
			_, _ = b.db.Exec(`UPDATE broadcasts SET `+col+`=`+col+`+1, cursor=? WHERE id=?`, uid, id)
			time.Sleep(gap)
		}
	}
}

func (b *Bot) broadcastFinished(id int64) {
	var chat, msg int64
	_ = b.db.QueryRow(`SELECT chat_id, msg_id FROM broadcasts WHERE id=?`, id).Scan(&chat, &msg)
	if chat != 0 {
		b.broadcastStatus(id, chat, 0)
	}
}
