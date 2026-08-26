package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/status"
	"github.com/imevul/hapudding/internal/store"
)

func TestStayOnOriginOffLeavesLocation(t *testing.T) {
	var gotHost, gotXFH string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFH = r.Header.Get("X-Forwarded-Host")
		w.Header().Set("Location", "https://server-a.example.test/web/")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://hap.example.test/web/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Location") != "https://server-a.example.test/web/" {
		t.Fatalf("off must leave Location, got %q", rec.Header().Get("Location"))
	}
	if gotXFH != "" {
		t.Fatalf("off must not set X-Forwarded-Host, got %q", gotXFH)
	}
	if gotHost == "hap.example.test" {
		t.Fatal("off hop Host must stay the backend URL host")
	}
}

func TestStayOnOriginRewritesRedirectAndForwardedHost(t *testing.T) {
	var gotHost, gotXFH, gotXFP string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFH = r.Header.Get("X-Forwarded-Host")
		gotXFP = r.Header.Get("X-Forwarded-Proto")
		w.Header().Set("Location", "https://server-a.example.test/web/")
		w.Header().Set("Content-Location", "https://server-a.example.test/web/index.html")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	h.cfg.Web.StayOnOrigin.Enabled = boolPtr(true)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://hap.example.test/web/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Location") != "http://hap.example.test/web/" {
		t.Fatalf("Location=%q", rec.Header().Get("Location"))
	}
	if rec.Header().Get("Content-Location") != "http://hap.example.test/web/index.html" {
		t.Fatalf("Content-Location=%q", rec.Header().Get("Content-Location"))
	}
	if gotXFH != "hap.example.test" || gotHost != "hap.example.test" {
		t.Fatalf("hop Host=%q X-Forwarded-Host=%q", gotHost, gotXFH)
	}
	if gotXFP != "http" {
		t.Fatalf("X-Forwarded-Proto=%q", gotXFP)
	}
}

func TestStayOnOriginRewritesPublicURLsNotItemIDs(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(strings.ToLower(r.URL.Path), "/system/info/public"):
			_, _ = w.Write([]byte(`{"Id":"SERVER-A-ID","LocalAddress":"https://server-a.example.test:8096","WanAddress":"https://server-a.example.test"}`))
		case strings.Contains(r.URL.Path, "/Items"):
			_, _ = w.Write([]byte(`{"Items":[{"Id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","Name":"Show"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	h.cfg.Web.StayOnOrigin.Enabled = boolPtr(true)
	mon.SetState("server-a", health.StateHealthy)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://hap.example.test/System/Info/Public", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("public %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"Id":"SERVER-A-ID"`) {
		t.Fatalf("stay-on-origin must not rewrite Id: %s", body)
	}
	if !strings.Contains(body, `"LocalAddress":"http://hap.example.test:8096"`) && !strings.Contains(body, `"LocalAddress":"http://hap.example.test`) {
		t.Fatalf("LocalAddress not rewritten: %s", body)
	}
	if strings.Contains(body, "server-a.example.test") {
		t.Fatalf("backend URL left in public info: %s", body)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "http://hap.example.test/Users/u-a/Items", nil)
	req2.Header.Set("Authorization", `MediaBrowser Token="t", DeviceId="d"`)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("items %d %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), `"Id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"`) {
		t.Fatalf("item Id rewritten: %s", rec2.Body.String())
	}
}

func TestStayOnOriginLoginSetsForwardedHost(t *testing.T) {
	var gotXFH, gotHost string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFH = r.Header.Get("X-Forwarded-Host")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-token",
			"User":        map[string]string{"Id": "u-a", "Name": "ada"},
		})
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	h.cfg.Web.StayOnOrigin.Enabled = boolPtr(true)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://hap.example.test/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser Client="Delfin", DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if gotXFH != "hap.example.test" || gotHost != "hap.example.test" {
		t.Fatalf("login hop Host=%q X-Forwarded-Host=%q", gotHost, gotXFH)
	}
}

func TestStayOnOriginRewritesPlaylistAbsURLs(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\nhttps://server-a.example.test/Videos/x/segment.ts\nsegment.ts\n"))
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	h.cfg.Web.StayOnOrigin.Enabled = boolPtr(true)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://hap.example.test/Videos/x/master.m3u8", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "http://hap.example.test/Videos/x/segment.ts") {
		t.Fatalf("absolute playlist URL not rewritten: %s", body)
	}
	if !strings.Contains(body, "\nsegment.ts\n") {
		t.Fatalf("relative playlist URI rewritten: %s", body)
	}
}

