package main

// End-to-end tests with a fake Telegram Bot API, a fake Tomato API and fake
// payment gateways. Run: go test ./...

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- fakes

type fakeTG struct {
	mu    sync.Mutex
	next  int64
	calls []tgCall
}

type tgCall struct {
	Method string
	Body   map[string]any
}

func (f *fakeTG) handler(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	body := map[string]any{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = r.ParseMultipartForm(10 << 20)
		for k, v := range r.MultipartForm.Value {
			body[k] = v[0]
		}
	} else {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.mu.Lock()
	f.next++
	id := f.next
	f.calls = append(f.calls, tgCall{method, body})
	f.mu.Unlock()
	var res any = true
	switch method {
	case "getMe":
		res = map[string]any{"id": 1, "is_bot": true, "first_name": "Store", "username": "store_bot"}
	case "sendMessage", "sendPhoto", "sendDocument":
		res = map[string]any{"message_id": id, "chat": map[string]any{"id": body["chat_id"]}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
}

func (f *fakeTG) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if t, ok := c.Body["text"].(string); ok {
			out = append(out, t)
		}
		if t, ok := c.Body["caption"].(string); ok {
			out = append(out, t)
		}
	}
	return out
}

func (f *fakeTG) last() string {
	t := f.texts()
	if len(t) == 0 {
		return ""
	}
	return t[len(t)-1]
}

// recent: any of the last n messages contains sub.
func (f *fakeTG) recent(sub string, n int) bool {
	t := f.texts()
	for i := len(t) - 1; i >= 0 && i >= len(t)-n; i-- {
		if strings.Contains(t[i], sub) {
			return true
		}
	}
	return false
}

func (f *fakeTG) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

type fakeTomato struct {
	mu       sync.Mutex
	accounts map[string]map[string]any // proto/user → account
	createFn func(w http.ResponseWriter) bool
	creates  int
}

func (f *fakeTomato) handler(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer testkey" {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "Invalid or missing API key"})
		return
	}
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1"), "/"), "/")
	ok := func(code int, data any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "message": "ok", "data": data})
	}
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": msg})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(p) == 1 && p[0] == "ping":
		ok(200, map[string]string{"version": "1.0.0"})
	case len(p) == 2 && p[1] == "accounts" && r.Method == "POST":
		f.creates++
		if f.createFn != nil && f.createFn(w) {
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		user := req["username"].(string)
		key := p[0] + "/" + strings.ToLower(user)
		if _, exists := f.accounts[key]; exists {
			fail(409, "Username already exists")
			return
		}
		days := int64(req["days"].(float64))
		acc := map[string]any{"username": user, "password": req["password"], "uuid": "1111-2222", "status": "active",
			"ip_limit": req["ip_limit"], "quota_bytes": 0, "expires_at": time.Now().Unix() + days*86400, "domain": "sg1.example.com"}
		f.accounts[key] = acc
		ok(201, acc)
	case len(p) == 2 && p[1] == "trial" && r.Method == "POST":
		acc := map[string]any{"username": "trial12345", "password": "trial12345", "status": "active", "ip_limit": 1,
			"expires_at": time.Now().Unix() + 3600, "domain": "sg1.example.com"}
		f.accounts[p[0]+"/trial12345"] = acc
		ok(201, acc)
	case len(p) == 3 && p[1] == "accounts" && r.Method == "GET":
		acc, exists := f.accounts[p[0]+"/"+strings.ToLower(p[2])]
		if !exists {
			fail(404, "account not found")
			return
		}
		ok(200, acc)
	case len(p) == 4 && p[3] == "renew":
		acc, exists := f.accounts[p[0]+"/"+strings.ToLower(p[2])]
		if !exists {
			fail(404, "account not found")
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		acc["expires_at"] = int64(acc["expires_at"].(int64)) + int64(req["days"].(float64))*86400
		ok(200, acc)
	default:
		fail(404, "Unknown endpoint")
	}
}

