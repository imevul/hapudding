package status

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/store"
)

func TestAdminUIIndex(t *testing.T) {
	cfg := &config.Config{
		Affinity: config.Affinity{Policy: "fail_closed", NewClientsRequire: "healthy", Graylist: config.Graylist{Policy: "fail_closed"}},
		Backends: []config.Backend{{Name: "server-a", URL: "http://127.0.0.1:1", Timeout: time.Second}},
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "s.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := New(cfg, st, mon, router.New(cfg, st, mon), nil, nil, nil).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type %q", ct)
	}
	page := rec.Body.String()
	if !strings.Contains(page, "hap-admin") {
		t.Fatal("missing hap-admin marker")
	}
	if !strings.Contains(page, `id="tab-overview"`) || !strings.Contains(page, `id="tab-users"`) {
		t.Fatal("missing overview/users tabs")
	}
	if !strings.Contains(page, `id="user-modal"`) {
		t.Fatal("missing user modal")
	}

	css := httptest.NewRecorder()
	h.ServeHTTP(css, httptest.NewRequest(http.MethodGet, "/ui/app.css", nil))
	if css.Code != http.StatusOK || !strings.Contains(css.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css %d %s", css.Code, css.Header().Get("Content-Type"))
	}

	miss := httptest.NewRecorder()
	h.ServeHTTP(miss, httptest.NewRequest(http.MethodGet, "/ui/secret.txt", nil))
	if miss.Code != http.StatusNotFound {
		t.Fatalf("unexpected file %d", miss.Code)
	}

	backends := httptest.NewRecorder()
	h.ServeHTTP(backends, httptest.NewRequest(http.MethodGet, "/hap/backends", nil))
	if backends.Code != http.StatusOK || !strings.Contains(backends.Header().Get("Content-Type"), "json") {
		t.Fatalf("json still required: %d %s", backends.Code, backends.Header().Get("Content-Type"))
	}
}

