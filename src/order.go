package main

// Orders are two-phase so a balance is never lost:
//  1. one transaction: take the balance + insert the order as "pending";
//  2. call the Tomato API;
//  3. success → account saved, order "done";
//     server rejected the request (HTTP 4xx) → refund, order "refunded";
//     no answer (timeout / connection lost / 5xx) → order stays "pending"
//     and recoverOrders() asks the server later: an account that exists is
//     delivered, one that does not is refunded.

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// userErr — message (text key) to show the buyer.
type userErr struct {
	key string
	kv  []any
}

func (e *userErr) Error() string { return e.key }

func uerr(key string, kv ...any) error { return &userErr{key, kv} }

// unitPrice — price per 30 days for this user (reseller discount applied).
func (b *Bot) unitPrice(u *User, s *Server, proto string) int64 {
	p := b.db.price(s.ID, proto)
	if u != nil && u.reseller() {
		d := b.set.Int("reseller_discount")
		p = p * (100 - d) / 100
	}
	return p
}

func (b *Bot) priceFor(u *User, s *Server, proto string, days int64) int64 {
	unit := b.unitPrice(u, s, proto)
	p := (unit*days + 29) / 30
	r := b.set.Int("price_round")
	if r > 1 && p%r != 0 {
		p = (p/r + 1) * r
	}
	return p
}

// slotState: "" (available), "full" or "offline".
func (b *Bot) slotState(s *Server) string {
	if s.Status == "offline" {
		return "offline"
	}
	if s.MaxAccounts > 0 && b.db.activeCount(s.ID) >= int64(s.MaxAccounts) {
		return "full"
	}
	return ""
}

func randString(n int, alphabet string) string {
	out := make([]byte, n)
	for i := range out {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		out[i] = alphabet[k.Int64()]
	}
	return string(out)
}

func randPassword() string { return randString(8, "abcdefghjkmnpqrstuvwxyz23456789") }

func validUsername(s string) bool {
	if len(s) < 3 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validPassword(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func definite(err error) bool {
	e, ok := isAPIErr(err)
	return ok && e.Status >= 400 && e.Status < 500
}

// ---------------------------------------------------------------- buy

type buyResult struct {
	Account *AccountRow
	Live    *Account
	Price   int64
	Pending bool
}

func (b *Bot) buy(u *User, s *Server, proto, username, password string, days int64) (*buyResult, error) {
	if !s.Enabled || !s.Protos[proto] {
		return nil, uerr("err_server_closed")
	}
	if st := b.slotState(s); st == "full" {
		return nil, uerr("err_server_full")
	} else if st == "offline" {
		return nil, uerr("err_server_offline")
	}
	price := b.priceFor(u, s, proto, days)
	if price <= 0 {
		return nil, uerr("err_server_closed")
	}
	if b.db.usernameTaken(s.ID, proto, username) {
		return nil, uerr("err_username_taken")
	}
	if proto == "ssh" && password == "" {
		password = randPassword()
	}
	var orderID int64
	err := b.db.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO orders(user_id, kind, server_id, proto, username, password, days, amount, created_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, u.ID, "create", s.ID, proto, username, password, days, price, now())
		if err != nil {
			return err
		}
		orderID, _ = res.LastInsertId()
		_, err = addBalance(tx, u.ID, -price, "purchase", "order:"+strconv.FormatInt(orderID, 10),
			fmt.Sprintf("%s %s %dd", protoLabel[proto], username, days))
		return err
	})
	if errors.Is(err, errInsufficient) {
		return nil, uerr("err_balance", "need", rp(price), "balance", rp(u.Balance))
	}
	if err != nil {
		return nil, err
	}
	live, err := b.api(s).Create(proto, CreateReq{Username: username, Password: password, Days: int(days), IPLimit: s.IPLimit, QuotaGB: s.QuotaGB})
	if err != nil {
		if definite(err) {
			b.refund(orderID, err.Error())
			if e, _ := isAPIErr(err); e.Status == 409 {
				return nil, uerr("err_username_taken")
			}
			return nil, uerr("err_server_reject", "msg", err.Error())
		}
		b.db.logError("buy", err)
		_, _ = b.db.Exec(`UPDATE orders SET error=? WHERE id=?`, err.Error(), orderID)
		return &buyResult{Price: price, Pending: true}, nil
	}
	acc, err := b.finishCreate(orderID, live)
	if err != nil {
		return nil, err
	}
	return &buyResult{Account: acc, Live: live, Price: price}, nil
}