type testEnv struct {
	t      *testing.T
	bot    *Bot
	tg     *fakeTG
	tomato *fakeTomato
	srv    *Server
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	etcDir, dataDir = dir, dir
	configFile = filepath.Join(dir, "config.json")
	textsFile = filepath.Join(dir, "texts.json")
	logoFile = filepath.Join(dir, "logo.png")
	dbFile = filepath.Join(dir, "store.db")
	backupDir = filepath.Join(dir, "backups")
	tomatoKey = filepath.Join(dir, "api.key")
	tomatoXray = filepath.Join(dir, "domain")

	ftg := &fakeTG{}
	tgs := httptest.NewServer(http.HandlerFunc(ftg.handler))
	t.Cleanup(tgs.Close)
	ft := &fakeTomato{accounts: map[string]map[string]any{}}
	ts := httptest.NewServer(http.HandlerFunc(ft.handler))
	t.Cleanup(ts.Close)

	db, err := openDB(dbFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &Config{Token: "123:abc", Admins: []int64{999}, TelegramAPI: tgs.URL, LocalAPIURL: defaultAPI}
	b := newBot(cfg, db)
	_, err = db.Exec(`INSERT INTO servers(name, url, api_key, status, created_at) VALUES('SG Test', ?, 'testkey', 'online', ?)`, ts.URL+"/api/v1", now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range protoList {
		_ = db.setPrice(1, p, 10000)
	}
	s, _ := db.server(1)
	return &testEnv{t: t, bot: b, tg: ftg, tomato: ft, srv: s}
}

func (e *testEnv) user(id, balance int64) *User {
	_, err := e.bot.db.touchUser(TGUser{ID: id, FirstName: "Akmal"})
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = e.bot.db.Exec(`UPDATE users SET balance=? WHERE id=?`, balance, id)
	u, _ := e.bot.db.user(id)
	return u
}

func (e *testEnv) click(uid int64, data string) {
	e.bot.onCallback(&CallbackQuery{ID: "q", From: TGUser{ID: uid, FirstName: "Akmal"}, Data: data,
		Message: &Message{MessageID: 50, Chat: Chat{ID: uid, Type: "private"}}})
}

func (e *testEnv) say(uid int64, text string) {
	e.bot.onMessage(&Message{MessageID: 60, From: &TGUser{ID: uid, FirstName: "Akmal"}, Chat: Chat{ID: uid, Type: "private"}, Text: text})
}

func (e *testEnv) balance(uid int64) int64 {
	return e.bot.db.int(`SELECT balance FROM users WHERE id=?`, uid)
}

// ---------------------------------------------------------------- tests

func TestBuyFlow(t *testing.T) {
	e := newEnv(t)
	e.user(10, 25000)
	e.click(10, "buy")
	if !strings.Contains(e.tg.last(), "BELI AKUN") {
		t.Fatalf("buy menu: %q", e.tg.last())
	}
	e.click(10, "buy:p:ssh")
	if !strings.Contains(e.tg.last(), "SG Test") || !strings.Contains(e.tg.last(), "Rp 10.000") {
		t.Fatalf("server list: %q", e.tg.last())
	}
	e.click(10, "buy:d:ssh:1:30")
	e.say(10, "akmal01:rahasia")
	if !strings.Contains(e.tg.last(), "KONFIRMASI") || !strings.Contains(e.tg.last(), "rahasia") {
		t.Fatalf("confirm: %q", e.tg.last())
	}
	e.click(10, "buy:ok")
	if !e.tg.recent("SSH PREMIUM", 2) || !e.tg.recent("PENJUALAN", 1) {
		t.Fatalf("result: %q", e.tg.texts())
	}
	if b := e.balance(10); b != 15000 {
		t.Fatalf("balance = %d, want 15000", b)
	}
	if n := e.bot.db.int(`SELECT COUNT(*) FROM accounts WHERE username='akmal01' AND status='active'`); n != 1 {
		t.Fatalf("accounts = %d", n)
	}
	// The same username again is rejected before charging.
	e.click(10, "buy:d:ssh:1:30")
	e.say(10, "akmal01")
	if !strings.Contains(e.tg.last(), "sudah dipakai") {
		t.Fatalf("duplicate: %q", e.tg.last())
	}
	// Renew 30 days.
	e.click(10, "acc:ro:1:30")
	if !e.tg.recent("PERPANJANG BERHASIL", 2) {
		t.Fatalf("renew: %q", e.tg.last())
	}
	if b := e.balance(10); b != 5000 {
		t.Fatalf("balance after renew = %d", b)
	}
}

func TestBuyRefundWhenServerRejects(t *testing.T) {
	e := newEnv(t)
	e.user(11, 10000)
	e.tomato.accounts["vless/taken"] = map[string]any{"username": "taken"}
	e.click(11, "buy:d:vless:1:30")
	e.say(11, "taken")
	e.click(11, "buy:ok")
	if !strings.Contains(e.tg.last(), "sudah dipakai") {
		t.Fatalf("expected taken message, got %q", e.tg.last())
	}
	if b := e.balance(11); b != 10000 {
		t.Fatalf("balance = %d, want refund to 10000", b)
	}
	if n := e.bot.db.int(`SELECT COUNT(*) FROM orders WHERE status='refunded'`); n != 1 {
		t.Fatalf("refunded orders = %d", n)
	}
}

func TestBuyPendingThenRecovered(t *testing.T) {
	e := newEnv(t)
	e.user(12, 10000)
	// Server creates the account but the answer is lost (502, no JSON).
	e.tomato.createFn = func(w http.ResponseWriter) bool {
		e.tomato.accounts["vmess/lost"] = map[string]any{"username": "lost", "uuid": "abc", "status": "active",
			"expires_at": time.Now().Unix() + 30*86400, "domain": "sg1.example.com"}
		w.WriteHeader(502)
		_, _ = io.WriteString(w, "<html>bad gateway</html>")
		return true
	}
	e.click(12, "buy:d:vmess:1:30")
	e.say(12, "lost")
	e.click(12, "buy:ok")
	if !strings.Contains(e.tg.last(), "SEDANG DIPROSES") {
		t.Fatalf("pending: %q", e.tg.last())
	}
	if b := e.balance(12); b != 0 {
		t.Fatalf("balance while pending = %d", b)
	}
	_, _ = e.bot.db.Exec(`UPDATE orders SET created_at=created_at-600`)
	e.bot.recoverOrders()
	if n := e.bot.db.int(`SELECT COUNT(*) FROM orders WHERE status='done'`); n != 1 {
		t.Fatalf("done orders = %d", n)
	}
	if !strings.Contains(e.tg.last(), "VMess PREMIUM") {
		t.Fatalf("config not delivered: %q", e.tg.last())
	}
	// Second case: account was never created → refund.
	e.tomato.createFn = func(w http.ResponseWriter) bool { w.WriteHeader(502); return true }
	_, _ = e.bot.db.Exec(`UPDATE users SET balance=10000 WHERE id=12`)
	e.click(12, "buy:d:vmess:1:30")
	e.say(12, "ghost")
	e.click(12, "buy:ok")
	_, _ = e.bot.db.Exec(`UPDATE orders SET created_at=created_at-600`)
	e.bot.recoverOrders()
	if b := e.balance(12); b != 10000 {
		t.Fatalf("balance after refund = %d", b)
	}
}

func TestTrialQuota(t *testing.T) {
	e := newEnv(t)
	e.user(13, 0)
	e.click(13, "trial:p:ssh")
	if !strings.Contains(e.tg.last(), "SSH TRIAL") {
		t.Fatalf("trial: %q", e.tg.last())
	}
	e.click(13, "trial:p:ssh")
	if !strings.Contains(e.tg.last(), "habis") {
		t.Fatalf("second trial should be refused: %q", e.tg.last())
	}
}

func TestCreditInvoiceOnceWithCashback(t *testing.T) {
	e := newEnv(t)
	e.user(14, 0)
	_ = e.bot.set.Set("cashback_enabled", "1")
	_ = e.bot.set.Set("cashback_percent", "5")
	_ = e.bot.set.Set("cashback_min", "50000")
	res, err := e.bot.db.Exec(`INSERT INTO invoices(user_id, amount, total, trx_id, order_ref, status, gateway, method, expires_at, created_at)
		VALUES(14, 50000, 50350, 'trx1', 'TS-1', 'pending', 'midtrans', 'qris', ?, ?)`, now()+900, now())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	inv, _ := e.bot.db.invoice(id)
	c1, bal, cb := e.bot.creditInvoice(inv)
	c2, _, _ := e.bot.creditInvoice(inv)
	if !c1 || c2 {
		t.Fatalf("credited %v %v", c1, c2)
	}
	if cb != 2500 || bal != 52500 || e.balance(14) != 52500 {
		t.Fatalf("cb=%d bal=%d db=%d", cb, bal, e.balance(14))
	}
}

func TestPricing(t *testing.T) {
	e := newEnv(t)
	u := e.user(15, 0)
	if p := e.bot.priceFor(u, e.srv, "ssh", 7); p != 2400 {
		t.Fatalf("7 days = %d, want 2400", p)
	}
	_, _ = e.bot.db.Exec(`UPDATE users SET role='reseller' WHERE id=15`)
	u, _ = e.bot.db.user(15)
	if p := e.bot.priceFor(u, e.srv, "ssh", 30); p != 8000 {
		t.Fatalf("reseller 30 days = %d, want 8000", p)
	}
}

func TestParseMoney(t *testing.T) {
	for in, want := range map[string]int64{"10000": 10000, "10.000": 10000, "Rp 25.000": 25000, "50k": 50000, "1jt": 1000000, "15rb": 15000} {
		if got, err := parseMoney(in); err != nil || got != want {
			t.Errorf("parseMoney(%q) = %d, %v", in, got, err)
		}
	}
	if _, err := parseMoney("abc"); err == nil {
		t.Error("abc accepted")
	}
}

// A 1.0.x database (no gateway columns) opens and is migrated.
func TestMigrateOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(baseSchema); err != nil {
		t.Fatal(err)
	}
	_, _ = old.Exec(`INSERT INTO invoices(user_id, amount, trx_id, expires_at, created_at) VALUES(1, 20000, 'agp-1', 0, 0)`)
	old.Close()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	inv, err := db.invoice(1)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Gateway != "autogopay" || inv.Total != 20000 || inv.OrderRef != "agp-1" {
		t.Fatalf("migrated invoice = %+v", inv)
	}
}

func TestMidtransAndPakasir(t *testing.T) {
	e := newEnv(t)
	mt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/charge":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			td := req["transaction_details"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": "201", "transaction_id": "mt-1", "qr_string": "000201...",
				"gross_amount": td["gross_amount"], "expiry_time": "2030-01-01 00:15:00"})
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": "200", "transaction_status": "settlement", "gross_amount": "50350.00"})
		}
	}))
	defer mt.Close()
	midtransBase = mt.URL
	defer func() { midtransBase = "" }()
	pk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/transactioncreate/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"payment": map[string]any{"amount": 50000, "fee": 3500, "total_payment": 53500,
				"payment_number": "880123456789", "expired_at": "2030-01-01T00:00:00Z"}})
		case r.URL.Path == "/api/transactiondetail":
			if r.URL.Query().Get("amount") != "50000" {
				t.Errorf("pakasir status amount = %s", r.URL.Query().Get("amount"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"transaction": map[string]any{"status": "completed"}})
		}
	}))
	defer pk.Close()
	pakasirBase = pk.URL
	for k, v := range map[string]string{"gw_midtrans_on": "1", "gw_midtrans_key": "SB-key", "gw_pakasir_on": "1",
		"gw_pakasir_project": "store", "gw_pakasir_key": "pk", "gw_pakasir_methods": "qris,bri_va"} {
		_ = e.bot.set.Set(k, v)
	}
	u := e.user(16, 0)
	inv, err := e.bot.newInvoice(u, "midtrans", "qris", 50000)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Total != 50350 || inv.Fee != 350 || inv.Code == "" {
		t.Fatalf("midtrans invoice = %+v", inv)
	}
	if st := e.bot.checkInvoice(inv); st != "paid" || e.balance(16) != 50000 {
		t.Fatalf("midtrans status %s balance %d", st, e.balance(16))
	}
	inv2, err := e.bot.newInvoice(u, "pakasir", "bri_va", 50000)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.Total != 53500 || inv2.Code != "880123456789" {
		t.Fatalf("pakasir invoice = %+v", inv2)
	}
	if st := e.bot.checkInvoice(inv2); st != "paid" || e.balance(16) != 100000 {
		t.Fatalf("pakasir status %s balance %d", st, e.balance(16))
	}
	// Method choice is shown when several are active.
	e.click(16, "top:a:50000")
	if !strings.Contains(e.tg.last(), "METODE BAYAR") {
		t.Fatalf("methods: %q", e.tg.last())
	}
}

