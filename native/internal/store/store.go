// Package store owns the SQLite database and exposes typed methods for
// zones, records, users, api keys, and audit events.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// ---- Types ---------------------------------------------------------------

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	Role         Role       `json:"role"`
	Disabled     bool       `json:"disabled"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

type APIKey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	TokenHash   string     `json:"-"`
	Scopes      []string   `json:"scopes"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Token       string     `json:"token,omitempty"` // only set at creation
}

type Zone struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	IsPrivate   bool      `json:"is_private"`
	Serial      uint32    `json:"serial"`
	CreatedAt   time.Time `json:"created_at"`
}

type Record struct {
	ID       int64  `json:"id"`
	Zone     string `json:"zone"`
	Name     string `json:"name"` // fully-qualified, no trailing dot
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

type AuditEvent struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"ts"`
	ActorUser  string    `json:"actor_user,omitempty"`
	ActorKey   string    `json:"actor_key,omitempty"`
	ActorIP    string    `json:"actor_ip,omitempty"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type,omitempty"`
	TargetID   string    `json:"target_id,omitempty"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
}

// ---- Store ---------------------------------------------------------------

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite is happier with a single writer; the driver serializes writes
	// under the busy_timeout above, but we still limit connections.
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_login_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			token_prefix TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT '',
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME,
			expires_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys(token_prefix)`,

		`CREATE TABLE IF NOT EXISTS zones (
			name TEXT PRIMARY KEY COLLATE NOCASE,
			description TEXT NOT NULL DEFAULT '',
			is_private INTEGER NOT NULL DEFAULT 1,
			serial INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			zone TEXT NOT NULL REFERENCES zones(name) ON DELETE CASCADE,
			name TEXT NOT NULL COLLATE NOCASE,
			type TEXT NOT NULL,
			ttl INTEGER NOT NULL DEFAULT 3600,
			content TEXT NOT NULL,
			disabled INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_records_zone ON records(zone)`,
		`CREATE INDEX IF NOT EXISTS idx_records_name_type ON records(name, type)`,

		`CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			actor_user TEXT,
			actor_key TEXT,
			actor_ip TEXT,
			action TEXT NOT NULL,
			target_type TEXT,
			target_id TEXT,
			outcome TEXT NOT NULL,
			detail TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts DESC)`,

		`CREATE TABLE IF NOT EXISTS revoked_tokens (
			jti TEXT PRIMARY KEY,
			revoked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w\nsql: %s", err, stmt)
		}
	}
	return nil
}

// ==== Users ==============================================================

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, role) VALUES (?,?,?,?)`,
		u.ID, strings.ToLower(u.Email), u.PasswordHash, u.Role)
	return err
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, role, disabled, created_at, last_login_at
		 FROM users WHERE email = ?`, strings.ToLower(email)).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, role, disabled, created_at, last_login_at
		 FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, email, password_hash, role, disabled, created_at, last_login_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.Disabled, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&n)
	return n, err
}

func (s *Store) TouchLogin(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// ==== API keys ===========================================================

func (s *Store) CreateAPIKey(ctx context.Context, k *APIKey) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, user_id, name, token_hash, token_prefix, scopes)
		 VALUES (?,?,?,?,?,?)`,
		k.ID, k.UserID, k.Name, k.TokenHash, k.TokenPrefix, strings.Join(k.Scopes, ","))
	return err
}

