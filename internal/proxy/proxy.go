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
	"strconv"
	"strings"
	"time"

	"github.com/imevul/hapudding/internal/authheader"
	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/imgcache"
	"github.com/imevul/hapudding/internal/libcache"
	"github.com/imevul/hapudding/internal/router"
	"github.com/imevul/hapudding/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/sync/singleflight"
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
	cfg            *config.Config
	rt             *router.Router
	st             store.Store
	mon            *health.Monitor
	log            *slog.Logger
	cache          *imgcache.Cache
	lib            *libcache.Cache
	flight         singleflight.Group
	libSem         *libSem
	coalesceSolo   int64
	coalesceShared int64
}

func New(cfg *config.Config, rt *router.Router, st store.Store, mon *health.Monitor, log *slog.Logger) *Handler {
	h := &Handler{cfg: cfg, rt: rt, st: st, mon: mon, log: log}
	if cfg != nil && cfg.Performance.CacheEnabled() {
		h.cache = imgcache.New(cfg.Performance.Cache)
	}
	if cfg != nil && cfg.Performance.LibraryEnabled() {
		h.lib = libcache.New(cfg.Performance.Library)
	}
	if cfg != nil && cfg.Performance.LibraryConcurrencyEnabled() {
		h.libSem = newLibSem(cfg.Performance.LibraryConcurrency.Max)
	}
	return h
}

func (h *Handler) Cache() *imgcache.Cache   { return h.cache }
func (h *Handler) Library() *libcache.Cache { return h.lib }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isStatusPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if err := acceptExpectContinue(r); err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
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

	if isLoginPath(r.Method, r.URL.Path) {
		h.log.Info("request", "backend", d.Backend.Name, "path", r.URL.Path, "method", r.Method, "graylisted", d.Graylisted)
		h.login(w, r, d, id)
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
			UserID: pathUserID(r.URL.Path), DeviceID: id.DeviceID, Client: id.Client, Device: id.Device, Version: id.Version,
		})
		_ = h.st.BindAnon(ctx, store.HashSessionIP(clientIP(r)), d.Backend.Name)
	}

	h.log.Info("request", "backend", d.Backend.Name, "path", r.URL.Path, "method", r.Method, "graylisted", d.Graylisted)
	h.proxy(w, r, d.Backend, id, d.Graylisted)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request, d router.Decision, id authheader.Identifiers) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if r.Body != nil {
		_ = r.Body.Close()
	}
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}
	cands := h.rt.LoginCandidates(h.loginPreferred(d, body))
	if len(cands) == 0 {
		writeHAP(w, http.StatusServiceUnavailable, "no_eligible_backend", "")
		return
	}
	var lastStatus int
	for i, b := range cands {
		h.log.Info("request", "backend", b.Name, "path", r.URL.Path, "method", r.Method, "login_try", i+1)
		res, err := h.loginHop(r, body, b, d.Graylisted)
		if err != nil {
			h.log.Error("backend unreachable", "backend", b.Name, "err", err, "path", r.URL.Path)
			reqCount.WithLabelValues(b.Name, "hap_backend_unreachable").Inc()
			h.mon.RecordAuthFailure(b.Name)
			continue
		}
		lastStatus = res.StatusCode
		if res.StatusCode == http.StatusUnauthorized && i < len(cands)-1 {
			h.log.Info("login_retry", "backend", b.Name, "status", res.StatusCode, "next", cands[i+1].Name)
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			reqCount.WithLabelValues(b.Name, "backend_4xx").Inc()
			continue
		}
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			token, userID := h.peekLogin(res, b.Name, id, clientIP(r))
			h.scheduleWarmLogin(b, token, userID)
		}
		if res.StatusCode >= 500 {
			h.mon.RecordAuthFailure(b.Name)
		}
		setBackendCookie(res, r, b.Name, h.cfg.Affinity.DeviceTTL)
		result := "proxied"
		if res.StatusCode >= 500 {
			result = "backend_5xx"
		} else if res.StatusCode >= 400 {
			result = "backend_4xx"
		}
		reqCount.WithLabelValues(b.Name, result).Inc()
		h.log.Info("proxy", "backend", b.Name, "status", res.StatusCode, "path", r.URL.Path, "method", r.Method)
		h.rewriteProxiedResponse(res, r)
		copyResponse(w, res)
		return
	}
	if lastStatus == http.StatusUnauthorized {
		writeHAP(w, http.StatusUnauthorized, "login_failed", nameOf(d.Backend))
		return
	}
	writeHAP(w, http.StatusServiceUnavailable, "backend_unreachable", nameOf(d.Backend))
}

