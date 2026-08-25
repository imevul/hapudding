package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/authheader"
	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/store"
)

func TestLookupOrderTokenWins(t *testing.T) {
	rt, st, mon := testRouter(t, "fail_closed", 2)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok-a", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindDevice(ctx, "dev-1", "server-b", ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1", Token="tok-a"`)
	d := rt.Decide(ctx, req, authheader.Parse(req))
	if d.Backend == nil || d.Backend.Name != "server-a" || d.Kind != store.KindToken {
		t.Fatalf("token must win, got %+v backend=%v", d, name(d))
	}
}

func TestDeviceThenAnonFallback(t *testing.T) {
	rt, st, mon := testRouter(t, "fail_closed", 2)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindDevice(ctx, "dev-1", "server-b", ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1"`)
	d := rt.Decide(ctx, req, authheader.Parse(req))
	if name(d) != "server-b" {
		t.Fatalf("device fallback got %s", name(d))
	}

	req2 := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	req2.RemoteAddr = "10.0.0.9:1"
	req2.Header.Set("User-Agent", "glue-ua")
	if err := st.BindAnon(ctx, store.HashAnon("10.0.0.9", "glue-ua"), "server-a"); err != nil {
		t.Fatal(err)
	}
	d2 := rt.Decide(ctx, req2, authheader.Parse(req2))
	if name(d2) != "server-a" {
		t.Fatalf("anon glue got %s", name(d2))
	}
}

func TestWebSocketAPIKeyRoutesToBoundBackend(t *testing.T) {
	rt, st, mon := testRouter(t, "fail_closed", 2)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "ws-tok", "server-b", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/socket?api_key=ws-tok&deviceId=other", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	d := rt.Decide(ctx, req, authheader.Parse(req))
	if name(d) != "server-b" {
		t.Fatalf("ws api_key should follow token, got %s", name(d))
	}
}

func TestPoliciesBoundUnhealthy(t *testing.T) {
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1", Token="tok"`)

	t.Run("fail_closed", func(t *testing.T) {
		rt, st, mon := testRouter(t, "fail_closed", 2)
		mon.SetState("server-a", health.StateUnhealthy)
		mon.SetState("server-b", health.StateHealthy)
		if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{}); err != nil {
			t.Fatal(err)
		}
		d := rt.Decide(ctx, req, authheader.Parse(req))
		if d.HAPError != "bound_backend_unavailable" || d.HAPStatus != http.StatusServiceUnavailable || d.DropClient {
			t.Fatalf("%+v", d)
		}
		if row, _ := st.LookupToken(ctx, "tok"); row == nil {
			t.Fatal("fail_closed must keep the binding")
		}
	})

	t.Run("force_reauth", func(t *testing.T) {
		rt, st, mon := testRouter(t, "force_reauth", 2)
		mon.SetState("server-a", health.StateUnhealthy)
		mon.SetState("server-b", health.StateHealthy)
		if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{}); err != nil {
			t.Fatal(err)
		}
		d := rt.Decide(ctx, req, authheader.Parse(req))
		if !d.RefuseToken || !d.DropClient || d.HAPStatus != http.StatusUnauthorized || d.HAPError != "reauth_required" {
			t.Fatalf("%+v", d)
		}
		if d.Backend != nil {
			t.Fatal("must not send the old token to another backend")
		}
	})

	t.Run("pin_unhealthy", func(t *testing.T) {
		rt, st, mon := testRouter(t, "pin_unhealthy", 2)
		mon.SetState("server-a", health.StateUnhealthy)
		mon.SetState("server-b", health.StateHealthy)
		if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{}); err != nil {
			t.Fatal(err)
		}
		d := rt.Decide(ctx, req, authheader.Parse(req))
		if name(d) != "server-a" || d.HAPError != "" {
			t.Fatalf("pin still targets A, got %+v", d)
		}
	})
}