func (s *Store) LookupAPIKey(ctx context.Context, hash string) (*APIKey, *User, error) {
	var k APIKey
	var u User
	var scopes string
	err := s.db.QueryRowContext(ctx, `
		SELECT k.id, k.user_id, k.name, k.token_prefix, k.scopes, k.disabled, k.created_at, k.last_used_at, k.expires_at,
		       u.id, u.email, u.role, u.disabled, u.created_at
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.token_hash = ?`, hash).Scan(
		&k.ID, &k.UserID, &k.Name, &k.TokenPrefix, &scopes, &k.Disabled, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt,
		&u.ID, &u.Email, &u.Role, &u.Disabled, &u.CreatedAt)
	if err != nil {
		return nil, nil, err
	}
	if scopes != "" {
		k.Scopes = strings.Split(scopes, ",")
	}
	return &k, &u, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, name, token_prefix, scopes, disabled, created_at, last_used_at, expires_at
		FROM api_keys WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var scopes string
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenPrefix, &scopes, &k.Disabled, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt); err != nil {
			return nil, err
		}
		if scopes != "" {
			k.Scopes = strings.Split(scopes, ",")
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIKey(ctx context.Context, userID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) TouchAPIKey(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
}

// ==== Zones ==============================================================

func (s *Store) CreateZone(ctx context.Context, z *Zone) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO zones (name, description, is_private) VALUES (?,?,?)`,
		strings.ToLower(z.Name), z.Description, boolInt(z.IsPrivate))
	return err
}

func (s *Store) GetZone(ctx context.Context, name string) (*Zone, error) {
	var z Zone
	var isPriv int
	err := s.db.QueryRowContext(ctx,
		`SELECT name, description, is_private, serial, created_at FROM zones WHERE name = ?`,
		strings.ToLower(name)).Scan(&z.Name, &z.Description, &isPriv, &z.Serial, &z.CreatedAt)
	if err != nil {
		return nil, err
	}
	z.IsPrivate = isPriv != 0
	return &z, nil
}

func (s *Store) ListZones(ctx context.Context) ([]Zone, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, description, is_private, serial, created_at FROM zones ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Zone
	for rows.Next() {
		var z Zone
		var isPriv int
		if err := rows.Scan(&z.Name, &z.Description, &isPriv, &z.Serial, &z.CreatedAt); err != nil {
			return nil, err
		}
		z.IsPrivate = isPriv != 0
		out = append(out, z)
	}
	return out, rows.Err()
}

func (s *Store) DeleteZone(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM zones WHERE name = ?`, strings.ToLower(name))
	return err
}

