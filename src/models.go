package main

import (
	"database/sql"
	"errors"
	"strings"
)

type User struct {
	ID          int64
	Username    string
	Name        string
	Balance     int64
	Role        string
	Banned      bool
	Blocked     bool
	Notify      bool
	TotalTopup  int64
	TotalSpent  int64
	TotalCreate int64
	CreatedAt   int64
}

func (u *User) reseller() bool { return u.Role == "reseller" }

const userCols = `id, username, name, balance, role, banned, blocked, notify, total_topup, total_spent, total_create, created_at`

func scanUser(r interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := r.Scan(&u.ID, &u.Username, &u.Name, &u.Balance, &u.Role, &u.Banned, &u.Blocked, &u.Notify,
		&u.TotalTopup, &u.TotalSpent, &u.TotalCreate, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (db *DB) user(id int64) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

// touchUser creates or refreshes a user from a Telegram update.
func (db *DB) touchUser(f TGUser) (*User, error) {
	t := now()
	_, err := db.Exec(`INSERT INTO users(id, username, name, created_at, last_seen) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET username=excluded.username, name=excluded.name, last_seen=excluded.last_seen, blocked=0`,
		f.ID, f.Username, f.name(), t, t)
	if err != nil {
		return nil, err
	}
	return db.user(f.ID)
}

func (db *DB) findUser(q string) (*User, error) {
	q = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	if n, err := parseID(q); err == nil {
		return db.user(n)
	}
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE lower(username)=lower(?)`, q))
}

// addBalance changes a balance inside tx and writes the ledger row.
// Fails when the balance would go below zero.
func addBalance(tx *sql.Tx, user int64, amount int64, typ, ref, note string) (int64, error) {
	res, err := tx.Exec(`UPDATE users SET balance = balance + ? WHERE id=? AND balance + ? >= 0`, amount, user, amount)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, errInsufficient
	}
	var bal int64
	if err := tx.QueryRow(`SELECT balance FROM users WHERE id=?`, user).Scan(&bal); err != nil {
		return 0, err
	}
	_, err = tx.Exec(`INSERT INTO transactions(user_id, type, amount, balance_after, ref, note, created_at) VALUES(?,?,?,?,?,?,?)`,
		user, typ, amount, bal, ref, note, now())
	return bal, err
}

var errInsufficient = errors.New("insufficient balance")

// ---------------------------------------------------------------- servers

type Server struct {
	ID          int64
	Name        string
	Location    string
	URL         string
	APIKey      string
	Local       bool
	Enabled     bool
	Protos      map[string]bool
	QuotaGB     int
	IPLimit     int
	MaxAccounts int
	Status      string
	LastCheck   int64
	LastError   string
	LatencyMs   int64
	DownSince   int64
}

const serverCols = `id, name, location, url, api_key, local, enabled, protos, quota_gb, ip_limit, max_accounts, status, last_check, last_error, latency_ms, down_since`

func scanServer(r interface{ Scan(...any) error }) (*Server, error) {
	var s Server
	var protos string
	err := r.Scan(&s.ID, &s.Name, &s.Location, &s.URL, &s.APIKey, &s.Local, &s.Enabled, &protos,
		&s.QuotaGB, &s.IPLimit, &s.MaxAccounts, &s.Status, &s.LastCheck, &s.LastError, &s.LatencyMs, &s.DownSince)
	if err != nil {
		return nil, err
	}
	s.Protos = map[string]bool{}
	for _, p := range strings.Split(protos, ",") {
		if p = strings.TrimSpace(p); isProto(p) {
			s.Protos[p] = true
		}
	}
	return &s, nil
}

func (s *Server) protoString() string {
	var out []string
	for _, p := range protoList {
		if s.Protos[p] {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}

func (db *DB) server(id int64) (*Server, error) {
	return scanServer(db.QueryRow(`SELECT `+serverCols+` FROM servers WHERE id=?`, id))
}

func (db *DB) servers(onlyEnabled bool) ([]*Server, error) {
	q := `SELECT ` + serverCols + ` FROM servers`
	if onlyEnabled {
		q += ` WHERE enabled=1`
	}
	rows, err := db.Query(q + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (db *DB) price(server int64, proto string) int64 {
	return db.int(`SELECT price FROM prices WHERE server_id=? AND proto=?`, server, proto)
}

func (db *DB) setPrice(server int64, proto string, price int64) error {
	_, err := db.Exec(`INSERT INTO prices(server_id, proto, price) VALUES(?,?,?)
		ON CONFLICT(server_id, proto) DO UPDATE SET price=excluded.price`, server, proto, price)
	return err
}

func (db *DB) activeCount(server int64) int64 {
	return db.int(`SELECT COUNT(*) FROM accounts WHERE server_id=? AND status IN ('active','pending') AND trial=0`, server)
}

// ensureLocalServer registers this VPS on first start (default price
// Rp10.000 / 30 days for every protocol).
func (db *DB) ensureLocalServer() error {
	if db.int(`SELECT COUNT(*) FROM servers WHERE local=1`) > 0 {
		return nil
	}
	if _, err := tomatoInstalled(); err != nil {
		return nil // not a Tomato VPS: servers are added from /admin
	}
	return db.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO servers(name, location, local, created_at) VALUES(?,?,1,?)`, localDomain(), "", now())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		for _, p := range protoList {
			if _, err := tx.Exec(`INSERT INTO prices(server_id, proto, price) VALUES(?,?,10000)`, id, p); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------- accounts

type AccountRow struct {
	ID        int64
	UserID    int64
	ServerID  int64
	Proto     string
	Username  string
	Password  string
	Trial     bool
	Status    string
	ExpiresAt int64
	PricePaid int64
	CreatedAt int64
}

const accountCols = `id, user_id, server_id, proto, username, password, trial, status, expires_at, price_paid, created_at`

func scanAccount(r interface{ Scan(...any) error }) (*AccountRow, error) {
	var a AccountRow
	err := r.Scan(&a.ID, &a.UserID, &a.ServerID, &a.Proto, &a.Username, &a.Password, &a.Trial, &a.Status, &a.ExpiresAt, &a.PricePaid, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (db *DB) account(id int64) (*AccountRow, error) {
	return scanAccount(db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id=?`, id))
}

func (db *DB) userAccounts(user int64) ([]*AccountRow, error) {
	rows, err := db.Query(`SELECT `+accountCols+` FROM accounts WHERE user_id=? AND status='active' ORDER BY expires_at`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AccountRow
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// usernameTaken — the bot's own record (any user, active) on that server.
func (db *DB) usernameTaken(server int64, proto, username string) bool {
	return db.int(`SELECT COUNT(*) FROM accounts WHERE server_id=? AND proto=? AND lower(username)=lower(?) AND status IN ('active','pending')`,
		server, proto, username) > 0
}

// ---------------------------------------------------------------- invoices

type Invoice struct {
	ID        int64
	UserID    int64
	Amount    int64
	Total     int64
	Fee       int64
	TrxID     string
	OrderRef  string
	URL       string
	Status    string
	Gateway   string
	Method    string
	Code      string
	Cashback  int64
	ChatID    int64
	MsgID     int64
	ExpiresAt int64
	CreatedAt int64
	PaidAt    int64
	CheckedAt int64
}

const invoiceCols = `id, user_id, amount, total, fee, trx_id, order_ref, checkout_url, status, gateway, method, pay_code, cashback, chat_id, msg_id, expires_at, created_at, paid_at, checked_at`

func scanInvoice(r interface{ Scan(...any) error }) (*Invoice, error) {
	var i Invoice
	err := r.Scan(&i.ID, &i.UserID, &i.Amount, &i.Total, &i.Fee, &i.TrxID, &i.OrderRef, &i.URL, &i.Status, &i.Gateway, &i.Method,
		&i.Code, &i.Cashback, &i.ChatID, &i.MsgID, &i.ExpiresAt, &i.CreatedAt, &i.PaidAt, &i.CheckedAt)
	if err != nil {
		return nil, err
	}
	if i.Total == 0 {
		i.Total = i.Amount
	}
	if i.OrderRef == "" {
		i.OrderRef = i.TrxID
	}
	return &i, nil
}

func (db *DB) invoice(id int64) (*Invoice, error) {
	return scanInvoice(db.QueryRow(`SELECT `+invoiceCols+` FROM invoices WHERE id=?`, id))
}

func (db *DB) pendingInvoices() ([]*Invoice, error) {
	rows, err := db.Query(`SELECT ` + invoiceCols + ` FROM invoices WHERE status='pending' ORDER BY id LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Invoice
	for rows.Next() {
		i, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