func TestForceReauthUnauthenticatedPicksEligible(t *testing.T) {
	rt, st, mon := testRouter(t, "force_reauth", 2)
	mon.SetState("server-a", health.StateUnhealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindDevice(ctx, "dev-1", "server-a", ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1"`)
	d := rt.Decide(ctx, req, authheader.Parse(req))
	if !d.DropClient || name(d) != "server-b" {
		t.Fatalf("expected drop + B, got %+v %s", d, name(d))
	}
}

func TestPinUnhealthyNewClientStaysOnHashedBackend(t *testing.T) {
	rt, _, mon := testRouter(t, "pin_unhealthy", 2)
	mon.SetState("server-a", health.StateUnhealthy)
	mon.SetState("server-b", health.StateHealthy)
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	id := authheader.Identifiers{DeviceID: "pin-me"}
	want := rt.hashName("pin-me", "", []string{"server-a", "server-b"})
	d := rt.Decide(context.Background(), req, id)
	if want == "server-a" {
		if d.HAPError != "bound_backend_unavailable" || name(d) != "server-a" {
			t.Fatalf("hashed A is down: %+v %s", d, name(d))
		}
	} else if name(d) != "server-b" || d.HAPError != "" {
		t.Fatalf("hashed B is up: %+v %s", d, name(d))
	}
}

func TestPoolSizeOneAndThree(t *testing.T) {
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("Authorization", `MediaBrowser Token="tok"`)

	rt1, st1, mon1 := testRouter(t, "force_reauth", 1)
	mon1.SetState("server-a", health.StateUnhealthy)
	if err := st1.BindToken(ctx, "tok", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	d1 := rt1.Decide(ctx, req, authheader.Parse(req))
	if d1.HAPStatus != http.StatusUnauthorized {
		t.Fatalf("N=1 force_reauth token: %+v", d1)
	}
	reqPub := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	d1b := rt1.Decide(ctx, reqPub, authheader.Identifiers{})
	if d1b.HAPError != "no_eligible_backend" {
		t.Fatalf("N=1 no eligible: %+v", d1b)
	}

	rt3, st3, mon3 := testRouter(t, "fail_closed", 3)
	mon3.SetState("server-a", health.StateUnhealthy)
	mon3.SetState("server-b", health.StateHealthy)
	mon3.SetState("server-c", health.StateHealthy)
	if err := st3.BindToken(ctx, "tok", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	d3 := rt3.Decide(ctx, req, authheader.Parse(req))
	if d3.HAPError != "bound_backend_unavailable" {
		t.Fatalf("N=3 fail_closed must not special-case a pair: %+v", d3)
	}
	d3new := rt3.Decide(ctx, reqPub, authheader.Identifiers{DeviceID: "fresh"})
	if name(d3new) != "server-b" && name(d3new) != "server-c" {
		t.Fatalf("new client should land on eligible B/C, got %s", name(d3new))
	}
}

func TestCookieHintIsNotSoleAffinity(t *testing.T) {
	rt, st, mon := testRouter(t, "fail_closed", 2)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("Authorization", `MediaBrowser Token="tok"`)
	req.AddCookie(&http.Cookie{Name: "hap_backend", Value: "server-b"})
	d := rt.Decide(ctx, req, authheader.Parse(req))
	if name(d) != "server-a" {
		t.Fatalf("cookie must not override token, got %s", name(d))
	}
}

func TestDegradedBoundStillProxied(t *testing.T) {
	rt, st, mon := testRouter(t, "fail_closed", 2)
	mon.SetState("server-a", health.StateDegraded)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("Authorization", `MediaBrowser Token="tok"`)
	d := rt.Decide(ctx, req, authheader.Parse(req))
	if name(d) != "server-a" || d.HAPError != "" {
		t.Fatalf("degraded bound must still proxy: %+v", d)
	}
}

func TestReady(t *testing.T) {
	rt, _, mon := testRouter(t, "fail_closed", 1)
	mon.SetState("server-a", health.StateUnknown)
	if !rt.Ready() {
		t.Fatal("1-backend unknown should be ready")
	}
	mon.SetState("server-a", health.StateUnhealthy)
	if rt.Ready() {
		t.Fatal("1-backend unhealthy and not eligible should not be ready")
	}
}

func testRouter(t *testing.T, policy string, n int) (*Router, store.Store, *health.Monitor) {
	t.Helper()
	cfg := &config.Config{
		Affinity: config.Affinity{
			Policy:            policy,
			NewClientsRequire: "healthy",
			Graylist:          config.Graylist{Policy: "fail_closed"},
			Store:             "sqlite",
			TokenTTL:          time.Hour,
			DeviceTTL:         time.Hour,
			AnonTTL:           time.Hour,
		},
	}
	names := []string{"server-a", "server-b", "server-c"}
	for i := 0; i < n; i++ {
		cfg.Backends = append(cfg.Backends, config.Backend{
			Name: names[i],
			URL:  "http://127.0.0.1:809" + string(rune('7'+i)),
		})
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "a.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, st, mon), st, mon
}

func TestGraylistInfuseUsesFailClosed(t *testing.T) {
	ctx := context.Background()
	rt, st, mon := testRouter(t, "force_reauth", 2)
	mon.SetState("server-a", health.StateUnhealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{Client: "Infuse"}); err != nil {
		t.Fatal(err)
	}

	reqInf := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	reqInf.Header.Set("Authorization", `MediaBrowser Client="Infuse", DeviceId="dev-1", Token="tok"`)
	d := rt.Decide(ctx, reqInf, authheader.Parse(reqInf))
	if !d.Graylisted || d.HAPError != "bound_backend_unavailable" || d.DropClient {
		t.Fatalf("Infuse should fail_closed: %+v", d)
	}
	if row, _ := st.LookupToken(ctx, "tok"); row == nil {
		t.Fatal("Infuse binding must be kept")
	}

	reqCLI := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	reqCLI.Header.Set("Authorization", `MediaBrowser Client="CLIamp", DeviceId="dev-2", Token="tok-cli"`)
	if err := st.BindToken(ctx, "tok-cli", "server-a", store.TokenRow{Client: "CLIamp"}); err != nil {
		t.Fatal(err)
	}
	d2 := rt.Decide(ctx, reqCLI, authheader.Parse(reqCLI))
	if d2.Graylisted || !d2.RefuseToken || d2.HAPError != "reauth_required" {
		t.Fatalf("CLIamp should force_reauth: %+v", d2)
	}
}

func TestGraylistTokenOnlyAndInfuseSyncAndDeviceAfterLogout(t *testing.T) {
	ctx := context.Background()
	rt, st, mon := testRouter(t, "force_reauth", 2)
	mon.SetState("server-a", health.StateUnhealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindToken(ctx, "ws-tok", "server-a", store.TokenRow{Client: "Infuse", DeviceID: "dev-i"}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindDevice(ctx, "dev-i", "server-a", "Infuse"); err != nil {
		t.Fatal(err)
	}

	reqWS := httptest.NewRequest(http.MethodGet, "/socket?api_key=ws-tok", nil)
	d := rt.Decide(ctx, reqWS, authheader.Parse(reqWS))
	if !d.Graylisted || d.HAPError != "bound_backend_unavailable" {
		t.Fatalf("token-only Infuse: %+v", d)
	}

	reqSync := httptest.NewRequest(http.MethodGet, "/InfuseSync/Checkpoint/x", nil)
	if err := st.BindDevice(ctx, "dev-sync", "server-a", ""); err != nil {
		t.Fatal(err)
	}
	reqSync.Header.Set("Authorization", `MediaBrowser DeviceId="dev-sync"`)
	d2 := rt.Decide(ctx, reqSync, authheader.Parse(reqSync))
	if !d2.Graylisted || d2.HAPError != "bound_backend_unavailable" {
		t.Fatalf("InfuseSync path: %+v", d2)
	}

	if err := st.DeleteToken(ctx, "ws-tok"); err != nil {
		t.Fatal(err)
	}
	reqDev := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	reqDev.Header.Set("Authorization", `MediaBrowser DeviceId="dev-i"`)
	d3 := rt.Decide(ctx, reqDev, authheader.Parse(reqDev))
	if !d3.Graylisted || d3.HAPError != "bound_backend_unavailable" {
		t.Fatalf("DeviceId after logout: %+v", d3)
	}
}

func TestGraylistDisableBuiltinAndExtraMatcher(t *testing.T) {
	ctx := context.Background()
	rt, st, mon := testRouter(t, "force_reauth", 2)
	mon.SetState("server-a", health.StateUnhealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{Client: "Infuse"}); err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	rt.cfg.Affinity.Graylist.Clients = &empty
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("Authorization", `MediaBrowser Client="Infuse", Token="tok"`)
	d := rt.Decide(ctx, req, authheader.Parse(req))
	if d.Graylisted || d.HAPError != "reauth_required" {
		t.Fatalf("disabled Infuse: %+v", d)
	}

	extra := []string{"MyApp"}
	rt.cfg.Affinity.Graylist.Clients = &extra
	if err := st.BindToken(ctx, "my", "server-a", store.TokenRow{Client: "MyApp 1.0"}); err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req2.Header.Set("Authorization", `MediaBrowser Client="MyApp 1.0", Token="my"`)
	d2 := rt.Decide(ctx, req2, authheader.Parse(req2))
	if !d2.Graylisted || d2.HAPError != "bound_backend_unavailable" {
		t.Fatalf("extra matcher: %+v", d2)
	}
}

func name(d Decision) string {
	if d.Backend == nil {
		return ""
	}
	return d.Backend.Name
}