func (h *Handler) loginHop(r *http.Request, body []byte, b *config.Backend, graylisted bool) (*http.Response, error) {
	timeout := 60 * time.Second
	if h.cfg != nil && h.cfg.Performance.AuthTimeout > 0 {
		timeout = h.cfg.Performance.AuthTimeout
	}
	b2 := *b
	b2.Timeout = timeout
	tr, err := health.HopTransport(b2)
	if err != nil {
		return nil, err
	}
	if graylisted {
		tr.DisableKeepAlives = true
	}
	c := &http.Client{Timeout: timeout, Transport: tr}
	u := strings.TrimRight(b.URL, "/") + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = r.Header.Clone()
	sanitizeLoginHopHeaders(req, len(body))
	applyForwardedOrigin(req, r, b, h.cfg != nil && h.cfg.StayOnOriginEnabled())
	for k, v := range b.Headers {
		req.Header.Set(k, v)
	}
	if graylisted {
		req.Header.Set("Connection", "close")
	}
	return c.Do(req)
}

var loginHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func sanitizeLoginHopHeaders(req *http.Request, bodyLen int) {
	if req == nil {
		return
	}
	req.Header.Del("Expect")
	for _, h := range loginHopHeaders {
		req.Header.Del(h)
	}
	req.TransferEncoding = nil
	req.ContentLength = int64(bodyLen)
	req.Header.Set("Content-Length", strconv.Itoa(bodyLen))
}

func copyResponse(w http.ResponseWriter, res *http.Response) {
	defer res.Body.Close()
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, b *config.Backend, id authheader.Identifiers, graylisted bool) {
	if h.serveCached(w, r, b, id) {
		return
	}
	if h.serveLibraryCached(w, r, b, id) {
		return
	}
	if h.shouldCoalesce(r) {
		h.proxyCoalesced(w, r, b, id, graylisted)
		return
	}
	if err := h.acquireLibrary(r.Context(), r, b.Name); err != nil {
		h.log.Error("backend unreachable", "backend", b.Name, "err", err, "path", r.URL.Path)
		reqCount.WithLabelValues(b.Name, "hap_backend_unreachable").Inc()
		writeHAP(w, http.StatusServiceUnavailable, "backend_unreachable", b.Name)
		return
	}
	defer h.releaseLibrary(r, b.Name)
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
	stay := h.cfg != nil && h.cfg.StayOnOriginEnabled()

	if stay {
		rp.Director = nil
		rp.Rewrite = func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			applyForwardedOrigin(pr.Out, pr.In, b, true)
			for k, v := range b.Headers {
				pr.Out.Header.Set(k, v)
			}
			if graylisted {
				pr.Out.Header.Set("Connection", "close")
			}
			pr.Out.Header.Del("Expect")
		}
	} else {
		rp.Director = func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = singleJoiningSlash(strings.TrimRight(target.Path, "/"), req.URL.Path)
			if target.RawQuery == "" || req.URL.RawQuery == "" {
				req.URL.RawQuery = target.RawQuery + req.URL.RawQuery
			} else {
				req.URL.RawQuery = target.RawQuery + "&" + req.URL.RawQuery
			}
			applyForwardedOrigin(req, r, b, false)
			for k, v := range b.Headers {
				req.Header.Set(k, v)
			}
			if graylisted {
				req.Header.Set("Connection", "close")
			}
			req.Header.Del("Expect")
		}
	}
	rp.ModifyResponse = func(res *http.Response) error {
		if peek && res.StatusCode >= 200 && res.StatusCode < 300 {
			h.peekLogin(res, b.Name, id, clientIP(r))
		}
		h.rewriteProxiedResponse(res, r)
		h.maybeStoreImage(res, r, b.Name)
		h.maybeStoreLibrary(res, r, b.Name, id)
		h.maybeInvalidateLibrary(res, r, b.Name, id)
		setBackendCookie(res, r, b.Name, h.cfg.Affinity.DeviceTTL)
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

func (h *Handler) serveCached(w http.ResponseWriter, r *http.Request, b *config.Backend, id authheader.Identifiers) bool {
	if h.cache == nil || !imgcache.IsCacheableRequest(r.Method, r.URL.Path) {
		return false
	}
	key := imgcache.Key(b.Name, r.URL.Path, r.URL.RawQuery, r.Header.Get("Accept"))
	ent := h.cache.Get(key)
	if ent == nil {
		return false
	}
	status := http.StatusOK
	if imgcache.FreshFor(ent, r) {
		status = http.StatusNotModified
	}
	_ = h.st.TouchToken(r.Context(), id.Token, r.Method, r.URL.Path, status)
	_ = h.st.TouchDevice(r.Context(), id.DeviceID)
	for k, vs := range ent.Header {
		if skipCachedHeader(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	age := int64(time.Since(ent.StoredAt).Seconds())
	if age < 0 {
		age = 0
	}
	w.Header().Set("Age", strconv.FormatInt(age, 10))
	http.SetCookie(w, backendCookie(r, b.Name, h.cfg.Affinity.DeviceTTL))
	reqCount.WithLabelValues(b.Name, "cache_hit").Inc()
	h.log.Info("proxy", "backend", b.Name, "status", status, "path", r.URL.Path, "method", r.Method, "cache", "hit")
	w.WriteHeader(status)
	if status != http.StatusNotModified && !strings.EqualFold(r.Method, http.MethodHead) {
		_, _ = w.Write(ent.Body)
	}
	return true
}

func (h *Handler) maybeStoreImage(res *http.Response, r *http.Request, backend string) {
	if h.cache == nil || res == nil || res.Request == nil {
		return
	}
	path := res.Request.URL.Path
	if !imgcache.IsCacheableRequest(res.Request.Method, path) {
		return
	}
	if res.StatusCode != http.StatusOK {
		return
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "image/") {
		return
	}
	ttl, ok := imgcache.StoreTTL(res.Header.Get("Cache-Control"), h.cache.DefaultTTL(), h.cache.MaxTTL())
	if !ok {
		return
	}
	max := h.cache.MaxObject()
	if res.ContentLength > max {
		return
	}
	limited := io.LimitReader(res.Body, max+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		res.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), res.Body))
		return
	}
	if int64(len(raw)) > max {
		res.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), res.Body))
		return
	}
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(raw))
	res.ContentLength = int64(len(raw))
	hdr := res.Header.Clone()
	hdr.Del("Set-Cookie")
	ent := &imgcache.Entry{
		Backend:      backend,
		Status:       res.StatusCode,
		Header:       hdr,
		Body:         raw,
		ETag:         res.Header.Get("ETag"),
		LastModified: res.Header.Get("Last-Modified"),
		ContentType:  ct,
		ExpiresAt:    time.Now().Add(ttl),
	}
	key := imgcache.Key(backend, path, res.Request.URL.RawQuery, r.Header.Get("Accept"))
	_ = h.cache.Put(key, ent)
}