func TestTranslateServerIDPublicOnly(t *testing.T) {
	const hapID = "11111111-2222-3333-4444-555555555555"
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(strings.ToLower(r.URL.Path), "/system/info/public") || strings.HasSuffix(strings.ToLower(r.URL.Path), "/system/info"):
			_, _ = w.Write([]byte(`{"Id":"SERVER-A-ID","ServerName":"A"}`))
		case strings.Contains(r.URL.Path, "/Items"):
			_, _ = w.Write([]byte(`{"Items":[{"Id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	h.cfg.Translate.ServerID.Enabled = boolPtr(true)
	h.cfg.Translate.ServerID.ID = hapID
	mon.SetState("server-a", health.StateHealthy)

	for _, path := range []string{"/System/Info/Public", "/System/Info"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %d %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"Id":"`+hapID+`"`) {
			t.Fatalf("%s Id not translated: %s", path, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ServerName":"A"`) {
			t.Fatalf("%s empty name must leave ServerName: %s", path, rec.Body.String())
		}
	}

	h.cfg.Translate.ServerID.Name = "HAP"
	recName := httptest.NewRecorder()
	reqName := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
	h.ServeHTTP(recName, reqName)
	if !strings.Contains(recName.Body.String(), `"ServerName":"HAP"`) {
		t.Fatalf("ServerName not overridden: %s", recName.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/Users/u-a/Items", nil)
	req2.Header.Set("Authorization", `MediaBrowser Token="t", DeviceId="d"`)
	h.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), `"Id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"`) {
		t.Fatalf("item Id rewritten: %s", rec2.Body.String())
	}
}

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

func TestExpectContinueLoginForwardsBodyWithoutExpect(t *testing.T) {
	var gotExpect, gotBody string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotExpect = r.Header.Get("Expect")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-token",
			"User":        map[string]string{"Id": "u-a", "Name": "ada"},
		})
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser Client="Delfin", DeviceId="dev-1"`)
	req.Header.Set("Expect", "100-continue")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %d %s", rec.Code, rec.Body.String())
	}
	if gotExpect != "" {
		t.Fatalf("Expect forwarded: %q", gotExpect)
	}
	if !strings.Contains(gotBody, `"Username":"ada"`) {
		t.Fatalf("body=%q", gotBody)
	}
}

func TestLoginHopStripsChunkedFraming(t *testing.T) {
	var gotTE, gotCL, gotBody string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTE = r.Header.Get("Transfer-Encoding")
		gotCL = r.Header.Get("Content-Length")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-token",
			"User":        map[string]string{"Id": "u-a", "Name": "ada"},
		})
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	body := `{"Username":"ada","Pw":"x"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(body))
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1"`)
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Set("Content-Length", "99999")
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %d %s", rec.Code, rec.Body.String())
	}
	if gotTE != "" {
		t.Fatalf("Transfer-Encoding forwarded: %q", gotTE)
	}
	if gotCL != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length=%q want %d", gotCL, len(body))
	}
	if !strings.Contains(gotBody, `"Username":"ada"`) {
		t.Fatalf("body=%q", gotBody)
	}
}

func TestLoginSetsBackendCookieAndSessionIP(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(strings.ToLower(r.URL.Path), "/users/authenticatebyname") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "issued-token",
				"User":        map[string]string{"Id": "u-a", "Name": "ada"},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(a.Close)
	h, st, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.RemoteAddr = "10.1.2.3:9"
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login %d %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "hap_backend" && c.Value == "server-a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing hap_backend cookie: %v", rec.Result().Cookies())
	}
	row, err := st.LookupAnon(req.Context(), store.HashSessionIP("10.1.2.3"))
	if err != nil || row == nil || row.Backend != "server-a" {
		t.Fatalf("session IP glue %+v err=%v", row, err)
	}
}

func TestHeaderlessImageFollowsSessionBackend(t *testing.T) {
	var hitA, hitB int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `a`)
	}))
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `b`)
	}))
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	h, st, mon := testProxy(t, "fail_closed", a, b)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok-a", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}

	recAuth := httptest.NewRecorder()
	reqAuth := httptest.NewRequest(http.MethodGet, "/Users/Me", nil)
	reqAuth.RemoteAddr = "10.9.8.7:1"
	reqAuth.Header.Set("Authorization", `MediaBrowser Token="tok-a"`)
	h.ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK {
		t.Fatalf("auth %d", recAuth.Code)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/xyz/Images/Primary", nil)
	req.RemoteAddr = "10.9.8.7:1"
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.AddCookie(&http.Cookie{Name: "hap_backend", Value: "server-a"})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "a" {
		t.Fatalf("image %d %s", rec.Code, rec.Body.String())
	}
	if hitA < 2 || hitB != 0 {
		t.Fatalf("image left the session backend A=%d B=%d", hitA, hitB)
	}
}

