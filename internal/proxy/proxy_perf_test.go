package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/libcache"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/status"
	"github.com/imevul/hapudding/internal/store"
	"io"
	"log/slog"
)

func TestLibraryCacheHitMissAndInvalidate(t *testing.T) {
	var hits int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/Sessions/Playing") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		hits++
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	t.Cleanup(a.Close)
	h, st, mon := testProxy(t, "fail_closed", a)
	on := true
	h.cfg.Performance.Library = config.LibraryCache{Enabled: &on, TTL: time.Hour, MaxBytes: 1 << 20, MaxObject: 1 << 20}
	h.lib = libcache.New(h.cfg.Performance.Library)
	mon.SetState("server-a", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok-a", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindToken(ctx, "tok-b", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}

	getLib(t, h, "tok-a", "/Users/u1/Views")
	getLib(t, h, "tok-a", "/Users/u1/Views")
	if hits != 1 {
		t.Fatalf("same token should hit, hops=%d", hits)
	}
	getLib(t, h, "tok-b", "/Users/u1/Views")
	if hits != 2 {
		t.Fatalf("other token must miss, hops=%d", hits)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(`{}`))
	req.Header.Set("Authorization", `MediaBrowser Token="tok-a"`)
	h.ServeHTTP(rec, req)
	getLib(t, h, "tok-a", "/Users/u1/Views")
	if hits != 3 {
		t.Fatalf("invalidate should drop token-a, hops=%d", hits)
	}
}

func TestLibraryCacheSeparatedByBackend(t *testing.T) {
	var hitA, hitB int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	t.Cleanup(a.Close)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"b":1}`))
	}))
	t.Cleanup(b.Close)
	h, st, mon := testProxy(t, "fail_closed", a, b)
	on := true
	h.cfg.Performance.Library = config.LibraryCache{Enabled: &on, TTL: time.Hour, MaxBytes: 1 << 20, MaxObject: 1 << 20}
	h.lib = libcache.New(h.cfg.Performance.Library)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok-a", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindToken(ctx, "tok-b", "server-b", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	getLib(t, h, "tok-a", "/Users/u1/Items/Latest")
	getLib(t, h, "tok-b", "/Users/u1/Items/Latest")
	getLib(t, h, "tok-a", "/Users/u1/Items/Latest")
	getLib(t, h, "tok-b", "/Users/u1/Items/Latest")
	if hitA != 1 || hitB != 1 {
		t.Fatalf("A=%d B=%d", hitA, hitB)
	}
}

func TestCoalesceIdenticalLatest(t *testing.T) {
	var hops atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	t.Cleanup(a.Close)
	h, st, mon := testProxy(t, "fail_closed", a)
	on := true
	h.cfg.Performance.Library = config.LibraryCache{Enabled: &on, TTL: time.Hour, MaxBytes: 1 << 20, MaxObject: 1 << 20}
	h.cfg.Performance.Coalesce.Enabled = &on
	h.lib = libcache.New(h.cfg.Performance.Library)
	mon.SetState("server-a", health.StateHealthy)
	if err := st.BindToken(context.Background(), "tok", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/Users/u1/Items/Latest", nil)
			req.Header.Set("Authorization", `MediaBrowser Token="tok"`)
			h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	<-started
	close(release)
	wg.Wait()
	if hops.Load() != 1 {
		t.Fatalf("want 1 hop, got %d", hops.Load())
	}
	if codes[0] != 200 || codes[1] != 200 {
		t.Fatalf("codes=%v", codes)
	}
}

func TestWarmLoginFillsLibraryCache(t *testing.T) {
	var views int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/users/authenticatebyname") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "issued-token",
				"User":        map[string]string{"Id": "u1", "Name": "ada"},
			})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/Views") {
			views++
		}
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	on := true
	h.cfg.Performance.Library = config.LibraryCache{Enabled: &on, TTL: time.Hour, MaxBytes: 1 << 20, MaxObject: 1 << 20}
	h.cfg.Performance.WarmLogin.Enabled = &on
	h.lib = libcache.New(h.cfg.Performance.Library)
	mon.SetState("server-a", health.StateHealthy)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser Client="Delfin", DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %d %s", rec.Code, rec.Body.String())
	}
	key := libcache.Key("server-a", store.HashToken("issued-token"), http.MethodGet, "/Users/u1/Views", "")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.lib.Get(key) == nil {
		time.Sleep(10 * time.Millisecond)
	}
	if h.lib.Get(key) == nil {
		t.Fatal("warm login did not fill Views cache")
	}
	before := views
	getLib(t, h, "issued-token", "/Users/u1/Views")
	if views != before {
		t.Fatalf("Views should be served from cache, hops %d -> %d", before, views)
	}
}

func TestAuthTimeoutUsedOnLogin(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "t",
			"User":        map[string]string{"Id": "u", "Name": "n"},
		})
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxyHop(t, "fail_closed", 5*time.Second, config.Cache{}, a)
	h.cfg.Performance.AuthTimeout = 80 * time.Millisecond
	h.cfg.Backends[0].Timeout = 5 * time.Second
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth_timeout should cut login before backend.timeout, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestStatusPerformanceAndBackendDisable(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(a.Close)
	h, st, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	srv := status.New(h.cfg, st, mon, h.rt, h.Cache(), h.Library(), func() any { return h.PerfSnapshot() }).Handler()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hap/performance", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "authTimeout") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/hap/backends/server-a/disable", nil))
	if rec2.Code != 200 || !strings.Contains(rec2.Body.String(), `"disabled":true`) {
		t.Fatalf("disable %d %s", rec2.Code, rec2.Body.String())
	}
	rec3 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Users/u/Views", nil)
	req.Header.Set("Authorization", `MediaBrowser Token="tok"`)
	_ = st.BindToken(context.Background(), "tok", "server-a", store.TokenRow{})
	h.ServeHTTP(rec3, req)
	if rec3.Code != http.StatusServiceUnavailable || !strings.Contains(rec3.Body.String(), "backend_disabled") {
		t.Fatalf("bound %d %s", rec3.Code, rec3.Body.String())
	}
	rec4 := httptest.NewRecorder()
	srv.ServeHTTP(rec4, httptest.NewRequest(http.MethodPost, "/hap/backends/server-a/enable", nil))
	if rec4.Code != 200 || strings.Contains(rec4.Body.String(), `"runtime_disabled":true`) {
		t.Fatalf("enable %d %s", rec4.Code, rec4.Body.String())
	}
}

func TestEnableConfigDisabledBackendConflicts(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(a.Close)
	cfg := &config.Config{
		Affinity: config.Affinity{
			Policy: "fail_closed", NewClientsRequire: "healthy",
			Graylist: config.Graylist{Policy: "fail_closed"}, Store: "sqlite",
			TokenTTL: time.Hour, DeviceTTL: time.Hour, AnonTTL: time.Hour,
		},
		Backends: []config.Backend{{Name: "server-a", URL: a.URL, Timeout: time.Second, Disabled: true, Headers: map[string]string{}}},
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "d.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt := router.New(cfg, st, mon)
	ph := New(cfg, rt, st, mon, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := status.New(cfg, st, mon, rt, ph.Cache(), ph.Library(), func() any { return ph.PerfSnapshot() }).Handler()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hap/backends/server-a/enable", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 got %d %s", rec.Code, rec.Body.String())
	}
}

func getLib(t *testing.T, h *Handler, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", `MediaBrowser Token="`+token+`"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %d %s", path, rec.Code, rec.Body.String())
	}
	return rec
}