// FindZoneForName returns the longest-matching zone name for a query name,
// or "" if none.
func (s *Store) FindZoneForName(ctx context.Context, qname string) (string, error) {
	qname = strings.TrimSuffix(strings.ToLower(qname), ".")
	if qname == "" {
		return "", nil
	}
	// Build candidates: qname, then chop labels one at a time.
	labels := strings.Split(qname, ".")
	for i := 0; i < len(labels); i++ {
		cand := strings.Join(labels[i:], ".")
		var got string
		err := s.db.QueryRowContext(ctx, `SELECT name FROM zones WHERE name = ?`, cand).Scan(&got)
		if err == nil {
			return got, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return "", nil
}

// BumpSerial increments the SOA-style serial for the zone.
func (s *Store) BumpSerial(ctx context.Context, zone string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE zones SET serial = serial + 1 WHERE name = ?`, strings.ToLower(zone))
	return err
}

// ==== Records ============================================================

// UpsertRecordSet replaces all records for a (zone, name, type) tuple with
// the given contents. TTL applies to all rows in the set.
func (s *Store) UpsertRecordSet(ctx context.Context, zone, name, typ string, ttl int, contents []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM records WHERE zone = ? AND name = ? AND type = ?`,
		strings.ToLower(zone), strings.ToLower(name), typ); err != nil {
		return err
	}
	for _, c := range contents {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO records (zone, name, type, ttl, content) VALUES (?,?,?,?,?)`,
			strings.ToLower(zone), strings.ToLower(name), typ, ttl, c); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE zones SET serial = serial + 1 WHERE name = ?`, strings.ToLower(zone)); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordSetInput is one normalized (name,type,ttl,contents) upsert. The
// batch upsert takes a slice of these; each replaces its (zone,name,type)
// tuple atomically alongside the others.
type RecordSetInput struct {
	Name     string
	Type     string
	TTL      int
	Contents []string
}

// UpsertRecordSetsBatch performs REPLACE semantics for every (name,type)
// pair in one SQL transaction. Any error rolls back the entire batch —
// callers see either "all persisted" or "none persisted".
func (s *Store) UpsertRecordSetsBatch(ctx context.Context, zone string, sets []RecordSetInput) error {
	if len(sets) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	zoneLC := strings.ToLower(zone)
	for _, rs := range sets {
		nameLC := strings.ToLower(rs.Name)
		typUC := strings.ToUpper(rs.Type)
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM records WHERE zone = ? AND name = ? AND type = ?`,
			zoneLC, nameLC, typUC); err != nil {
			return fmt.Errorf("batch delete %s %s: %w", rs.Name, rs.Type, err)
		}
		for _, c := range rs.Contents {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO records (zone, name, type, ttl, content) VALUES (?,?,?,?,?)`,
				zoneLC, nameLC, typUC, rs.TTL, c); err != nil {
				return fmt.Errorf("batch insert %s %s: %w", rs.Name, rs.Type, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE zones SET serial = serial + 1 WHERE name = ?`, zoneLC); err != nil {
		return fmt.Errorf("batch bump serial: %w", err)
	}
	return tx.Commit()
}

func (s *Store) DeleteRecordSet(ctx context.Context, zone, name, typ string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM records WHERE zone = ? AND name = ? AND type = ?`,
		strings.ToLower(zone), strings.ToLower(name), typ)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		_, _ = s.db.ExecContext(ctx, `UPDATE zones SET serial = serial + 1 WHERE name = ?`, strings.ToLower(zone))
	}
	return n > 0, nil
}

// LookupRecords returns records matching (name, type). If type is "" then all types.
func (s *Store) LookupRecords(ctx context.Context, name, typ string) ([]Record, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	var (
		rows *sql.Rows
		err  error
	)
	if typ == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, zone, name, type, ttl, content, disabled FROM records WHERE name = ? AND disabled = 0`,
			name)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, zone, name, type, ttl, content, disabled FROM records WHERE name = ? AND type = ? AND disabled = 0`,
			name, strings.ToUpper(typ))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var dis int
		if err := rows.Scan(&r.ID, &r.Zone, &r.Name, &r.Type, &r.TTL, &r.Content, &dis); err != nil {
			return nil, err
		}
		r.Disabled = dis != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRecordsInZone lists everything under a zone, ordered by name+type.
func (s *Store) ListRecordsInZone(ctx context.Context, zone string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, zone, name, type, ttl, content, disabled FROM records WHERE zone = ? ORDER BY name, type`,
		strings.ToLower(zone))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var dis int
		if err := rows.Scan(&r.ID, &r.Zone, &r.Name, &r.Type, &r.TTL, &r.Content, &dis); err != nil {
			return nil, err
		}
		r.Disabled = dis != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ==== Audit ==============================================================

func (s *Store) WriteAudit(ctx context.Context, e AuditEvent) {
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO audit_log (actor_user, actor_key, actor_ip, action, target_type, target_id, outcome, detail)
		VALUES (?,?,?,?,?,?,?,?)`,
		nullIfEmpty(e.ActorUser), nullIfEmpty(e.ActorKey), nullIfEmpty(e.ActorIP),
		e.Action, nullIfEmpty(e.TargetType), nullIfEmpty(e.TargetID), e.Outcome, nullIfEmpty(e.Detail))
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, actor_user, actor_key, actor_ip, action, target_type, target_id, outcome, detail
		 FROM audit_log ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var au, ak, ip, tt, ti, det sql.NullString
		if err := rows.Scan(&e.ID, &e.Timestamp, &au, &ak, &ip, &e.Action, &tt, &ti, &e.Outcome, &det); err != nil {
			return nil, err
		}
		e.ActorUser = au.String
		e.ActorKey = ak.String
		e.ActorIP = ip.String
		e.TargetType = tt.String
		e.TargetID = ti.String
		e.Detail = det.String
		out = append(out, e)
	}
	return out, rows.Err()
}

// ==== Token revocation ===================================================

func (s *Store) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) {
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO revoked_tokens (jti, expires_at) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		jti, expiresAt)
}

func (s *Store) IsRevoked(ctx context.Context, jti string) bool {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM revoked_tokens WHERE jti = ?`, jti).Scan(&one)
	return err == nil
}

// ==== helpers ============================================================

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