func TestAdminPanelsRender(t *testing.T) {
	e := newEnv(t)
	e.user(999, 0)
	for _, d := range []string{"a", "a:dash", "a:stat", "a:pgw", "a:srv", "a:s:1", "a:s:1:price", "a:s:1:proto", "a:pay",
		"a:g:autogopay", "a:g:midtrans", "a:g:pakasir", "a:g:pakasir:m", "a:user", "a:u:999", "a:bc", "a:cb", "a:set",
		"a:set:g:store", "a:set:g:topup", "a:set:g:trial", "a:set:g:reseller", "a:set:g:account", "a:set:g:backup",
		"a:txt", "a:txt:welcome", "a:tools", "a:t:err", "a:t:audit", "a:t:exp"} {
		before := len(e.tg.texts())
		e.click(999, d)
		if len(e.tg.texts()) == before {
			t.Errorf("%s: nothing rendered", d)
			continue
		}
		if txt := e.tg.last(); regexp.MustCompile(`\b(lbl|btn|err|srv|adm)_[a-z_]+\b`).MatchString(txt) {
			t.Errorf("%s: untranslated key in %q", d, txt)
		}
	}
	// Members cannot open admin callbacks.
	e.user(20, 0)
	before := len(e.tg.texts())
	e.click(20, "a:dash")
	if len(e.tg.texts()) != before {
		t.Error("member opened the admin panel")
	}
}

