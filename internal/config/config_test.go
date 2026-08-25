package config

import (
	"os"
	"path/filepath"
	"testing"
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
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.Affinity.Policy != "fail_closed" {
		t.Fatalf("listen=%q policy=%q", c.Listen, c.Affinity.Policy)
	}
	if c.Affinity.Store != "sqlite" {
		t.Fatalf("store=%q", c.Affinity.Store)
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
