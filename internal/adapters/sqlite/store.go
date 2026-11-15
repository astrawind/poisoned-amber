package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"net/url"
	"poisoned-amber/internal/domain"
)

type Store struct{ DB *sql.DB }

const schema = `CREATE TABLE IF NOT EXISTS schema_version(version INTEGER PRIMARY KEY); INSERT OR IGNORE INTO schema_version VALUES(1);
CREATE TABLE IF NOT EXISTS contents(version TEXT PRIMARY KEY, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS rooms(id TEXT PRIMARY KEY, code TEXT UNIQUE NOT NULL, body BLOB NOT NULL, deadline INTEGER NOT NULL, expiry INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, adult INTEGER NOT NULL DEFAULT 0, room TEXT NOT NULL DEFAULT '', failures INTEGER NOT NULL DEFAULT 0, blocked_until INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS buttons(id TEXT PRIMARY KEY, user TEXT NOT NULL, room TEXT NOT NULL, phase INTEGER NOT NULL, action TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS outbox(id INTEGER PRIMARY KEY AUTOINCREMENT, chat TEXT NOT NULL, body BLOB NOT NULL, due INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS outbox_chat ON outbox(chat,id);
CREATE TABLE IF NOT EXISTS cards(chat TEXT PRIMARY KEY, message INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value INTEGER NOT NULL);`

func Open(path string) (*Store, error) {
	db, e := sql.Open("sqlite3", "file:"+url.PathEscape(path)+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_txlock=immediate")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(schema); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Import(c *domain.Content) error {
	if e := c.Validate(); e != nil {
		return e
	}
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	var old []byte
	e = s.DB.QueryRow("SELECT body FROM contents WHERE version=?", c.Version).Scan(&old)
	if e == nil {
		if string(old) != string(b) {
			return fmt.Errorf("version already exists with different content")
		}
		return nil
	}
	if e != sql.ErrNoRows {
		return e
	}
	_, e = s.DB.Exec("INSERT INTO contents VALUES(?,?)", c.Version, b)
	return e
}
func Content(tx *sql.Tx, version string) (*domain.Content, error) {
	var b []byte
	if e := tx.QueryRow("SELECT body FROM contents WHERE version=?", version).Scan(&b); e != nil {
		return nil, e
	}
	var c domain.Content
	e := json.Unmarshal(b, &c)
	return &c, e
}
func Room(tx *sql.Tx, id string) (*domain.Room, error) {
	var b []byte
	if e := tx.QueryRow("SELECT body FROM rooms WHERE id=?", id).Scan(&b); e != nil {
		return nil, e
	}
	var r domain.Room
	e := json.Unmarshal(b, &r)
	return &r, e
}
func Save(tx *sql.Tx, r *domain.Room, c *domain.Content) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	expiry := r.LastAction + int64(c.Balance.ExpirySeconds)
	if r.Phase == "ended" || r.Phase == "expired" {
		expiry = 0
	}
	_, e = tx.Exec("INSERT INTO rooms VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body,deadline=excluded.deadline,expiry=excluded.expiry", r.ID, r.Code, b, r.Deadline, expiry)
	return e
}
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}
