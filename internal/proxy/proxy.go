package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/imevul/hapudding/internal/authheader"
	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	reqCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hap_requests_total",
		Help: "Proxied and HAP-synthesized requests",
	}, []string{"backend", "result"})
	backendState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hap_backend_state",
		Help: "0 unknown, 1 healthy, 2 degraded, 3 unhealthy",
	}, []string{"backend"})
	bindCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hap_bindings",
		Help: "Affinity binding counts (hashes only)",
	}, []string{"backend", "kind"})
)

type Handler struct {
	cfg *config.Config
	rt  *router.Router
	st  store.Store
	mon *health.Monitor
	log *slog.Logger
}

func New(cfg *config.Config, rt *router.Router, st store.Store, mon *health.Monitor, log *slog.Logger) *Handler {
	return &Handler{cfg: cfg, rt: rt, st: st, mon: mon, log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isStatusPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	id := authheader.Parse(r)
	d := h.rt.Decide(ctx, r, id)

	if d.DropClient {
		_ = h.st.DeleteClient(ctx, id.Token, id.DeviceID)
	}
	if d.HAPError != "" {
		reqCount.WithLabelValues(nameOf(d.Backend), "hap_"+d.HAPError).Inc()
		writeHAP(w, d.HAPStatus, d.HAPError, nameOf(d.Backend))
		return
	}
	if d.Backend == nil {
		reqCount.WithLabelValues("", "hap_no_eligible_backend").Inc()
		writeHAP(w, http.StatusServiceUnavailable, "no_eligible_backend", "")
		return
	}

	anonKey := store.HashAnon(clientIP(r), r.UserAgent())
	if id.DeviceID != "" {
		_ = h.st.BindDevice(ctx, id.DeviceID, d.Backend.Name, id.Client)
	} else {
		_ = h.st.BindAnon(ctx, anonKey, d.Backend.Name)
	}
	if id.Token != "" {
		_ = h.st.BindToken(ctx, id.Token, d.Backend.Name, store.TokenRow{
			DeviceID: id.DeviceID, Client: id.Client, Device: id.Device, Version: id.Version,
		})
	}

	h.proxy(w, r, d.Backend, id, d.Graylisted)
}

func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, b *config.Backend, id authheader.Identifiers, graylisted bool) {
	target, err := url.Parse(strings.TrimRight(b.URL, "/") + "/")
	if err != nil {
		writeHAP(w, http.StatusBadGateway, "bad_backend_url", b.Name)
		return
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	if tr := h.mon.RoundTripper(b.Name, graylisted); tr != nil {
		rp.Transport = tr
	}
	peek := isLoginPath(r.Method, r.URL.Path)
	logout := isLogoutPath(r.Method, r.URL.Path)

	rp.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = singleJoiningSlash(strings.TrimRight(target.Path, "/"), req.URL.Path)
		if target.RawQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = target.RawQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = target.RawQuery + "&" + req.URL.RawQuery
		}
		if b.Host != "" {
			req.Host = b.Host
		} else {
			req.Host = target.Host
		}
		if xf := req.Header.Get("X-Forwarded-For"); xf == "" {
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				req.Header.Set("X-Forwarded-For", host)
			}
		}
		if req.Header.Get("X-Forwarded-Proto") == "" {
			if r.TLS != nil {
				req.Header.Set("X-Forwarded-Proto", "https")
			} else {
				req.Header.Set("X-Forwarded-Proto", "http")
			}
		}
		for k, v := range b.Headers {
			req.Header.Set(k, v)
		}
		if graylisted {
			req.Header.Set("Connection", "close")
		}
	}
	rp.ModifyResponse = func(res *http.Response) error {
		if peek && res.StatusCode >= 200 && res.StatusCode < 300 {
			h.peekLogin(res, b.Name, id)
		}
		if logout && res.StatusCode >= 200 && res.StatusCode < 300 && id.Token != "" {
			_ = h.st.DeleteToken(res.Request.Context(), id.Token)
		}
		if isUserPath(res.Request.URL.Path) && res.StatusCode >= 500 {
			h.mon.RecordAuthFailure(b.Name)
		}
		_ = h.st.TouchToken(res.Request.Context(), id.Token, res.Request.Method, res.Request.URL.Path, res.StatusCode)
		_ = h.st.TouchDevice(res.Request.Context(), id.DeviceID)
		result := "proxied"
		if res.StatusCode >= 500 {
			result = "backend_5xx"
		} else if res.StatusCode >= 400 {
			result = "backend_4xx"
		}
		reqCount.WithLabelValues(b.Name, result).Inc()
		h.log.Info("proxy", "backend", b.Name, "status", res.StatusCode, "path", res.Request.URL.Path, "method", res.Request.Method)
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.log.Error("backend unreachable", "backend", b.Name, "err", err, "path", r.URL.Path)
		reqCount.WithLabelValues(b.Name, "hap_backend_unreachable").Inc()
		writeHAP(w, http.StatusServiceUnavailable, "backend_unreachable", b.Name)
	}
	if graylisted {
		rp.FlushInterval = -1
	} else {
		rp.FlushInterval = 100 * time.Millisecond
	}
	rp.ServeHTTP(w, r)
}