func TestLoginPrefersUserAffinityWhenUnbound(t *testing.T) {
	var hitA, hitB int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(a.Close)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-b",
			"User":        map[string]string{"Id": "u-b", "Name": "ada"},
		})
	}))
	t.Cleanup(b.Close)
	h, st, mon := testProxy(t, "fail_closed", a, b)
	h.cfg.Affinity.UserAffinity = config.UserAffinityList{{"ada": "server-b"}}
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	for i := 0; i < 3; i++ {
		_ = st.BindToken(context.Background(), "load-"+string(rune('a'+i)), "server-b", store.TokenRow{})
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser Client="HAP-Test"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hitB != 1 || hitA != 0 {
		t.Fatalf("want B first, code=%d A=%d B=%d body=%s", rec.Code, hitA, hitB, rec.Body.String())
	}
}

func TestLoginUserAffinityOverridesDevicePin(t *testing.T) {
	var hitA, hitB int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(a.Close)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-b",
			"User":        map[string]string{"Id": "u-b", "Name": "ada"},
		})
	}))
	t.Cleanup(b.Close)
	h, st, mon := testProxy(t, "fail_closed", a, b)
	h.cfg.Affinity.UserAffinity = config.UserAffinityList{{"ada": "server-b"}}
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindDevice(context.Background(), "dev-1", "server-a", ""); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hitB != 1 || hitA != 0 {
		t.Fatalf("user_affinity must beat leftover device pin, code=%d A=%d B=%d", rec.Code, hitA, hitB)
	}
}

func TestLoginUserAffinityDoesNotOverrideTokenPin(t *testing.T) {
	var hitA, hitB int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-a",
			"User":        map[string]string{"Id": "u-a", "Name": "ada"},
		})
	}))
	t.Cleanup(a.Close)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(b.Close)
	h, st, mon := testProxy(t, "fail_closed", a, b)
	h.cfg.Affinity.UserAffinity = config.UserAffinityList{{"ada": "server-b"}}
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindToken(context.Background(), "tok-a", "server-a", store.TokenRow{Username: "ada"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser Token="tok-a", DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hitA != 1 || hitB != 0 {
		t.Fatalf("live token pin must win, code=%d A=%d B=%d", rec.Code, hitA, hitB)
	}
}

func TestPathUserID(t *testing.T) {
	if got := pathUserID("/Users/d132bb76-e134-48ab-bbdc-b55e542170bd/Items"); got != "d132bb76-e134-48ab-bbdc-b55e542170bd" {
		t.Fatalf("got %q", got)
	}
	if pathUserID("/Users/AuthenticateByName") != "" || pathUserID("/Users/Me") != "" {
		t.Fatal("non-guid users path")
	}
}

func TestLoginFailsOverAfterTimeout(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-b",
			"User":        map[string]string{"Id": "u-b", "Name": "ada"},
		})
	}))
	t.Cleanup(ok.Close)
	h, st, mon := testProxy(t, "fail_closed", dead, ok)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindDevice(context.Background(), "dev-1", "server-a", "Delfin"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser Client="Delfin", DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "issued-b") {
		t.Fatalf("want failover 200, got %d %s", rec.Code, rec.Body.String())
	}
	row, err := st.LookupToken(req.Context(), "issued-b")
	if err != nil || row == nil || row.Backend != "server-b" {
		t.Fatalf("token bound %+v err=%v", row, err)
	}
}

