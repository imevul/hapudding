package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSQLiteWALAndBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	st, err := Open("sqlite", path, time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var mode string
	if err := st.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=%s", mode)
	}
	var busy int
	if err := st.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy < 5000 {
		t.Fatalf("busy_timeout=%d", busy)
	}
}

func TestSQLiteConcurrentBindLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conc.db")
	st, err := Open("sqlite", path, time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok", "server-a", TokenRow{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := st.BindDevice(ctx, "dev", "server-a", ""); err != nil {
				errCh <- err
				return
			}
			row, err := st.LookupToken(ctx, "tok")
			if err != nil || row == nil {
				errCh <- err
				return
			}
			_ = st.TouchToken(ctx, "tok", "GET", "/Items/x/Images/Primary", 200)
			_ = i
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent store: %v", err)
		}
	}
}

func TestSQLiteBindLookupDeleteAndTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "affinity.db")
	st, err := Open("sqlite", path, time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runStoreContract(t, st)
}

func TestSQLiteTokenTTLExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttl.db")
	st, err := Open("sqlite", path, 20*time.Millisecond, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok", "server-a", TokenRow{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	row, err := st.LookupToken(ctx, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("expected expired token, got %+v", row)
	}
}

func TestDeviceClientStickyAndMigrate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE device_bindings (
		device_id TEXT PRIMARY KEY, backend TEXT NOT NULL, created_at INTEGER NOT NULL, last_seen INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	st, err := Open("sqlite", path, time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.BindDevice(ctx, "dev-1", "server-a", "Infuse"); err != nil {
		t.Fatal(err)
	}
	if err := st.BindDevice(ctx, "dev-1", "server-a", ""); err != nil {
		t.Fatal(err)
	}
	row, err := st.LookupDevice(ctx, "dev-1")
	if err != nil || row == nil || row.Client != "Infuse" {
		t.Fatalf("sticky client %+v err=%v", row, err)
	}
	if err := st.BindToken(ctx, "tok", "server-a", TokenRow{Client: "Infuse"}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindToken(ctx, "tok", "server-a", TokenRow{}); err != nil {
		t.Fatal(err)
	}
	tok, err := st.LookupToken(ctx, "tok")
	if err != nil || tok == nil || tok.Client != "Infuse" {
		t.Fatalf("sticky token client %+v err=%v", tok, err)
	}
}

func TestPostgresStoreContract(t *testing.T) {
	dsn := os.Getenv("HAP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HAP_TEST_DATABASE_URL not set")
	}
	st, err := Open("postgres", dsn, time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_, _ = st.db.ExecContext(ctx, `DELETE FROM token_bindings`)
	_, _ = st.db.ExecContext(ctx, `DELETE FROM device_bindings`)
	_, _ = st.db.ExecContext(ctx, `DELETE FROM anon_bindings`)
	runStoreContract(t, st)
}

func runStoreContract(t *testing.T, st Store) {
	t.Helper()
	ctx := context.Background()
	if err := st.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.BindToken(ctx, "secret-token", "server-a", TokenRow{
		UserID: "user-1", Username: "ada", DeviceID: "dev-1", Client: "CLIamp",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindDevice(ctx, "dev-1", "server-a", "CLIamp"); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAnon(ctx, HashAnon("1.2.3.4", "ua"), "server-a"); err != nil {
		t.Fatal(err)
	}

	tok, err := st.LookupToken(ctx, "secret-token")
	if err != nil || tok == nil || tok.Backend != "server-a" || tok.UserID != "user-1" {
		t.Fatalf("token %+v err=%v", tok, err)
	}
	if tok.TokenHash != HashToken("secret-token") {
		t.Fatal("store must persist hash, not a different key")
	}
	dev, err := st.LookupDevice(ctx, "dev-1")
	if err != nil || dev == nil || dev.Backend != "server-a" || dev.Client != "CLIamp" {
		t.Fatalf("device %+v err=%v", dev, err)
	}
	anon, err := st.LookupAnon(ctx, HashAnon("1.2.3.4", "ua"))
	if err != nil || anon == nil || anon.Backend != "server-a" {
		t.Fatalf("anon %+v err=%v", anon, err)
	}

	if err := st.DeleteToken(ctx, "secret-token"); err != nil {
		t.Fatal(err)
	}
	gone, err := st.LookupToken(ctx, "secret-token")
	if err != nil || gone != nil {
		t.Fatalf("token should be gone: %+v %v", gone, err)
	}
	still, err := st.LookupDevice(ctx, "dev-1")
	if err != nil || still == nil {
		t.Fatal("logout-style token delete must keep DeviceId")
	}

	if err := st.DeleteClient(ctx, "", "dev-1"); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.LookupDevice(ctx, "dev-1"); row != nil {
		t.Fatal("expected device gone after DeleteClient")
	}

	counts, err := st.CountsByBackend(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["server-a"].Anons != 1 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestBackendFlagsOverlay(t *testing.T) {
	st, err := Open("sqlite", filepath.Join(t.TempDir(), "flags.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SetBackendDisabled(ctx, "server-a", true); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListBackendFlags(ctx)
	if err != nil || !got["server-a"] {
		t.Fatalf("%v %v", got, err)
	}
	if err := st.ClearBackendFlag(ctx, "server-a"); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListBackendFlags(ctx)
	if err != nil || got["server-a"] {
		t.Fatalf("cleared %v %v", got, err)
	}
}