type loginJSON struct {
	AccessToken string `json:"AccessToken"`
	ServerID    string `json:"ServerId"`
	User        *struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	} `json:"User"`
	SessionInfo *struct {
		DeviceID string `json:"DeviceId"`
	} `json:"SessionInfo"`
}

func (h *Handler) peekLogin(res *http.Response, backend string, id authheader.Identifiers) {
	if res.Body == nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	_ = res.Body.Close()
	if err != nil {
		res.Body = io.NopCloser(bytes.NewReader(nil))
		return
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	res.ContentLength = int64(len(raw))
	var body loginJSON
	if json.Unmarshal(raw, &body) != nil || body.AccessToken == "" {
		return
	}
	meta := store.TokenRow{
		DeviceID: id.DeviceID,
		Client:   id.Client,
		Device:   id.Device,
		Version:  id.Version,
	}
	if body.User != nil {
		meta.UserID = body.User.ID
		meta.Username = body.User.Name
	}
	if body.SessionInfo != nil && body.SessionInfo.DeviceID != "" {
		meta.DeviceID = body.SessionInfo.DeviceID
	}
	ctx := context.Background()
	_ = h.st.BindToken(ctx, body.AccessToken, backend, meta)
	if meta.DeviceID != "" {
		_ = h.st.BindDevice(ctx, meta.DeviceID, backend, meta.Client)
	}
}

func writeHAP(w http.ResponseWriter, status int, code, backend string) {
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-HAP-Error", code)
	if backend != "" {
		w.Header().Set("X-HAP-Backend", backend)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"backend": backend,
	})
}

func isLoginPath(method, path string) bool {
	if !strings.EqualFold(method, http.MethodPost) {
		return false
	}
	p := strings.ToLower(path)
	return strings.HasSuffix(p, "/users/authenticatebyname") ||
		strings.HasSuffix(p, "/users/authenticatewithquickconnect") ||
		strings.Contains(p, "/quickconnect/connect")
}

func isLogoutPath(method, path string) bool {
	if !strings.EqualFold(method, http.MethodPost) && !strings.EqualFold(method, http.MethodDelete) {
		return false
	}
	p := strings.ToLower(path)
	return strings.HasSuffix(p, "/sessions/logout") || strings.Contains(p, "/sessions/logout")
}

func isUserPath(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "/users/") || strings.Contains(p, "/sessions/")
}

func isStatusPath(path string) bool {
	p := strings.ToLower(path)
	return strings.HasPrefix(p, "/hap/") || p == "/metrics"
}

func nameOf(b *config.Backend) string {
	if b == nil {
		return ""
	}
	return b.Name
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	return a + b
}

func ObserveStates(mon *health.Monitor) {
	for _, s := range mon.All() {
		var n float64
		switch s.State {
		case health.StateHealthy:
			n = 1
		case health.StateDegraded:
			n = 2
		case health.StateUnhealthy:
			n = 3
		}
		backendState.WithLabelValues(s.Name).Set(n)
	}
}

func ObserveBinds(counts map[string]store.Counts) {
	for name, c := range counts {
		bindCount.WithLabelValues(name, "token").Set(float64(c.Tokens))
		bindCount.WithLabelValues(name, "device").Set(float64(c.Devices))
		bindCount.WithLabelValues(name, "anon").Set(float64(c.Anons))
	}
}
