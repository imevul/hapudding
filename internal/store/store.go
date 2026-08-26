package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type BindingKind string

const (
	KindToken  BindingKind = "token"
	KindDevice BindingKind = "device"
	KindAnon   BindingKind = "anon"
)

type TokenRow struct {
	TokenHash  string
	Backend    string
	UserID     string
	Username   string
	DeviceID   string
	Client     string
	Device     string
	Version    string
	SessionID  string
	CreatedAt  time.Time
	LastSeen   time.Time
	LastMethod string
	LastPath   string
	LastStatus int
}

type SimpleBinding struct {
	Key       string
	Backend   string
	Client    string
	CreatedAt time.Time
	LastSeen  time.Time
}

type Counts struct {
	Tokens  int
	Devices int
	Anons   int
}

type Store interface {
	LookupToken(ctx context.Context, token string) (*TokenRow, error)
	LookupDevice(ctx context.Context, deviceID string) (*SimpleBinding, error)
	LookupAnon(ctx context.Context, anonKey string) (*SimpleBinding, error)
	BindToken(ctx context.Context, token, backend string, meta TokenRow) error
	BindDevice(ctx context.Context, deviceID, backend, client string) error
	BindAnon(ctx context.Context, anonKey, backend string) error
	TouchToken(ctx context.Context, token, method, path string, status int) error
	TouchDevice(ctx context.Context, deviceID string) error
	TouchAnon(ctx context.Context, anonKey string) error
	DeleteToken(ctx context.Context, token string) error
	DeleteDevice(ctx context.Context, deviceID string) error
	DeleteClient(ctx context.Context, token, deviceID string) error
	DeletePinsByUsername(ctx context.Context, username string, extraUserIDs ...string) (tokens, devices int, err error)
	ListTokens(ctx context.Context) ([]TokenRow, error)
	CountsByBackend(ctx context.Context) (map[string]Counts, error)
	ListBackendFlags(ctx context.Context) (map[string]bool, error)
	SetBackendDisabled(ctx context.Context, name string, disabled bool) error
	ClearBackendFlag(ctx context.Context, name string) error
	Ping(ctx context.Context) error
	Close() error
}

type SQLStore struct {
	db          *sql.DB
	placeholder func(n int) string
	tokenTTL    time.Duration
	deviceTTL   time.Duration
	anonTTL     time.Duration
}

func Open(driver, dsn string, tokenTTL, deviceTTL, anonTTL time.Duration) (*SQLStore, error) {
	var (
		goDriver string
		ph       func(int) string
	)
	switch driver {
	case "sqlite":
		goDriver = "sqlite"
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil && filepath.Dir(dsn) != "." && filepath.Dir(dsn) != "" {
			return nil, err
		}
		ph = func(int) string { return "?" }
	case "postgres":
		goDriver = "pgx"
		ph = func(n int) string { return fmt.Sprintf("$%d", n) }
	default:
		return nil, fmt.Errorf("unknown store driver %q", driver)
	}
	if driver == "sqlite" {
		dsn = sqliteDSN(dsn)
	}
	db, err := sql.Open(goDriver, dsn)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		if err := configureSQLite(db); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	s := &SQLStore{db: db, placeholder: ph, tokenTTL: tokenTTL, deviceTTL: deviceTTL, anonTTL: anonTTL}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		if goDriver == "sqlite" {
			return nil, fmt.Errorf("open sqlite %s: %w (path must be writable; Docker: mount /data and chown uid 65532)", dsn, err)
		}
		return nil, err
	}
	return s, nil
}

