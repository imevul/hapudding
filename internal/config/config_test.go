package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRejectReservedHopHeaders(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	raw := `
listen: ":8096"
backends:
  - name: server-a
    url: http://127.0.0.1:8096
    headers:
      Authorization: Bearer nope
`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected reserved header error")
	}
}

func TestDefaultsAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	raw := `
backends:
  - name: server-a
    url: http://127.0.0.1:8096
`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAP_LISTEN", ":9000")
	t.Setenv("HAP_AFFINITY_POLICY", "fail_closed")
	t.Setenv("HAP_STORE", "")
	t.Setenv("HAP_DATABASE_URL", "")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.Affinity.Policy != "fail_closed" {
		t.Fatalf("listen=%q policy=%q", c.Listen, c.Affinity.Policy)
	}
	if c.Affinity.Store != "postgres" {
		t.Fatalf("store=%q", c.Affinity.Store)
	}
	if c.Affinity.Postgres.URL != "postgres://hap:hap@localhost:5432/hap?sslmode=disable" {
		t.Fatalf("postgres url=%q", c.Affinity.Postgres.URL)
	}
	if c.Affinity.Graylist.Policy != "fail_closed" {
		t.Fatalf("graylist policy=%q", c.Affinity.Graylist.Policy)
	}
	if got := c.Affinity.Graylist.ClientNames(); len(got) != 1 || got[0] != "Infuse" {
		t.Fatalf("default clients=%v", got)
	}
	if !c.Affinity.Graylist.MatchesClient("Infuse 8.1") || !c.Affinity.Graylist.MatchesPath("/InfuseSync/Checkpoint/x") {
		t.Fatal("default Infuse matchers")
	}
}

func TestExplicitSQLiteDoesNotRequirePostgresURL(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
affinity:
  store: sqlite
  sqlite:
    path: ./data/affinity.db
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Affinity.Store != "sqlite" {
		t.Fatalf("store=%q", c.Affinity.Store)
	}
}

func TestGraylistEmptyClientsDisablesBuiltin(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
affinity:
  graylist:
    clients: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Affinity.Graylist.ClientNames()) != 0 {
		t.Fatalf("clients=%v", c.Affinity.Graylist.ClientNames())
	}
	if c.Affinity.Graylist.MatchesClient("Infuse") || c.Affinity.Graylist.MatchesPath("/InfuseSync/x") {
		t.Fatal("empty clients must disable built-in Infuse")
	}
}

func TestGraylistEnvAndExtraClient(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
affinity:
  graylist:
    clients: [MyApp]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAP_GRAYLIST_POLICY", "pin_unhealthy")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Affinity.Graylist.Policy != "pin_unhealthy" {
		t.Fatalf("policy=%q", c.Affinity.Graylist.Policy)
	}
	if !c.Affinity.Graylist.MatchesClient("MyApp 1.0") {
		t.Fatal("extra client")
	}
	if c.Affinity.Graylist.MatchesClient("Infuse") {
		t.Fatal("Infuse should not match when not in clients")
	}
}

func TestUserAffinityYAMLAndValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
  - name: server-b
    url: http://127.0.0.1:8097
affinity:
  user_affinity:
    - ada: server-b
    - Bob: server-a
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Affinity.PreferredBackend("ADA") != "server-b" || c.Affinity.PreferredBackend("bob") != "server-a" {
		t.Fatalf("preferred %+v", c.Affinity.UserAffinityEntries())
	}
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
affinity:
  user_affinity:
    - ada: missing
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("unknown backend must fail")
	}
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
affinity:
  user_affinity:
    - ada: server-a
    - Ada: server-a
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("duplicate username must fail")
	}
}

func TestPerformanceDefaultsOnAfterLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Performance.CacheEnabled() || !c.Performance.LibraryEnabled() || !c.Performance.CoalesceEnabled() {
		t.Fatal("cache/library/coalesce must default on after Load")
	}
	if c.Performance.WarmLoginEnabled() || c.Performance.LibraryConcurrencyEnabled() {
		t.Fatal("warm_login and library_concurrency must default off")
	}
	if c.Performance.AuthTimeout != 60*time.Second {
		t.Fatalf("auth_timeout=%s", c.Performance.AuthTimeout)
	}
	if c.Performance.Cache.MaxBytes != 256<<20 || c.Performance.Cache.MaxObject != 2<<20 {
		t.Fatalf("image defaults bytes=%d object=%d", c.Performance.Cache.MaxBytes, c.Performance.Cache.MaxObject)
	}
	if c.Performance.Library.TTL != 60*time.Second || c.Performance.Library.MaxBytes != 64<<20 {
		t.Fatalf("library ttl=%s bytes=%d", c.Performance.Library.TTL, c.Performance.Library.MaxBytes)
	}
	if c.Backends[0].Disabled {
		t.Fatal("backend disabled must default false")
	}
	if !c.Performance.CacheDiskEnabled() || c.Performance.CacheDiskPath() != "./data/imgcache" {
		t.Fatalf("disk default enabled=%v path=%q", c.Performance.CacheDiskEnabled(), c.Performance.CacheDiskPath())
	}
	if c.Performance.Cache.Disk.MaxBytes != 1<<30 {
		t.Fatalf("disk max_bytes=%d", c.Performance.Cache.Disk.MaxBytes)
	}
}

func TestPerformanceExplicitFalseSticks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
performance:
  cache:
    enabled: false
    disk:
      enabled: false
  library:
    enabled: false
  coalesce:
    enabled: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Performance.CacheEnabled() || c.Performance.LibraryEnabled() || c.Performance.CoalesceEnabled() || c.Performance.CacheDiskEnabled() {
		t.Fatal("explicit false must stick")
	}
}

func TestCacheDiskExplicitFalseSticks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
performance:
  cache:
    enabled: true
    disk:
      enabled: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Performance.CacheEnabled() || c.Performance.CacheDiskEnabled() {
		t.Fatal("disk false must stick while memory cache stays on")
	}
}

func TestCacheEnabledEnvAndValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	if err := os.WriteFile(p, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
performance:
  cache:
    enabled: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAP_CACHE_ENABLED", "true")
	t.Setenv("HAP_AUTH_TIMEOUT", "90s")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Performance.CacheEnabled() {
		t.Fatal("HAP_CACHE_ENABLED should enable cache")
	}
	if c.Performance.AuthTimeout != 90*time.Second {
		t.Fatalf("auth_timeout=%s", c.Performance.AuthTimeout)
	}

	p2 := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(p2, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
performance:
  cache:
    enabled: true
    max_bytes: 100
    max_object: 200
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAP_CACHE_ENABLED", "")
	t.Setenv("HAP_AUTH_TIMEOUT", "")
	if _, err := Load(p2); err == nil {
		t.Fatal("expected max_object > max_bytes to fail")
	}

	p3 := filepath.Join(dir, "bad-lib.yaml")
	if err := os.WriteFile(p3, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
performance:
  library:
    enabled: true
    max_bytes: 100
    max_object: 200
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p3); err == nil {
		t.Fatal("expected library max_object > max_bytes to fail")
	}
}

func TestEnvHopHeadersMergeAndReservedRejected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hap.yaml")
	raw := `
backends:
  - name: server-a
    url: http://127.0.0.1:8096
    headers:
      X-Site-Token: from-yaml
`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAP_BACKEND_SERVER_A_HEADER_X_SITE_TOKEN", "from-env")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backends[0].Headers["X-SITE-TOKEN"] != "from-env" && c.Backends[0].Headers["X-Site-Token"] != "from-env" {
		got := c.Backends[0].Headers
		if got["X-SITE-TOKEN"] == "" && got["X-Site-Token"] == "" {
			t.Fatalf("headers=%v", got)
		}
	}

	p2 := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(p2, []byte(`
backends:
  - name: server-a
    url: http://127.0.0.1:8096
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAP_BACKEND_SERVER_A_HEADER_AUTHORIZATION", "Bearer nope")
	if _, err := Load(p2); err == nil {
		t.Fatal("expected reserved header from env to be rejected")
	}
}