func TestSettingEditor(t *testing.T) {
	e := newEnv(t)
	e.user(999, 0)
	e.click(999, "a:set:e:durations")
	e.say(999, "30, 7, 15")
	if got := e.bot.set.Str("durations"); got != "7,15,30" {
		t.Fatalf("durations = %q", got)
	}
	e.click(999, "a:set:t:maintenance")
	if !e.bot.set.Bool("maintenance") {
		t.Fatal("maintenance not toggled")
	}
	e.user(21, 0)
	e.say(21, "/start")
	if !strings.Contains(e.tg.last(), "MAINTENANCE") {
		t.Fatalf("maintenance not shown: %q", e.tg.last())
	}
}

// Every text key used in the code exists in both languages.
func TestTextKeys(t *testing.T) {
	re := regexp.MustCompile(`(?:b\.t\(|alert\(q, |uerr\(|again\()"([a-z0-9_]+)"`)
	files, _ := filepath.Glob("*.go")
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f)
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if !strings.HasSuffix(m[1], "_") {
				seen[m[1]] = true
			}
		}
	}
	for _, k := range editableTexts {
		seen[k] = true
		seen["txtname_"+k] = true
	}
	for k := range seen {
		d, ok := defaultTexts[k]
		if !ok {
			t.Errorf("missing text key %q", k)
			continue
		}
		if d[0] == "" || d[1] == "" {
			t.Errorf("text %q missing a language", k)
		}
	}
	if len(seen) < 100 {
		t.Fatalf("only %d keys found, regexp broken?", len(seen))
	}
}

func TestCardWidth(t *testing.T) {
	if n := len([]rune(cardLine)); n != 27 {
		t.Fatalf("card line = %d chars, want 27", n)
	}
	s := newCard("X").kv("A", "<b>").line().String()
	if strings.HasSuffix(s, cardLine) || !strings.Contains(s, "&lt;b&gt;") {
		t.Fatalf("card = %q", s)
	}
}