func (s *SQLStore) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS token_bindings (
			token_hash TEXT PRIMARY KEY,
			backend TEXT NOT NULL,
			user_id TEXT,
			username TEXT,
			device_id TEXT,
			client TEXT,
			device TEXT,
			version TEXT,
			session_id TEXT,
			created_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL,
			last_method TEXT,
			last_path TEXT,
			last_status INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS device_bindings (
			device_id TEXT PRIMARY KEY,
			backend TEXT NOT NULL,
			client TEXT,
			created_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS anon_bindings (
			anon_hash TEXT PRIMARY KEY,
			backend TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS backend_flags (
			name TEXT PRIMARY KEY,
			disabled INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return s.ensureDeviceClientColumn(ctx)
}

func (s *SQLStore) ensureDeviceClientColumn(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `ALTER TABLE device_bindings ADD COLUMN client TEXT`)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func HashAnon(ip, ua string) string {
	sum := sha256.Sum256([]byte(ip + "\x00" + ua))
	return hex.EncodeToString(sum[:])
}

// HashSessionIP is IP-only glue for header-less media (images, streams).
// Distinct from HashAnon(ip, ua). Written on login and token-bearing requests.
func HashSessionIP(ip string) string {
	return HashAnon(ip, "")
}

func sqliteDSN(dsn string) string {
	if strings.Contains(dsn, "_pragma=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

func configureSQLite(db *sql.DB) error {
	// One connection: Go's pool plus SQLite writers otherwise yield SQLITE_BUSY
	// under the post-login image storm (every request looks up + bind/touch).
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	for _, q := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
	} {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("sqlite %s: %w", q, err)
		}
	}
	return nil
}

func (s *SQLStore) LookupToken(ctx context.Context, token string) (*TokenRow, error) {
	if token == "" {
		return nil, nil
	}
	h := HashToken(token)
	row, err := s.scanToken(ctx, `SELECT token_hash, backend, user_id, username, device_id, client, device, version, session_id, created_at, last_seen, last_method, last_path, last_status FROM token_bindings WHERE token_hash = `+s.placeholder(1), h)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	if s.expired(row.LastSeen, s.tokenTTL) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM token_bindings WHERE token_hash = `+s.placeholder(1), h)
		return nil, nil
	}
	return row, nil
}

func (s *SQLStore) scanToken(ctx context.Context, q string, args ...any) (*TokenRow, error) {
	var (
		r          TokenRow
		userID     sql.NullString
		username   sql.NullString
		deviceID   sql.NullString
		client     sql.NullString
		device     sql.NullString
		version    sql.NullString
		sessionID  sql.NullString
		created    int64
		seen       int64
		lastMethod sql.NullString
		lastPath   sql.NullString
		lastStatus sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, q, args...).Scan(
		&r.TokenHash, &r.Backend, &userID, &username, &deviceID, &client, &device, &version, &sessionID,
		&created, &seen, &lastMethod, &lastPath, &lastStatus,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.UserID = userID.String
	r.Username = username.String
	r.DeviceID = deviceID.String
	r.Client = client.String
	r.Device = device.String
	r.Version = version.String
	r.SessionID = sessionID.String
	r.CreatedAt = time.Unix(created, 0)
	r.LastSeen = time.Unix(seen, 0)
	r.LastMethod = lastMethod.String
	r.LastPath = lastPath.String
	r.LastStatus = int(lastStatus.Int64)
	return &r, nil
}

func (s *SQLStore) LookupDevice(ctx context.Context, deviceID string) (*SimpleBinding, error) {
	if deviceID == "" {
		return nil, nil
	}
	var backend string
	var client sql.NullString
	var created, seen int64
	q := `SELECT backend, client, created_at, last_seen FROM device_bindings WHERE device_id = ` + s.placeholder(1)
	err := s.db.QueryRowContext(ctx, q, deviceID).Scan(&backend, &client, &created, &seen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	last := time.Unix(seen, 0)
	if s.expired(last, s.deviceTTL) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM device_bindings WHERE device_id = `+s.placeholder(1), deviceID)
		return nil, nil
	}
	return &SimpleBinding{Key: deviceID, Backend: backend, Client: client.String, CreatedAt: time.Unix(created, 0), LastSeen: last}, nil
}

func (s *SQLStore) LookupAnon(ctx context.Context, anonKey string) (*SimpleBinding, error) {
	return s.lookupSimple(ctx, "anon_bindings", "anon_hash", anonKey, s.anonTTL)
}

func (s *SQLStore) lookupSimple(ctx context.Context, table, col, key string, ttl time.Duration) (*SimpleBinding, error) {
	if key == "" {
		return nil, nil
	}
	var backend string
	var created, seen int64
	q := fmt.Sprintf(`SELECT backend, created_at, last_seen FROM %s WHERE %s = %s`, table, col, s.placeholder(1))
	err := s.db.QueryRowContext(ctx, q, key).Scan(&backend, &created, &seen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	last := time.Unix(seen, 0)
	if s.expired(last, ttl) {
		_, _ = s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = %s`, table, col, s.placeholder(1)), key)
		return nil, nil
	}
	return &SimpleBinding{Key: key, Backend: backend, CreatedAt: time.Unix(created, 0), LastSeen: last}, nil
}

func (s *SQLStore) BindToken(ctx context.Context, token, backend string, meta TokenRow) error {
	if token == "" || backend == "" {
		return nil
	}
	h := HashToken(token)
	now := time.Now().Unix()
	if meta.SessionID == "" {
		meta.SessionID = h[:16]
	}
	q := `INSERT INTO token_bindings (token_hash, backend, user_id, username, device_id, client, device, version, session_id, created_at, last_seen, last_method, last_path, last_status)
		VALUES (` + joinPH(s, 14) + `)
		ON CONFLICT (token_hash) DO UPDATE SET backend=excluded.backend, last_seen=excluded.last_seen,
		user_id=CASE WHEN excluded.user_id IS NULL OR excluded.user_id = '' THEN token_bindings.user_id ELSE excluded.user_id END,
		username=CASE WHEN excluded.username IS NULL OR excluded.username = '' THEN token_bindings.username ELSE excluded.username END,
		device_id=CASE WHEN excluded.device_id IS NULL OR excluded.device_id = '' THEN token_bindings.device_id ELSE excluded.device_id END,
		device=CASE WHEN excluded.device IS NULL OR excluded.device = '' THEN token_bindings.device ELSE excluded.device END,
		version=CASE WHEN excluded.version IS NULL OR excluded.version = '' THEN token_bindings.version ELSE excluded.version END,
		client=CASE WHEN excluded.client IS NULL OR excluded.client = '' THEN token_bindings.client ELSE excluded.client END`
	_, err := s.db.ExecContext(ctx, q, h, backend, nullStr(meta.UserID), nullStr(meta.Username), nullStr(meta.DeviceID),
		nullStr(meta.Client), nullStr(meta.Device), nullStr(meta.Version), meta.SessionID, now, now, nullStr(meta.LastMethod), nullStr(meta.LastPath), meta.LastStatus)
	return err
}

func (s *SQLStore) BindDevice(ctx context.Context, deviceID, backend, client string) error {
	if deviceID == "" || backend == "" {
		return nil
	}
	now := time.Now().Unix()
	q := `INSERT INTO device_bindings (device_id, backend, client, created_at, last_seen) VALUES (` + joinPH(s, 5) + `)
		ON CONFLICT (device_id) DO UPDATE SET backend=excluded.backend, last_seen=excluded.last_seen,
		client=CASE WHEN excluded.client IS NULL OR excluded.client = '' THEN device_bindings.client ELSE excluded.client END`
	_, err := s.db.ExecContext(ctx, q, deviceID, backend, nullStr(client), now, now)
	return err
}

func (s *SQLStore) BindAnon(ctx context.Context, anonKey, backend string) error {
	return s.upsertSimple(ctx, "anon_bindings", "anon_hash", anonKey, backend)
}

func (s *SQLStore) upsertSimple(ctx context.Context, table, col, key, backend string) error {
	if key == "" || backend == "" {
		return nil
	}
	now := time.Now().Unix()
	q := fmt.Sprintf(`INSERT INTO %s (%s, backend, created_at, last_seen) VALUES (%s, %s, %s, %s)
		ON CONFLICT (%s) DO UPDATE SET backend=excluded.backend, last_seen=excluded.last_seen`,
		table, col, s.placeholder(1), s.placeholder(2), s.placeholder(3), s.placeholder(4), col)
	_, err := s.db.ExecContext(ctx, q, key, backend, now, now)
	return err
}

func (s *SQLStore) TouchToken(ctx context.Context, token, method, path string, status int) error {
	if token == "" {
		return nil
	}
	h := HashToken(token)
	_, err := s.db.ExecContext(ctx, `UPDATE token_bindings SET last_seen=`+s.placeholder(1)+`, last_method=`+s.placeholder(2)+`, last_path=`+s.placeholder(3)+`, last_status=`+s.placeholder(4)+` WHERE token_hash=`+s.placeholder(5),
		time.Now().Unix(), method, path, status, h)
	return err
}

func (s *SQLStore) TouchDevice(ctx context.Context, deviceID string) error {
	if deviceID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE device_bindings SET last_seen=`+s.placeholder(1)+` WHERE device_id=`+s.placeholder(2), time.Now().Unix(), deviceID)
	return err
}

func (s *SQLStore) TouchAnon(ctx context.Context, anonKey string) error {
	if anonKey == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE anon_bindings SET last_seen=`+s.placeholder(1)+` WHERE anon_hash=`+s.placeholder(2), time.Now().Unix(), anonKey)
	return err
}

func (s *SQLStore) DeleteToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM token_bindings WHERE token_hash=`+s.placeholder(1), HashToken(token))
	return err
}

func (s *SQLStore) DeleteDevice(ctx context.Context, deviceID string) error {
	if deviceID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM device_bindings WHERE device_id=`+s.placeholder(1), deviceID)
	return err
}

func (s *SQLStore) DeleteClient(ctx context.Context, token, deviceID string) error {
	if err := s.DeleteToken(ctx, token); err != nil {
		return err
	}
	return s.DeleteDevice(ctx, deviceID)
}

func (s *SQLStore) DeletePinsByUsername(ctx context.Context, username string, extraUserIDs ...string) (tokens, devices int, err error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	where, args := pinWhere(s, username, extraUserIDs)
	rows, err := tx.QueryContext(ctx, `SELECT backend FROM token_bindings WHERE `+where, args...)
	if err != nil {
		return 0, 0, err
	}
	backends := map[string]struct{}{}
	for rows.Next() {
		var b sql.NullString
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return 0, 0, err
		}
		if b.String != "" {
			backends[b.String] = struct{}{}
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()
	if len(backends) == 0 {
		where = `COALESCE(username,'')='' AND COALESCE(user_id,'')=''`
		args = nil
		rows, err = tx.QueryContext(ctx, `SELECT backend FROM token_bindings WHERE `+where)
		if err != nil {
			return 0, 0, err
		}
		for rows.Next() {
			var b sql.NullString
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return 0, 0, err
			}
			if b.String != "" {
				backends[b.String] = struct{}{}
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return 0, 0, err
		}
		rows.Close()
	}
	if len(backends) == 0 {
		err = tx.Commit()
		return 0, 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM token_bindings WHERE `+where, args...)
	if err != nil {
		return 0, 0, err
	}
	if n, e := res.RowsAffected(); e == nil {
		tokens = int(n)
	}
	for backend := range backends {
		res, err = tx.ExecContext(ctx, `DELETE FROM device_bindings WHERE backend=`+s.placeholder(1), backend)
		if err != nil {
			return 0, 0, err
		}
		if n, e := res.RowsAffected(); e == nil {
			devices += int(n)
		}
	}
	err = tx.Commit()
	return tokens, devices, err
}

func compactIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func pinWhere(s *SQLStore, username string, extraUserIDs []string) (string, []any) {
	where := `LOWER(username)=LOWER(` + s.placeholder(1) + `)`
	args := []any{username}
	ids := compactIDs(extraUserIDs)
	if len(ids) == 0 {
		return where, args
	}
	ph := make([]string, len(ids))
	for i, id := range ids {
		ph[i] = s.placeholder(i + 2)
		args = append(args, id)
	}
	return where + ` OR user_id IN (` + strings.Join(ph, ",") + `)`, args
}

func (s *SQLStore) ListTokens(ctx context.Context) ([]TokenRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, backend, user_id, username, device_id, client, device, version, session_id, created_at, last_seen, last_method, last_path, last_status FROM token_bindings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenRow
	for rows.Next() {
		var (
			r          TokenRow
			userID     sql.NullString
			username   sql.NullString
			deviceID   sql.NullString
			client     sql.NullString
			device     sql.NullString
			version    sql.NullString
			sessionID  sql.NullString
			created    int64
			seen       int64
			lastMethod sql.NullString
			lastPath   sql.NullString
			lastStatus sql.NullInt64
		)
		if err := rows.Scan(&r.TokenHash, &r.Backend, &userID, &username, &deviceID, &client, &device, &version, &sessionID, &created, &seen, &lastMethod, &lastPath, &lastStatus); err != nil {
			return nil, err
		}
		r.UserID = userID.String
		r.Username = username.String
		r.DeviceID = deviceID.String
		r.Client = client.String
		r.Device = device.String
		r.Version = version.String
		r.SessionID = sessionID.String
		r.CreatedAt = time.Unix(created, 0)
		r.LastSeen = time.Unix(seen, 0)
		r.LastMethod = lastMethod.String
		r.LastPath = lastPath.String
		r.LastStatus = int(lastStatus.Int64)
		if !s.expired(r.LastSeen, s.tokenTTL) {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func (s *SQLStore) CountsByBackend(ctx context.Context) (map[string]Counts, error) {
	out := map[string]Counts{}
	add := func(q string, set func(*Counts, int)) error {
		rows, err := s.db.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var n int
			if err := rows.Scan(&name, &n); err != nil {
				return err
			}
			c := out[name]
			set(&c, n)
			out[name] = c
		}
		return rows.Err()
	}
	if err := add(`SELECT backend, COUNT(*) FROM token_bindings GROUP BY backend`, func(c *Counts, n int) { c.Tokens = n }); err != nil {
		return nil, err
	}
	if err := add(`SELECT backend, COUNT(*) FROM device_bindings GROUP BY backend`, func(c *Counts, n int) { c.Devices = n }); err != nil {
		return nil, err
	}
	if err := add(`SELECT backend, COUNT(*) FROM anon_bindings GROUP BY backend`, func(c *Counts, n int) { c.Anons = n }); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SQLStore) ListBackendFlags(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, disabled FROM backend_flags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		var disabled int
		if err := rows.Scan(&name, &disabled); err != nil {
			return nil, err
		}
		out[name] = disabled != 0
	}
	return out, rows.Err()
}

func (s *SQLStore) SetBackendDisabled(ctx context.Context, name string, disabled bool) error {
	if name == "" {
		return nil
	}
	flag := 0
	if disabled {
		flag = 1
	}
	q := `INSERT INTO backend_flags (name, disabled, updated_at) VALUES (` + joinPH(s, 3) + `)
		ON CONFLICT (name) DO UPDATE SET disabled=excluded.disabled, updated_at=excluded.updated_at`
	_, err := s.db.ExecContext(ctx, q, name, flag, time.Now().Unix())
	return err
}

func (s *SQLStore) ClearBackendFlag(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM backend_flags WHERE name=`+s.placeholder(1), name)
	return err
}

func (s *SQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *SQLStore) Close() error {
	return s.db.Close()
}

func (s *SQLStore) expired(last time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	return time.Since(last) > ttl
}

func joinPH(s *SQLStore, n int) string {
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = s.placeholder(i + 1)
	}
	return strings.Join(parts, ", ")
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