// finishCreate stores the account of a create order (idempotent).
func (b *Bot) finishCreate(orderID int64, live *Account) (*AccountRow, error) {
	var accID int64
	err := b.db.tx(func(tx *sql.Tx) error {
		var userID, serverID, days, amount int64
		var proto, username, password, status string
		if err := tx.QueryRow(`SELECT user_id, server_id, proto, username, password, days, amount, status FROM orders WHERE id=?`, orderID).
			Scan(&userID, &serverID, &proto, &username, &password, &days, &amount, &status); err != nil {
			return err
		}
		if status != "pending" {
			return tx.QueryRow(`SELECT account_id FROM orders WHERE id=?`, orderID).Scan(&accID)
		}
		exp := live.ExpiresAt
		if exp == 0 {
			exp = now() + days*86400
		}
		pw := live.Password
		if pw == "" {
			pw = password
		}
		t := now()
		res, err := tx.Exec(`INSERT INTO accounts(user_id, server_id, proto, username, password, uuid, status, expires_at, price_paid, created_at, updated_at)
			VALUES(?,?,?,?,?,?, 'active', ?,?,?,?)`, userID, serverID, proto, username, pw, live.UUID, exp, amount, t, t)
		if err != nil {
			return err
		}
		accID, _ = res.LastInsertId()
		if _, err := tx.Exec(`UPDATE orders SET status='done', account_id=?, done_at=? WHERE id=?`, accID, t, orderID); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE users SET total_spent=total_spent+?, total_create=total_create+1 WHERE id=?`, amount, userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return b.db.account(accID)
}

// refund returns the balance of a pending order (idempotent).
func (b *Bot) refund(orderID int64, reason string) {
	err := b.db.tx(func(tx *sql.Tx) error {
		var userID, amount int64
		var status string
		if err := tx.QueryRow(`SELECT user_id, amount, status FROM orders WHERE id=?`, orderID).Scan(&userID, &amount, &status); err != nil {
			return err
		}
		if status != "pending" {
			return nil
		}
		if len(reason) > 300 {
			reason = reason[:300]
		}
		if _, err := tx.Exec(`UPDATE orders SET status='refunded', error=?, done_at=? WHERE id=?`, reason, now(), orderID); err != nil {
			return err
		}
		_, err := addBalance(tx, userID, amount, "refund", "order:"+strconv.FormatInt(orderID, 10), "refund")
		return err
	})
	if err != nil {
		b.db.logError("refund", fmt.Errorf("order %d: %w", orderID, err))
	}
}

// ---------------------------------------------------------------- renew

func (b *Bot) renew(u *User, a *AccountRow, days int64) (*AccountRow, *Account, int64, bool, error) {
	s, err := b.db.server(a.ServerID)
	if err != nil {
		return nil, nil, 0, false, uerr("err_server_closed")
	}
	price := b.priceFor(u, s, a.Proto, days)
	if price <= 0 {
		return nil, nil, 0, false, uerr("err_server_closed")
	}
	var orderID int64
	err = b.db.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO orders(user_id, kind, server_id, proto, username, days, amount, account_id, prev_expires, created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, u.ID, "renew", s.ID, a.Proto, a.Username, days, price, a.ID, a.ExpiresAt, now())
		if err != nil {
			return err
		}
		orderID, _ = res.LastInsertId()
		_, err = addBalance(tx, u.ID, -price, "renew", "order:"+strconv.FormatInt(orderID, 10),
			fmt.Sprintf("%s %s +%dd", protoLabel[a.Proto], a.Username, days))
		return err
	})
	if errors.Is(err, errInsufficient) {
		return nil, nil, 0, false, uerr("err_balance", "need", rp(price), "balance", rp(u.Balance))
	}
	if err != nil {
		return nil, nil, 0, false, err
	}
	live, err := b.api(s).Renew(a.Proto, a.Username, int(days), s.IPLimit, s.QuotaGB)
	if err != nil {
		if definite(err) {
			b.refund(orderID, err.Error())
			if notFound(err) {
				_, _ = b.db.Exec(`UPDATE accounts SET status='deleted', updated_at=? WHERE id=?`, now(), a.ID)
				return nil, nil, 0, false, uerr("err_account_gone")
			}
			return nil, nil, 0, false, uerr("err_server_reject", "msg", err.Error())
		}
		b.db.logError("renew", err)
		_, _ = b.db.Exec(`UPDATE orders SET error=? WHERE id=?`, err.Error(), orderID)
		return nil, nil, price, true, nil
	}
	b.finishRenew(orderID, live.ExpiresAt)
	na, _ := b.db.account(a.ID)
	return na, live, price, false, nil
}

func (b *Bot) finishRenew(orderID, expires int64) {
	_ = b.db.tx(func(tx *sql.Tx) error {
		var accID, days, amount, userID, prev int64
		var status string
		if err := tx.QueryRow(`SELECT account_id, days, amount, user_id, prev_expires, status FROM orders WHERE id=?`, orderID).
			Scan(&accID, &days, &amount, &userID, &prev, &status); err != nil || status != "pending" {
			return err
		}
		if expires == 0 {
			base := prev
			if base < now() {
				base = now()
			}
			expires = base + days*86400
		}
		t := now()
		if _, err := tx.Exec(`UPDATE accounts SET expires_at=?, status='active', notified_at=0, price_paid=price_paid+?, updated_at=? WHERE id=?`,
			expires, amount, t, accID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE orders SET status='done', done_at=? WHERE id=?`, t, orderID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE users SET total_spent=total_spent+? WHERE id=?`, amount, userID)
		return err
	})
}

// ---------------------------------------------------------------- trial

// trialLeft — remaining trials: members per 7 days, resellers per day.
func (b *Bot) trialLeft(u *User) (left, quota int64, perDay bool) {
	if b.cfg.isAdmin(u.ID) {
		return 99, 99, true
	}
	if u.reseller() {
		quota = b.set.Int("trial_reseller_daily")
		used := b.db.int(`SELECT COUNT(*) FROM trial_logs WHERE user_id=? AND created_at>?`, u.ID, now()-86400)
		return max(quota-used, 0), quota, true
	}
	quota = b.set.Int("trial_member_weekly")
	used := b.db.int(`SELECT COUNT(*) FROM trial_logs WHERE user_id=? AND created_at>?`, u.ID, now()-7*86400)
	return max(quota-used, 0), quota, false
}

func (b *Bot) trial(u *User, s *Server, proto string) (*Account, int64, error) {
	if !b.set.Bool("trial_enabled") {
		return nil, 0, uerr("err_trial_off")
	}
	if left, _, _ := b.trialLeft(u); left <= 0 {
		return nil, 0, uerr("err_trial_used")
	}
	if !s.Enabled || !s.Protos[proto] || s.Status == "offline" {
		return nil, 0, uerr("err_server_offline")
	}
	mins := b.set.Int("trial_minutes")
	live, err := b.api(s).Trial(proto, int(mins))
	if err != nil {
		b.db.logError("trial", err)
		return nil, 0, uerr("err_server_reject", "msg", err.Error())
	}
	exp := live.ExpiresAt
	if exp == 0 {
		exp = now() + mins*60
	}
	t := now()
	_, _ = b.db.Exec(`INSERT INTO trial_logs(user_id, server_id, proto, username, created_at) VALUES(?,?,?,?,?)`, u.ID, s.ID, proto, live.Username, t)
	_, _ = b.db.Exec(`INSERT INTO accounts(user_id, server_id, proto, username, password, uuid, trial, status, expires_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?,1,'active',?,?,?)`, u.ID, s.ID, proto, live.Username, live.Password, live.UUID, exp, t, t)
	return live, exp, nil
}

// ---------------------------------------------------------------- recovery

// recoverOrders resolves orders left "pending" by a lost connection.
func (b *Bot) recoverOrders() {
	rows, err := b.db.Query(`SELECT id, user_id, kind, server_id, proto, username, account_id, prev_expires, days, created_at
		FROM orders WHERE status='pending' AND created_at < ? ORDER BY id LIMIT 50`, now()-90)
	if err != nil {
		return
	}
	type ord struct {
		id, user, server, acc, prev, days, created int64
		kind, proto, username                      string
	}
	var list []ord
	for rows.Next() {
		var o ord
		if rows.Scan(&o.id, &o.user, &o.kind, &o.server, &o.proto, &o.username, &o.acc, &o.prev, &o.days, &o.created) == nil {
			list = append(list, o)
		}
	}
	rows.Close()
	for _, o := range list {
		s, err := b.db.server(o.server)
		if err != nil {
			b.refund(o.id, "server deleted")
			continue
		}
		live, err := b.api(s).Get(o.proto, o.username)
		switch {
		case err == nil && o.kind == "create":
			if acc, err := b.finishCreate(o.id, live); err == nil {
				b.send(o.user, newCard(b.t("order_recovered")).text(b.t("order_recovered_body")).String(), nil)
				b.send(o.user, formatAccount(acc.Proto, "PREMIUM", live, acc.ExpiresAt), nil)
			}
		case err == nil && o.kind == "renew":
			if live.ExpiresAt > o.prev+o.days*86400/2 {
				b.finishRenew(o.id, live.ExpiresAt)
				b.send(o.user, newCard(b.t("order_recovered")).kv(b.t("lbl_username"), o.username).kv("Expired", fmtTime(live.ExpiresAt)).String(), nil)
			} else if time.Since(time.Unix(o.created, 0)) > 10*time.Minute {
				b.refund(o.id, "renew not applied")
				b.send(o.user, newCard(b.t("order_refunded")).kv(b.t("lbl_username"), o.username).text(b.t("order_refunded_body")).String(), nil)
			}
		case notFound(err) && o.kind == "create":
			b.refund(o.id, "account not created")
			b.send(o.user, newCard(b.t("order_refunded")).kv(b.t("lbl_username"), o.username).text(b.t("order_refunded_body")).String(), nil)
		case notFound(err) && o.kind == "renew":
			b.refund(o.id, "account not found")
			_, _ = b.db.Exec(`UPDATE accounts SET status='deleted', updated_at=? WHERE id=?`, now(), o.acc)
			b.send(o.user, newCard(b.t("order_refunded")).kv(b.t("lbl_username"), o.username).text(b.t("order_refunded_body")).String(), nil)
		default:
			// Server still unreachable: retry next round; give up after 24h.
			if now()-o.created > 86400 {
				b.db.logError("recover", fmt.Errorf("order %d still pending after 24h: %v", o.id, err))
				b.notifyAdmins(newCard(b.t("adm_pending_title")).kv("Order", strconv.FormatInt(o.id, 10)).kv(b.t("lbl_username"), o.username).text(b.t("adm_pending_body")).String())
				_, _ = b.db.Exec(`UPDATE orders SET created_at=? WHERE id=?`, now(), o.id) // remind daily
			}
		}
	}
}

func cleanUser(s string) string { return strings.TrimSpace(s) }
