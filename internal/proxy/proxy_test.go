package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/status"
	"github.com/imevul/hapudding/internal/store"
)

func TestPublicInfoIDPassedThrough(t *testing.T) {
	a := backend(t, "SERVER-A-ID", http.StatusOK, "")
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"Id":"SERVER-A-ID"`) {
		t.Fatalf("Id rewritten or dropped: %s", rec.Body.String())
	}
}

func TestBackend500NotRewritten(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "jellyfin-db-locked", http.StatusInternalServerError)
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	req.Header.Set("Authorization", `MediaBrowser Token="t", DeviceId="d"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 from backend, got %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-HAP-Error") != "" {
		t.Fatal("must not synthesize a HAP error for a backend 500")
	}
	if !strings.Contains(rec.Body.String(), "jellyfin-db-locked") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestLoginPeekBindsTokenAndLogoutDropsTokenOnly(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(strings.ToLower(r.URL.Path), "/users/authenticatebyname"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "issued-token",
				"ServerId":    "SERVER-A-ID",
				"User":        map[string]string{"Id": "u-a", "Name": "ada"},
				"SessionInfo": map[string]string{"DeviceId": "dev-1"},
			})
		case strings.HasSuffix(strings.ToLower(r.URL.Path), "/sessions/logout"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(a.Close)
	h, st, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %d %s", rec.Code, rec.Body.String())
	}
	row, err := st.LookupToken(req.Context(), "issued-token")
	if err != nil || row == nil || row.Backend != "server-a" || row.UserID != "u-a" {
		t.Fatalf("peek bind %+v err=%v", row, err)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/Sessions/Logout", nil)
	req2.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1", Token="issued-token"`)
	h.ServeHTTP(rec2, req2)
	gone, err := st.LookupToken(req2.Context(), "issued-token")
	if err != nil || gone != nil {
		t.Fatalf("token should be dropped: %+v %v", gone, err)
	}
	dev, err := st.LookupDevice(req2.Context(), "dev-1")
	if err != nil || dev == nil {
		t.Fatal("DeviceId must survive logout")
	}
}

func TestHopHeadersMerged(t *testing.T) {
	var seen string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Site-Token")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	// rebuild with hop header
	cfg := h.cfg
	cfg.Backends[0].Headers["X-Site-Token"] = "site-secret"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	h.ServeHTTP(rec, req)
	if seen != "site-secret" {
		t.Fatalf("hop header not merged, got %q", seen)
	}
}

func TestPublicListenerHidesStatusPaths(t *testing.T) {
	a := backend(t, "A", http.StatusOK, "")
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	for _, path := range []string{"/hap/users", "/hap/health", "/metrics"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s want 404 got %d", path, rec.Code)
		}
	}
}

func TestStatusUsersRedactTokens(t *testing.T) {
	a := backend(t, "A", http.StatusOK, "")
	h, st, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	if err := st.BindToken(context.Background(), "super-secret-token", "server-a", store.TokenRow{
		UserID: "user-1", Username: "ada", DeviceID: "dev-1",
	}); err != nil {
		t.Fatal(err)
	}
	srv := status.New(h.cfg, st, mon, router.New(h.cfg, st, mon))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hap/users/user-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "super-secret-token") {
		t.Fatal("raw token leaked")
	}
	if !strings.Contains(body, store.HashToken("super-secret-token")[:12]) {
		t.Fatalf("expected hash prefix: %s", body)
	}

	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/hap/users/user-1?backend=server-b", nil))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("filter to unknown backend: %d", rec2.Code)
	}
}

func TestTwoBackendsTokenStaysOnIssuer(t *testing.T) {
	var hitA, hitB int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"backend":"a"}`)
	}))
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"backend":"b"}`)
	}))
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	h, st, mon := testProxy(t, "fail_closed", a, b)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindToken(context.Background(), "tok-a", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
		req.Header.Set("Authorization", `MediaBrowser Token="tok-a"`)
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"backend":"a"`) {
			t.Fatalf("round %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if hitA != 5 || hitB != 0 {
		t.Fatalf("authenticated round-robin? A=%d B=%d", hitA, hitB)
	}
}

func TestInfuseHopRangeUnchangedAndConnectionClose(t *testing.T) {
	var gotRange, gotConn string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotConn = r.Header.Get("Connection")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("chunk"))
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "force_reauth", a)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Videos/1/stream", nil)
	req.Header.Set("Authorization", `MediaBrowser Client="Infuse", DeviceId="dev-1"`)
	req.Header.Set("Range", "bytes=0-")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "chunk" {
		t.Fatalf("body buffered or rewritten: %q", rec.Body.String())
	}
	if gotRange != "bytes=0-" {
		t.Fatalf("Range rewritten: %q", gotRange)
	}
	if !strings.EqualFold(gotConn, "close") {
		t.Fatalf("want Connection: close, got %q", gotConn)
	}
}

func backend(t *testing.T, id string, status int, extra string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		body := extra
		if body == "" {
			body = `{"Id":"` + id + `","ServerName":"A"}`
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func testProxy(t *testing.T, policy string, servers ...*httptest.Server) (*Handler, store.Store, *health.Monitor) {
	t.Helper()
	cfg := &config.Config{
		Listen: ":0",
		Status: config.Status{Listen: "127.0.0.1:0"},
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
	for i, s := range servers {
		cfg.Backends = append(cfg.Backends, config.Backend{
			Name:    names[i],
			URL:     s.URL,
			Timeout: 5 * time.Second,
			Headers: map[string]string{},
		})
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "p.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rt := router.New(cfg, st, mon)
	return New(cfg, rt, st, mon, slog.New(slog.NewTextHandler(io.Discard, nil))), st, mon
}