func TestUsersListSortedByUsernameThenID(t *testing.T) {
	cfg := &config.Config{
		Affinity: config.Affinity{Policy: "fail_closed", NewClientsRequire: "healthy", Graylist: config.Graylist{Policy: "fail_closed"}},
		Backends: []config.Backend{
			{Name: "server-a", URL: "http://127.0.0.1:1", Timeout: time.Second},
			{Name: "server-b", URL: "http://127.0.0.1:2", Timeout: time.Second},
		},
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "s.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	binds := []store.TokenRow{
		{UserID: "z-1", Username: "zoe"},
		{UserID: "a-2", Username: "Ada"},
		{UserID: "a-1", Username: "ada"},
	}
	for _, row := range binds {
		if err := st.BindToken(ctx, "tok-"+row.UserID, "server-a", row); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.BindToken(ctx, "tok-ada-b", "server-b", store.TokenRow{UserID: "a-1", Username: "ada"}); err != nil {
		t.Fatal(err)
	}
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := New(cfg, st, mon, router.New(cfg, st, mon), nil, nil, nil).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hap/users", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var rows []userRow
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.Username+"/"+fmt.Sprint(r.UserID)+"/"+r.Backend)
	}
	want := []string{"ada/a-1/server-a", "ada/a-1/server-b", "Ada/a-2/server-a", "zoe/z-1/server-a"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestUsersScopedByBackendAndRedacted(t *testing.T) {
	cfg := &config.Config{
		Affinity: config.Affinity{Policy: "fail_closed", NewClientsRequire: "healthy", Graylist: config.Graylist{Policy: "fail_closed"}},
		Backends: []config.Backend{
			{Name: "server-a", URL: "http://127.0.0.1:1", Timeout: time.Second},
			{Name: "server-b", URL: "http://127.0.0.1:2", Timeout: time.Second},
		},
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "s.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.BindToken(ctx, "raw-token-must-not-leak", "server-a", store.TokenRow{UserID: "u1", Username: "ada", Client: "Infuse"}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindToken(ctx, "other-secret", "server-b", store.TokenRow{UserID: "u1", Username: "ada-b"}); err != nil {
		t.Fatal(err)
	}
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	h := New(cfg, st, mon, router.New(cfg, st, mon), nil, nil, nil).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hap/users/u1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "raw-token-must-not-leak") || strings.Contains(body, "other-secret") {
		t.Fatal("raw tokens leaked")
	}
	var dumps []userDump
	if err := json.Unmarshal(rec.Body.Bytes(), &dumps); err != nil {
		t.Fatal(err)
	}
	if len(dumps) != 2 {
		t.Fatalf("want both backends, got %d: %s", len(dumps), body)
	}
	var sawGray bool
	for _, d := range dumps {
		if d.Backend == "server-a" && d.Graylisted {
			sawGray = true
		}
		if d.Backend == "server-b" && d.Graylisted {
			t.Fatal("server-b ada-b should not be gray-listed")
		}
	}
	if !sawGray {
		t.Fatalf("expected server-a Infuse graylisted: %s", body)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/hap/users/u1?backend=server-a", nil))
	if rec2.Code != http.StatusOK {
		t.Fatal(rec2.Body.String())
	}
	var one []userDump
	if err := json.Unmarshal(rec2.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Backend != "server-a" {
		t.Fatalf("%+v", one)
	}
}

func TestUserAffinityAndUnpin(t *testing.T) {
	cfg := &config.Config{
		Affinity: config.Affinity{
			Policy:            "fail_closed",
			NewClientsRequire: "healthy",
			Graylist:          config.Graylist{Policy: "fail_closed"},
			UserAffinity:      config.UserAffinityList{{"ada": "server-b"}},
		},
		Backends: []config.Backend{
			{Name: "server-a", URL: "http://127.0.0.1:1", Timeout: time.Second},
			{Name: "server-b", URL: "http://127.0.0.1:2", Timeout: time.Second},
		},
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "s.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok", "server-a", store.TokenRow{Username: "ada", DeviceID: "dev-ada"}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindDevice(ctx, "dev-ada", "server-a", ""); err != nil {
		t.Fatal(err)
	}
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	h := New(cfg, st, mon, router.New(cfg, st, mon), nil, nil, nil).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hap/user-affinity", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"ada"`) || !strings.Contains(rec.Body.String(), `"backend":"server-b"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/hap/users/by-name/Ada/unpin", nil))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"tokens":1`) {
		t.Fatalf("unpin %d %s", rec2.Code, rec2.Body.String())
	}
	if row, _ := st.LookupToken(ctx, "tok"); row != nil {
		t.Fatal("token should be gone")
	}

	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodPost, "/hap/users/by-name/%20/unpin", nil))
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("empty username %d %s", rec3.Code, rec3.Body.String())
	}
}

func TestUnpinResolvesPublicUserIDForWipedTokens(t *testing.T) {
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/Public" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{{"Name": "ada", "Id": "u-ada"}})
	}))
	t.Cleanup(pub.Close)
	cfg := &config.Config{
		Affinity: config.Affinity{Policy: "fail_closed", NewClientsRequire: "healthy", Graylist: config.Graylist{Policy: "fail_closed"}},
		Backends: []config.Backend{{Name: "server-a", URL: pub.URL, Timeout: time.Second}},
	}
	st, err := store.Open("sqlite", filepath.Join(t.TempDir(), "s.db"), time.Hour, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.BindToken(ctx, "orphan", "server-a", store.TokenRow{DeviceID: "dev-1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindDevice(ctx, "dev-1", "server-a", ""); err != nil {
		t.Fatal(err)
	}
	mon, err := health.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mon.SetState("server-a", health.StateHealthy)
	h := New(cfg, st, mon, router.New(cfg, st, mon), nil, nil, nil).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/hap/users/by-name/ada/unpin", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tokens":1`) {
		t.Fatalf("unpin orphan %d %s", rec.Code, rec.Body.String())
	}
	if row, _ := st.LookupToken(ctx, "orphan"); row != nil {
		t.Fatal("wiped token should be gone")
	}
}
