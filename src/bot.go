package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State — what the bot waits for when the user types text (username,
// amount, setting value, ...).
type State struct {
	Kind  string
	Data  map[string]string
	MsgID int64 // message to edit after the input
	At    time.Time
}

type Bot struct {
	cfg  *Config
	db   *DB
	tg   *TG
	set  *Settings
	pay  *Payments
	txt  *Texts
	me   TGUser
	ctx  context.Context
	stop context.CancelFunc

	mu     sync.Mutex
	states map[int64]*State
	busy   map[int64]bool
	hits   map[int64][]time.Time
	muted  map[int64]time.Time
	subOK  map[int64]time.Time // force-subscribe cache

	bcWake chan struct{}
}

func newBot(cfg *Config, db *DB) *Bot {
	s := newSettings(db)
	b := &Bot{
		cfg:    cfg,
		db:     db,
		tg:     newTG(cfg.TelegramAPI, cfg.Token),
		set:    s,
		pay:    newPayments(s),
		states: map[int64]*State{},
		busy:   map[int64]bool{},
		hits:   map[int64][]time.Time{},
		muted:  map[int64]time.Time{},
		subOK:  map[int64]time.Time{},
		bcWake: make(chan struct{}, 1),
	}
	b.txt = newTexts(db, s)
	b.migrateLegacy()
	return b
}

// migrateLegacy moves the AutoGoPay key of 1.0.x from config.json into the
// payment settings (once).
func (b *Bot) migrateLegacy() {
	if b.cfg.AutoGoPayKey != "" && b.set.Str("gw_autogopay_key") == "" {
		_ = b.set.Set("gw_autogopay_key", b.cfg.AutoGoPayKey)
		_ = b.set.Set("gw_autogopay_on", "1")
		if b.cfg.AutoGoPayURL != "" {
			_ = b.set.Set("gw_autogopay_url", b.cfg.AutoGoPayURL)
		}
	}
	if b.cfg.AutoGoPayKey != "" {
		b.cfg.AutoGoPayKey, b.cfg.AutoGoPayURL = "", ""
		_ = saveConfig(b.cfg)
	}
}

func (b *Bot) t(key string, kv ...any) string { return b.txt.get(key, kv...) }

func (b *Bot) lang() int {
	if b.set.Str("lang") == "en" {
		return 1
	}
	return 0
}

// api returns the Tomato API client of a server.
func (b *Bot) api(s *Server) *Tomato {
	if s.Local {
		u := b.cfg.LocalAPIURL
		if s.URL != "" {
			u = s.URL
		}
		key := s.APIKey
		if key == "" {
			key = localKey()
		}
		return newTomato(u, key, 120*time.Second)
	}
	return newTomato(s.URL, s.APIKey, 120*time.Second)
}

// ---------------------------------------------------------------- state

func (b *Bot) setState(uid int64, kind string, msgID int64, data map[string]string) {
	if data == nil {
		data = map[string]string{}
	}
	b.mu.Lock()
	b.states[uid] = &State{Kind: kind, Data: data, MsgID: msgID, At: time.Now()}
	b.mu.Unlock()
}

func (b *Bot) getState(uid int64) *State {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.states[uid]
	if s != nil && time.Since(s.At) > 30*time.Minute {
		delete(b.states, uid)
		return nil
	}
	return s
}

func (b *Bot) clearState(uid int64) {
	b.mu.Lock()
	delete(b.states, uid)
	b.mu.Unlock()
}

// lock — one action per user at a time (double clicks are rejected).
func (b *Bot) lock(uid int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy[uid] {
		return false
	}
	b.busy[uid] = true
	return true
}

func (b *Bot) unlock(uid int64) {
	b.mu.Lock()
	delete(b.busy, uid)
	b.mu.Unlock()
}

// limited — more than 40 actions per minute mutes the user for 5 minutes.
func (b *Bot) limited(uid int64) (bool, bool) {
	if b.cfg.isAdmin(uid) {
		return false, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	nowT := time.Now()
	if until, ok := b.muted[uid]; ok {
		if nowT.Before(until) {
			return true, false
		}
		delete(b.muted, uid)
	}
	h := b.hits[uid]
	cut := 0
	for cut < len(h) && nowT.Sub(h[cut]) > time.Minute {
		cut++
	}
	h = append(h[cut:], nowT)
	b.hits[uid] = h
	if len(h) > 40 {
		b.muted[uid] = nowT.Add(5 * time.Minute)
		delete(b.hits, uid)
		return true, true
	}
	return false, false
}

// ---------------------------------------------------------------- output

// show edits msgID (callback navigation) or sends a new message. Photo
// messages (QRIS) cannot become text, so they are replaced.
func (b *Bot) show(chat, msgID int64, text string, kb Keyboard) int64 {
	if msgID > 0 {
		err := b.tg.Edit(chat, msgID, text, kb)
		if err == nil {
			return msgID
		}
		var te *tgError
		if errors.As(err, &te) && te.Code == 400 {
			b.tg.Delete(chat, msgID)
		} else {
			b.db.logError("edit", err)
		}
	}
	id, err := b.tg.Send(chat, text, kb)
	if err != nil {
		b.db.logError("send", err)
	}
	return id
}

func (b *Bot) send(chat int64, text string, kb Keyboard) int64 {
	id, err := b.tg.Send(chat, text, kb)
	if err != nil && !isForbidden(err) {
		b.db.logError("send", err)
	}
	if isForbidden(err) {
		_, _ = b.db.Exec(`UPDATE users SET blocked=1 WHERE id=?`, chat)
	}
	return id
}

// notifyAdmins sends to the log chat when set, otherwise to every admin.
func (b *Bot) notifyAdmins(text string) {
	if b.cfg.NotifyChat != 0 {
		if _, err := b.tg.Send(b.cfg.NotifyChat, text, nil); err == nil {
			return
		}
	}
	for _, a := range b.cfg.Admins {
		_, _ = b.tg.Send(a, text, nil)
	}
}

// ---------------------------------------------------------------- loop

func (b *Bot) run(ctx context.Context) error {
	var me TGUser
	for {
		var err error
		me, err = b.tg.GetMe()
		if err == nil {
			break
		}
		var te *tgError
		if errors.As(err, &te) && (te.Code == 401 || te.Code == 404) {
			return fmt.Errorf("telegram: bot token rejected (%d), run: tomato-store setup", te.Code)
		}
		log.Printf("telegram getMe: %v (retry in 10s)", err)
		sleepCtx(ctx, 10*time.Second)
		if ctx.Err() != nil {
			return nil
		}
	}
	b.me = me
	b.ctx = ctx
	if err := b.db.ensureLocalServer(); err != nil {
		log.Printf("local server: %v", err)
	}
	log.Printf("tomato-store %s started as @%s", version, me.Username)
	go b.jobs(ctx)

	var offset int64
	sem := make(chan struct{}, 16) // max concurrent updates
	for ctx.Err() == nil {
		ups, err := b.tg.Updates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var te *tgError
			if errors.As(err, &te) && te.Code == 409 {
				log.Printf("telegram: another instance is polling this bot (409); retry in 10s")
				sleepCtx(ctx, 10*time.Second)
				continue
			}
			if errors.As(err, &te) && te.Code == 401 {
				return errors.New("telegram: bot token rejected (401)")
			}
			log.Printf("getUpdates: %v", err)
			sleepCtx(ctx, 3*time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			sem <- struct{}{}
			go func(u Update) {
				defer func() { <-sem }()
				b.handle(u)
			}(u)
		}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *Bot) handle(u Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("panic: %v\n%s", r, debug.Stack())
			b.db.logError("panic", fmt.Errorf("%v", r))
		}
	}()
	switch {
	case u.CallbackQuery != nil:
		b.onCallback(u.CallbackQuery)
	case u.Message != nil:
		b.onMessage(u.Message)
	case u.MyChatMember != nil:
		m := u.MyChatMember
		if m.Chat.Type == "private" {
			blocked := m.NewChatMember.Status == "kicked"
			_, _ = b.db.Exec(`UPDATE users SET blocked=? WHERE id=?`, blocked, m.From.ID)
		}
	}
}

// gate — checks every member action: ban, rate limit, maintenance and the
// required channel. Admins always pass.
func (b *Bot) gate(f TGUser, chat, msgID int64, cbID string) (*User, bool) {
	if lim, first := b.limited(f.ID); lim {
		if first {
			b.db.audit(f.ID, "rate_limit", strconv.FormatInt(f.ID, 10), "muted 5 minutes")
			if cbID != "" {
				b.tg.Answer(cbID, b.t("rate_limited"), true)
			} else {
				b.send(chat, b.t("rate_limited"), nil)
			}
		} else if cbID != "" {
			b.tg.Answer(cbID, "", false)
		}
		return nil, false
	}
	u, err := b.db.touchUser(f)
	if err != nil {
		b.db.logError("user", err)
		return nil, false
	}
	if b.cfg.isAdmin(f.ID) {
		return u, true
	}
	if u.Banned {
		if cbID != "" {
			b.tg.Answer(cbID, b.t("banned"), true)
		} else {
			b.send(chat, b.t("banned"), nil)
		}
		return nil, false
	}
	if b.set.Bool("maintenance") {
		if cbID != "" {
			b.tg.Answer(cbID, "", false)
		}
		b.show(chat, msgID, b.t("maintenance_msg"), nil)
		return nil, false
	}
	if ch := b.set.Str("force_channel"); ch != "" && !b.subscribed(ch, f.ID) {
		if cbID != "" {
			b.tg.Answer(cbID, "", false)
		}
		link := "https://t.me/" + strings.TrimPrefix(ch, "@")
		kb := kbd(row(btnURL(b.t("btn_join"), link)), row(ok(b.t("btn_joined"), "m")))
		if strings.HasPrefix(ch, "-100") {
			kb = kbd(row(ok(b.t("btn_joined"), "m")))
		}
		b.show(chat, msgID, newCard(b.t("join_title")).kv(b.t("lbl_channel"), ch).line().text(b.t("join_body")).String(), kb)
		return nil, false
	}
	return u, true
}

func (b *Bot) subscribed(channel string, uid int64) bool {
	b.mu.Lock()
	at, ok := b.subOK[uid]
	b.mu.Unlock()
	if ok && time.Since(at) < 5*time.Minute {
		return true
	}
	st, err := b.tg.MemberStatus(channel, uid)
	if err != nil {
		// Bot not admin in the channel / channel wrong: do not lock buyers out.
		b.db.logError("force_channel", err)
		return true
	}
	switch st {
	case "member", "administrator", "creator", "restricted":
		b.mu.Lock()
		b.subOK[uid] = time.Now()
		b.mu.Unlock()
		return true
	}
	return false
}

func (b *Bot) onMessage(m *Message) {
	if m.From == nil || m.From.IsBot || m.Chat.Type != "private" {
		return
	}
	f := *m.From
	text := strings.TrimSpace(m.Text)
	cmd := ""
	if strings.HasPrefix(text, "/") {
		cmd = strings.ToLower(strings.SplitN(strings.Fields(text)[0], "@", 2)[0])
	}
	// Admin-only inputs that are files/photos (restore, broadcast photo).
	if st := b.getState(f.ID); st != nil && cmd == "" && b.cfg.isAdmin(f.ID) {
		if b.adminInput(m, st) {
			return
		}
	}
	u, okGate := b.gate(f, m.Chat.ID, 0, "")
	if !okGate {
		return
	}
	switch cmd {
	case "/start", "/menu":
		b.clearState(f.ID)
		b.mainMenu(u, m.Chat.ID, 0)
		return
	case "/admin":
		b.clearState(f.ID)
		if b.cfg.isAdmin(f.ID) {
			b.adminMenu(m.Chat.ID, 0)
		}
		return
	case "/id":
		b.send(m.Chat.ID, newCard("TELEGRAM ID").kv("ID", strconv.FormatInt(f.ID, 10)).String(), nil)
		return
	case "/cancel", "/batal":
		b.clearState(f.ID)
		b.mainMenu(u, m.Chat.ID, 0)
		return
	}
	if cmd != "" {
		b.mainMenu(u, m.Chat.ID, 0)
		return
	}
	st := b.getState(f.ID)
	if st == nil {
		b.mainMenu(u, m.Chat.ID, 0)
		return
	}
	if !b.lock(f.ID) {
		b.send(m.Chat.ID, b.t("busy"), nil)
		return
	}
	defer b.unlock(f.ID)
	b.userInput(u, m, st, text)
}

func (b *Bot) onCallback(q *CallbackQuery) {
	if q.Message == nil {
		b.tg.Answer(q.ID, "", false)
		return
	}
	chat, msgID := q.Message.Chat.ID, q.Message.MessageID
	if q.Message.Chat.Type != "private" {
		b.tg.Answer(q.ID, "", false)
		return
	}
	u, okGate := b.gate(q.From, chat, msgID, q.ID)
	if !okGate {
		return
	}
	if !b.lock(q.From.ID) {
		b.tg.Answer(q.ID, b.t("busy"), false)
		return
	}
	defer b.unlock(q.From.ID)
	data := q.Data
	if strings.HasPrefix(data, "a:") || data == "a" {
		if !b.cfg.isAdmin(q.From.ID) {
			b.tg.Answer(q.ID, "", false)
			return
		}
		b.adminCallback(u, q, chat, msgID, data)
		return
	}
	b.userCallback(u, q, chat, msgID, data)
}

func parseID(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func tomatoInstalled() (string, error) {
	for _, p := range []string{"/usr/local/bin/tomato-agent", "/etc/tomato"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("Tomato Autoscript not found")
}