func TestLoginFailsOverAfter401(t *testing.T) {
	var hitA, hitB int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitA++
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(a.Close)
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitB++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"AccessToken": "issued-b",
			"User":        map[string]string{"Id": "u-b", "Name": "ada"},
		})
	}))
	t.Cleanup(b.Close)
	h, st, mon := testProxy(t, "fail_closed", a, b)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	if err := st.BindDevice(context.Background(), "dev-1", "server-a", "Delfin"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(`{"Username":"ada","Pw":"x"}`))
	req.Header.Set("Authorization", `MediaBrowser Client="Delfin", DeviceId="dev-1"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hitA != 1 || hitB != 1 {
		t.Fatalf("code=%d A=%d B=%d body=%s", rec.Code, hitA, hitB, rec.Body.String())
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
	for _, path := range []string{"/hap/users", "/hap/health", "/hap/cache", "/metrics"} {
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
	srv := status.New(h.cfg, st, mon, router.New(h.cfg, st, mon), h.Cache(), h.Library(), func() any { return h.PerfSnapshot() })
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

func TestImageCacheDisabledHitsBackendTwice(t *testing.T) {
	var hits int
	a := imageBackend(t, &hits, "image/png", "public", []byte("png-bytes"))
	h, _, mon := testProxy(t, "fail_closed", a)
	mon.SetState("server-a", health.StateHealthy)
	getImage(t, h, "/Items/abc/Images/Primary")
	getImage(t, h, "/Items/abc/Images/Primary")
	if hits != 2 {
		t.Fatalf("disabled cache should hop twice, hits=%d", hits)
	}
}

func TestImageCacheHitSkipsBackend(t *testing.T) {
	var hits int
	a := imageBackend(t, &hits, "image/png", "public", []byte("png-bytes"))
	h, _, mon := testProxyCached(t, "fail_closed", cacheCfg(1<<20, 1<<20), a)
	mon.SetState("server-a", health.StateHealthy)
	rec1 := getImage(t, h, "/Items/abc/Images/Primary")
	if rec1.Body.String() != "png-bytes" {
		t.Fatalf("body=%q", rec1.Body.String())
	}
	found := false
	for _, c := range rec1.Result().Cookies() {
		if c.Name == "hap_backend" && c.Value == "server-a" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing cookie on miss")
	}
	rec2 := getImage(t, h, "/Items/abc/Images/Primary")
	if rec2.Body.String() != "png-bytes" {
		t.Fatalf("hit body=%q", rec2.Body.String())
	}
	found = false
	for _, c := range rec2.Result().Cookies() {
		if c.Name == "hap_backend" && c.Value == "server-a" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing cookie on hit")
	}
	if hits != 1 {
		t.Fatalf("want 1 hop, got %d", hits)
	}
}

func TestImageCacheSeparatedByBackendPin(t *testing.T) {
	var hitA, hitB int
	a := imageBackend(t, &hitA, "image/png", "public", []byte("aaa"))
	b := imageBackend(t, &hitB, "image/png", "public", []byte("bbb"))
	h, st, mon := testProxyCached(t, "fail_closed", cacheCfg(1<<20, 1<<20), a, b)
	mon.SetState("server-a", health.StateHealthy)
	mon.SetState("server-b", health.StateHealthy)
	ctx := context.Background()
	if err := st.BindToken(ctx, "tok-a", "server-a", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindToken(ctx, "tok-b", "server-b", store.TokenRow{}); err != nil {
		t.Fatal(err)
	}
	recA := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodGet, "/Items/abc/Images/Primary", nil)
	reqA.Header.Set("Authorization", `MediaBrowser Token="tok-a"`)
	h.ServeHTTP(recA, reqA)
	recB := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodGet, "/Items/abc/Images/Primary", nil)
	reqB.Header.Set("Authorization", `MediaBrowser Token="tok-b"`)
	h.ServeHTTP(recB, reqB)
	if recA.Body.String() != "aaa" || recB.Body.String() != "bbb" {
		t.Fatalf("a=%q b=%q", recA.Body.String(), recB.Body.String())
	}
	getPinnedImage(t, h, "tok-a")
	getPinnedImage(t, h, "tok-b")
	if hitA != 1 || hitB != 1 {
		t.Fatalf("shared cache? A=%d B=%d", hitA, hitB)
	}
}

func TestImageCacheSkipsUserAvatarAndNoStore(t *testing.T) {
	var hits int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("img"))
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxyCached(t, "fail_closed", cacheCfg(1<<20, 1<<20), a)
	mon.SetState("server-a", health.StateHealthy)
	getImage(t, h, "/Users/u1/Images/Primary")
	getImage(t, h, "/Users/u1/Images/Primary")
	if hits != 2 {
		t.Fatalf("user images must not cache, hits=%d", hits)
	}
	hits = 0
	getImage(t, h, "/Items/abc/Images/Primary")
	getImage(t, h, "/Items/abc/Images/Primary")
	if hits != 2 {
		t.Fatalf("no-store must not cache, hits=%d", hits)
	}
}

func TestImageCacheSkipsOversize(t *testing.T) {
	var hits int
	body := bytes.Repeat([]byte("x"), 64)
	a := imageBackend(t, &hits, "image/png", "public", body)
	h, _, mon := testProxyCached(t, "fail_closed", cacheCfg(1024, 32), a)
	mon.SetState("server-a", health.StateHealthy)
	getImage(t, h, "/Items/abc/Images/Primary")
	getImage(t, h, "/Items/abc/Images/Primary")
	if hits != 2 {
		t.Fatalf("oversize must not cache, hits=%d", hits)
	}
}

func TestImageCacheNotModified(t *testing.T) {
	var hits int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public")
		w.Header().Set("Last-Modified", "Sat, 04 Jul 2026 23:48:51 GMT")
		_, _ = w.Write([]byte("png"))
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxyCached(t, "fail_closed", cacheCfg(1<<20, 1<<20), a)
	mon.SetState("server-a", health.StateHealthy)
	getImage(t, h, "/Items/abc/Images/Primary")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/abc/Images/Primary", nil)
	req.Header.Set("If-Modified-Since", "Sat, 04 Jul 2026 23:48:51 GMT")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("want 304 got %d %s", rec.Code, rec.Body.String())
	}
	if hits != 1 {
		t.Fatalf("hits=%d", hits)
	}
}

func TestImageCacheCapacityEvictsCold(t *testing.T) {
	var hits int
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public")
		_, _ = w.Write([]byte("1234567890")) // 10 bytes
	}))
	t.Cleanup(a.Close)
	h, _, mon := testProxyCached(t, "fail_closed", cacheCfg(25, 15), a)
	mon.SetState("server-a", health.StateHealthy)
	getImage(t, h, "/Items/cold/Images/Primary")
	getImage(t, h, "/Items/hot/Images/Primary")
	getImage(t, h, "/Items/hot/Images/Primary") // hit, stay MRU
	getImage(t, h, "/Items/new/Images/Primary")
	hotBefore := hits
	getImage(t, h, "/Items/hot/Images/Primary")
	if hits != hotBefore {
		t.Fatalf("hot should stay cached, hits %d -> %d", hotBefore, hits)
	}
	getImage(t, h, "/Items/cold/Images/Primary")
	if hits != hotBefore+1 {
		t.Fatalf("cold should have been evicted, hits=%d want %d", hits, hotBefore+1)
	}
}

func TestStatusCacheEndpoint(t *testing.T) {
	a := imageBackend(t, new(int), "image/png", "public", []byte("png"))
	h, st, mon := testProxyCached(t, "fail_closed", cacheCfg(1<<20, 1<<20), a)
	mon.SetState("server-a", health.StateHealthy)
	getImage(t, h, "/Items/abc/Images/Primary")
	srv := status.New(h.cfg, st, mon, router.New(h.cfg, st, mon), h.Cache(), h.Library(), func() any { return h.PerfSnapshot() })
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hap/cache", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
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

func getImage(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotModified {
		t.Fatalf("%s status %d %s", path, rec.Code, rec.Body.String())
	}
	return rec
}

func getPinnedImage(t *testing.T, h *Handler, token string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/Items/abc/Images/Primary", nil)
	req.Header.Set("Authorization", `MediaBrowser Token="`+token+`"`)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pinned %s %d %s", token, rec.Code, rec.Body.String())
	}
}

func imageBackend(t *testing.T, hits *int, contentType, cacheControl string, body []byte) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", cacheControl)
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func boolPtr(v bool) *bool { return &v }

func cacheCfg(maxBytes, maxObject int64) config.Cache {
	return config.Cache{
		Enabled:    boolPtr(true),
		MaxBytes:   maxBytes,
		MaxObject:  maxObject,
		DefaultTTL: time.Hour,
		MaxTTL:     time.Hour,
	}
}

func testProxy(t *testing.T, policy string, servers ...*httptest.Server) (*Handler, store.Store, *health.Monitor) {
	t.Helper()
	return testProxyHop(t, policy, 5*time.Second, config.Cache{}, servers...)
}

func testProxyCached(t *testing.T, policy string, cache config.Cache, servers ...*httptest.Server) (*Handler, store.Store, *health.Monitor) {
	t.Helper()
	return testProxyHop(t, policy, 5*time.Second, cache, servers...)
}

func testProxyHop(t *testing.T, policy string, hop time.Duration, cache config.Cache, servers ...*httptest.Server) (*Handler, store.Store, *health.Monitor) {
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
		Performance: config.Performance{Cache: cache, AuthTimeout: hop},
	}
	names := []string{"server-a", "server-b", "server-c"}
	for i, s := range servers {
		cfg.Backends = append(cfg.Backends, config.Backend{
			Name:    names[i],
			URL:     s.URL,
			Timeout: hop,
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
