package main

// SQLite storage (pure Go driver, no cgo). The base schema is identical to
// tomato-store 1.0.x so an existing /var/lib/tomato-store/store.db keeps
// working after the update; newer columns are added by migrate().

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct{ *sql.DB }

const baseSchema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  balance INTEGER NOT NULL DEFAULT 0 CHECK (balance >= 0),
  role TEXT NOT NULL DEFAULT 'member',
  banned INTEGER NOT NULL DEFAULT 0,
  blocked INTEGER NOT NULL DEFAULT 0,
  notify INTEGER NOT NULL DEFAULT 1,
  total_topup INTEGER NOT NULL DEFAULT 0,
  total_spent INTEGER NOT NULL DEFAULT 0,
  total_create INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS texts (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS servers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  location TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  api_key TEXT NOT NULL DEFAULT '',
  local INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  protos TEXT NOT NULL DEFAULT 'ssh,vmess,vless,trojan,zivpn',
  quota_gb INTEGER NOT NULL DEFAULT 0,
  ip_limit INTEGER NOT NULL DEFAULT 2,
  max_accounts INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'unknown',
  last_check INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS prices (
  server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
  proto TEXT NOT NULL,
  price INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (server_id, proto)
);
CREATE TABLE IF NOT EXISTS accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  server_id INTEGER NOT NULL,
  proto TEXT NOT NULL,
  username TEXT NOT NULL,
  password TEXT NOT NULL DEFAULT '',
  trial INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  expires_at INTEGER NOT NULL,
  price_paid INTEGER NOT NULL DEFAULT 0,
  notified_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS accounts_user ON accounts(user_id, status);
CREATE INDEX IF NOT EXISTS accounts_exp ON accounts(status, expires_at);
CREATE INDEX IF NOT EXISTS accounts_srv ON accounts(server_id, proto, username);
CREATE TABLE IF NOT EXISTS orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  server_id INTEGER NOT NULL,
  proto TEXT NOT NULL,
  username TEXT NOT NULL,
  password TEXT NOT NULL DEFAULT '',
  days INTEGER NOT NULL,
  amount INTEGER NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  prev_expires INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  done_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS orders_status ON orders(status, created_at);
CREATE INDEX IF NOT EXISTS orders_user ON orders(user_id, created_at);
CREATE TABLE IF NOT EXISTS transactions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  type TEXT NOT NULL,
  amount INTEGER NOT NULL,
  balance_after INTEGER NOT NULL,
  ref TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tx_user ON transactions(user_id, created_at);
CREATE INDEX IF NOT EXISTS tx_type ON transactions(type, created_at);
CREATE TABLE IF NOT EXISTS invoices (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  amount INTEGER NOT NULL,
  trx_id TEXT NOT NULL UNIQUE,
  order_ref TEXT NOT NULL DEFAULT '',
  checkout_url TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  chat_id INTEGER NOT NULL DEFAULT 0,
  msg_id INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  paid_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS invoices_status ON invoices(status);
CREATE TABLE IF NOT EXISTS trial_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  server_id INTEGER NOT NULL,
  proto TEXT NOT NULL,
  username TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS trial_user ON trial_logs(user_id, created_at);
CREATE TABLE IF NOT EXISTS broadcasts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  text TEXT NOT NULL DEFAULT '',
  photo TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  cursor INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  sent INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0,
  created_by INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS audit_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor INTEGER NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS error_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,
  message TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS health_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id INTEGER NOT NULL,
  ok INTEGER NOT NULL,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS health_srv ON health_logs(server_id, created_at);
`

// Columns added after 1.0.x. Applied only when missing, so the migration is
// safe to run on every start and on databases restored from old backups.
var addColumns = []struct{ table, column, def string }{
	{"invoices", "gateway", "TEXT NOT NULL DEFAULT 'autogopay'"},
	{"invoices", "method", "TEXT NOT NULL DEFAULT 'qris'"},
	{"invoices", "fee", "INTEGER NOT NULL DEFAULT 0"},
	{"invoices", "total", "INTEGER NOT NULL DEFAULT 0"},
	{"invoices", "pay_code", "TEXT NOT NULL DEFAULT ''"},
	{"invoices", "cashback", "INTEGER NOT NULL DEFAULT 0"},
	{"invoices", "checked_at", "INTEGER NOT NULL DEFAULT 0"},
	{"servers", "latency_ms", "INTEGER NOT NULL DEFAULT 0"},
	{"servers", "down_since", "INTEGER NOT NULL DEFAULT 0"},
	{"accounts", "uuid", "TEXT NOT NULL DEFAULT ''"},
	{"broadcasts", "blocked", "INTEGER NOT NULL DEFAULT 0"},
	{"broadcasts", "target", "TEXT NOT NULL DEFAULT 'all'"},
	{"broadcasts", "chat_id", "INTEGER NOT NULL DEFAULT 0"},
	{"broadcasts", "msg_id", "INTEGER NOT NULL DEFAULT 0"},
}

func openDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(4)
	sdb.SetConnMaxIdleTime(5 * time.Minute)
	db := &DB{sdb}
	if err := db.migrate(); err != nil {
		sdb.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return db, nil
}

func (db *DB) migrate() error {
	if _, err := db.Exec(baseSchema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	for _, c := range addColumns {
		if db.hasColumn(c.table, c.column) {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.column, c.def)); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", c.table, c.column, err)
		}
	}
	// Old invoices (AutoGoPay only) had no total; total = amount.
	_, _ = db.Exec(`UPDATE invoices SET total = amount WHERE total = 0`)
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS invoices_ref ON invoices(order_ref)`)
	return err
}

func (db *DB) hasColumn(table, column string) bool {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && strings.EqualFold(n, column) {
			return true
		}
	}
	return false
}

// tx runs fn in a write transaction (BEGIN IMMEDIATE).
func (db *DB) tx(fn func(*sql.Tx) error) error {
	t, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

func (db *DB) int(q string, args ...any) int64 {
	var n sql.NullInt64
	_ = db.QueryRow(q, args...).Scan(&n)
	return n.Int64
}

func now() int64 { return time.Now().Unix() }

func (db *DB) logError(scope string, err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, _ = db.Exec(`INSERT INTO error_logs(scope, message, created_at) VALUES(?,?,?)`, scope, msg, now())
}

func (db *DB) audit(actor int64, action, target, detail string) {
	_, _ = db.Exec(`INSERT INTO audit_logs(actor, action, target, detail, created_at) VALUES(?,?,?,?,?)`,
		actor, action, target, detail, now())
}

// backupTo writes a consistent copy of the database (VACUUM INTO).
func (db *DB) backupTo(path string) error {
	_ = os.Remove(path)
	_, err := db.Exec(`VACUUM INTO ?`, path)
	if err == nil {
		_ = os.Chmod(path, 0o600)
	}
	return err
}

type sqlTx = sql.Tx

func monthStart() int64 {
	t := time.Now()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).Unix()
}

func dayStart() int64 {
	t := time.Now()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Unix()
}

func timeFmt(ts int64, layout string) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format(layout)
}