func skipCachedHeader(k string) bool {
	switch strings.ToLower(k) {
	case "set-cookie", "connection", "keep-alive", "transfer-encoding", "proxy-connection", "age":
		return true
	}
	return false
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

func (h *Handler) peekLogin(res *http.Response, backend string, id authheader.Identifiers, clientIP string) (token, userID string) {
	if res.Body == nil {
		return "", ""
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	_ = res.Body.Close()
	if err != nil {
		res.Body = io.NopCloser(bytes.NewReader(nil))
		return "", ""
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	res.ContentLength = int64(len(raw))
	var body loginJSON
	if json.Unmarshal(raw, &body) != nil || body.AccessToken == "" {
		return "", ""
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
	if clientIP != "" {
		_ = h.st.BindAnon(ctx, store.HashSessionIP(clientIP), backend)
	}
	if body.User != nil {
		return body.AccessToken, body.User.ID
	}
	return body.AccessToken, ""
}

func setBackendCookie(res *http.Response, req *http.Request, backend string, ttl time.Duration) {
	if res == nil || backend == "" {
		return
	}
	res.Header.Add("Set-Cookie", backendCookie(req, backend, ttl).String())
}

func backendCookie(req *http.Request, backend string, ttl time.Duration) *http.Cookie {
	maxAge := 0
	if ttl > 0 {
		maxAge = int(ttl.Seconds())
	}
	c := &http.Cookie{
		Name:     "hap_backend",
		Value:    backend,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if req != nil && (req.TLS != nil || strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https")) {
		c.Secure = true
	}
	return c
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

// acceptExpectContinue reads a small body so net/http sends 100 Continue
// before the hop is dialed. libsoup/Delfin otherwise waits for 100 while
// ReverseProxy waits for the body (and ResponseHeaderTimeout never starts).
func acceptExpectContinue(r *http.Request) error {
	if r == nil || !strings.Contains(strings.ToLower(r.Header.Get("Expect")), "100-continue") {
		return nil
	}
	r.Header.Del("Expect")
	if r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	_ = r.Body.Close()
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	r.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	return nil
}

func (h *Handler) loginPreferred(d router.Decision, body []byte) string {
	preferred := nameOf(d.Backend)
	if d.Kind == store.KindToken {
		return preferred
	}
	if h.cfg == nil {
		return preferred
	}
	if want := h.cfg.Affinity.PreferredBackend(loginUsername(body)); want != "" {
		return want
	}
	return preferred
}

func pathUserID(path string) string {
	p := path
	const prefix = "/users/"
	if !strings.HasPrefix(strings.ToLower(p), prefix) {
		return ""
	}
	rest := p[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	if !isGUID(rest) {
		return ""
	}
	return rest
}

func isGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			hex := c >= '0' && c <= '9' || c >= 'A' && c <= 'F' || c >= 'a' && c <= 'f'
			if !hex {
				return false
			}
		}
	}
	return true
}

func loginUsername(body []byte) string {
	var v struct {
		Username string `json:"Username"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	return strings.TrimSpace(v.Username)
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
