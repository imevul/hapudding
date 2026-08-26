package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/imevul/hapudding/internal/authheader"
	"github.com/imevul/hapudding/internal/config"
	"github.com/imevul/hapudding/internal/health"
	"github.com/imevul/hapudding/internal/imgcache"
	"github.com/imevul/hapudding/internal/libcache"
	"github.com/imevul/hapudding/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	coalesceCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hap_coalesce_total",
		Help: "Coalesced image/library hops",
	}, []string{"backend", "shared"})
	libInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "hap_library_inflight",
		Help: "In-flight library hops per backend (after the concurrency cap)",
	}, []string{"backend"})
)

type hopBuf struct {
	Status int
	Header http.Header
	Body   []byte
}

type PerfSnapshot struct {
	AuthTimeout        string          `json:"authTimeout"`
	Images             imgcache.Stats  `json:"images"`
	Library            libcache.Stats  `json:"library"`
	Coalesce           CoalesceStats   `json:"coalesce"`
	LibraryConcurrency ConcurrencySnap `json:"libraryConcurrency"`
}

type CoalesceStats struct {
	Enabled bool  `json:"enabled"`
	Solo    int64 `json:"solo"`
	Shared  int64 `json:"shared"`
}

type ConcurrencySnap struct {
	Enabled  bool           `json:"enabled"`
	Max      int            `json:"max"`
	Inflight map[string]int `json:"inflight,omitempty"`
}

func (h *Handler) PerfSnapshot() PerfSnapshot {
	p := PerfSnapshot{}
	if h.cfg != nil {
		p.AuthTimeout = h.cfg.Performance.AuthTimeout.String()
		p.Coalesce.Enabled = h.cfg.Performance.CoalesceEnabled()
		p.LibraryConcurrency.Enabled = h.cfg.Performance.LibraryConcurrencyEnabled()
		p.LibraryConcurrency.Max = h.cfg.Performance.LibraryConcurrency.Max
	}
	if h.cache != nil {
		p.Images = h.cache.Stats()
	}
	if h.lib != nil {
		p.Library = h.lib.Stats()
	}
	p.Coalesce.Solo = atomic.LoadInt64(&h.coalesceSolo)
	p.Coalesce.Shared = atomic.LoadInt64(&h.coalesceShared)
	if h.libSem != nil {
		p.LibraryConcurrency.Inflight = h.libSem.inflight()
	}
	return p
}

func (h *Handler) serveLibraryCached(w http.ResponseWriter, r *http.Request, b *config.Backend, id authheader.Identifiers) bool {
	if h.lib == nil || id.Token == "" || !libcache.IsLibraryRequest(r.Method, r.URL.Path) {
		return false
	}
	key := libcache.Key(b.Name, store.HashToken(id.Token), r.Method, r.URL.Path, r.URL.RawQuery)
	ent := h.lib.Get(key)
	if ent == nil {
		return false
	}
	_ = h.st.TouchToken(r.Context(), id.Token, r.Method, r.URL.Path, ent.Status)
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
	reqCount.WithLabelValues(b.Name, "library_hit").Inc()
	h.log.Info("proxy", "backend", b.Name, "status", ent.Status, "path", r.URL.Path, "method", r.Method, "cache", "library")
	w.WriteHeader(ent.Status)
	if !strings.EqualFold(r.Method, http.MethodHead) {
		_, _ = w.Write(ent.Body)
	}
	return true
}

func (h *Handler) maybeStoreLibrary(res *http.Response, r *http.Request, backend string, id authheader.Identifiers) {
	if h.lib == nil || res == nil || res.Request == nil || id.Token == "" {
		return
	}
	path := res.Request.URL.Path
	if !libcache.IsLibraryRequest(res.Request.Method, path) {
		return
	}
	if res.StatusCode != http.StatusOK {
		return
	}
	ct := strings.ToLower(res.Header.Get("Content-Type"))
	if !strings.Contains(ct, "json") {
		return
	}
	max := h.lib.MaxObject()
	if res.ContentLength > max && res.ContentLength > 0 {
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
	ent := &libcache.Entry{
		Backend:     backend,
		Status:      res.StatusCode,
		Header:      hdr,
		Body:        raw,
		ContentType: ct,
		ExpiresAt:   time.Now().Add(h.lib.TTL()),
	}
	key := libcache.Key(backend, store.HashToken(id.Token), res.Request.Method, path, res.Request.URL.RawQuery)
	_ = h.lib.Put(key, ent)
}

func (h *Handler) maybeInvalidateLibrary(res *http.Response, r *http.Request, backend string, id authheader.Identifiers) {
	if h.lib == nil || res == nil || id.Token == "" {
		return
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return
	}
	if !libcache.IsInvalidateRequest(r.Method, r.URL.Path) {
		return
	}
	h.lib.DropToken(backend, store.HashToken(id.Token))
}

func (h *Handler) shouldCoalesce(r *http.Request) bool {
	if h.cfg == nil || !h.cfg.Performance.CoalesceEnabled() {
		return false
	}
	return imgcache.IsCacheableRequest(r.Method, r.URL.Path) || libcache.IsLibraryRequest(r.Method, r.URL.Path)
}

func (h *Handler) coalesceKey(r *http.Request, b *config.Backend, id authheader.Identifiers) string {
	if libcache.IsLibraryRequest(r.Method, r.URL.Path) {
		return libcache.Key(b.Name, store.HashToken(id.Token), r.Method, r.URL.Path, r.URL.RawQuery)
	}
	return imgcache.Key(b.Name, r.URL.Path, r.URL.RawQuery, r.Header.Get("Accept"))
}

func (h *Handler) proxyCoalesced(w http.ResponseWriter, r *http.Request, b *config.Backend, id authheader.Identifiers, graylisted bool) {
	key := h.coalesceKey(r, b, id)
	v, err, shared := h.flight.Do(key, func() (any, error) {
		if err := h.acquireLibrary(r.Context(), r, b.Name); err != nil {
			return nil, err
		}
		defer h.releaseLibrary(r, b.Name)
		res, err := h.hopOnce(r, b, graylisted)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		max := h.coalesceReadLimit(r)
		raw, err := io.ReadAll(io.LimitReader(res.Body, max))
		if err != nil {
			return nil, err
		}
		res.Body = io.NopCloser(bytes.NewReader(raw))
		res.ContentLength = int64(len(raw))
		h.maybeStoreImage(res, r, b.Name)
		h.maybeStoreLibrary(res, r, b.Name, id)
		hdr := res.Header.Clone()
		return &hopBuf{Status: res.StatusCode, Header: hdr, Body: raw}, nil
	})
	if shared {
		atomic.AddInt64(&h.coalesceShared, 1)
		coalesceCount.WithLabelValues(b.Name, "true").Inc()
	} else {
		atomic.AddInt64(&h.coalesceSolo, 1)
		coalesceCount.WithLabelValues(b.Name, "false").Inc()
	}
	if err != nil {
		h.log.Error("backend unreachable", "backend", b.Name, "err", err, "path", r.URL.Path)
		reqCount.WithLabelValues(b.Name, "hap_backend_unreachable").Inc()
		writeHAP(w, http.StatusServiceUnavailable, "backend_unreachable", b.Name)
		return
	}
	buf := v.(*hopBuf)
	for k, vs := range buf.Header {
		if skipCachedHeader(k) {
			continue
		}
		for _, val := range vs {
			w.Header().Add(k, val)
		}
	}
	http.SetCookie(w, backendCookie(r, b.Name, h.cfg.Affinity.DeviceTTL))
	status := buf.Status
	_ = h.st.TouchToken(r.Context(), id.Token, r.Method, r.URL.Path, status)
	_ = h.st.TouchDevice(r.Context(), id.DeviceID)
	result := "proxied"
	if status >= 500 {
		result = "backend_5xx"
	} else if status >= 400 {
		result = "backend_4xx"
	}
	reqCount.WithLabelValues(b.Name, result).Inc()
	h.log.Info("proxy", "backend", b.Name, "status", status, "path", r.URL.Path, "method", r.Method, "coalesce", shared)
	w.WriteHeader(status)
	if !strings.EqualFold(r.Method, http.MethodHead) {
		_, _ = w.Write(buf.Body)
	}
}

func (h *Handler) coalesceReadLimit(r *http.Request) int64 {
	if libcache.IsLibraryRequest(r.Method, r.URL.Path) && h.lib != nil {
		return h.lib.MaxObject() + 1
	}
	if h.cache != nil {
		return h.cache.MaxObject() + 1
	}
	return 4<<20 + 1
}

func (h *Handler) hopOnce(r *http.Request, b *config.Backend, graylisted bool) (*http.Response, error) {
	tr := h.mon.RoundTripper(b.Name, graylisted)
	if tr == nil {
		return nil, io.ErrUnexpectedEOF
	}
	u := strings.TrimRight(b.URL, "/") + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header = r.Header.Clone()
	req.Header.Del("Expect")
	applyForwardedOrigin(req, r, b, h.cfg != nil && h.cfg.StayOnOriginEnabled())
	for k, v := range b.Headers {
		req.Header.Set(k, v)
	}
	if graylisted {
		req.Header.Set("Connection", "close")
	}
	res, err := tr.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	h.rewriteProxiedResponse(res, r)
	return res, nil
}

func (h *Handler) acquireLibrary(ctx context.Context, r *http.Request, backend string) error {
	if h.libSem == nil || r == nil || !libcache.IsLibraryRequest(r.Method, r.URL.Path) {
		return nil
	}
	return h.libSem.acquire(ctx, backend)
}

func (h *Handler) releaseLibrary(r *http.Request, backend string) {
	if h.libSem == nil || r == nil || !libcache.IsLibraryRequest(r.Method, r.URL.Path) {
		return
	}
	h.libSem.release(backend)
}

func (h *Handler) scheduleWarmLogin(b *config.Backend, token, userID string) {
	if h.cfg == nil || !h.cfg.Performance.WarmLoginEnabled() || !h.cfg.Performance.LibraryEnabled() || h.lib == nil {
		return
	}
	if b == nil || token == "" || userID == "" {
		return
	}
	go h.warmLogin(b, token, userID)
}

func (h *Handler) warmLogin(b *config.Backend, token, userID string) {
	timeout := 60 * time.Second
	if h.cfg.Performance.AuthTimeout > 0 {
		timeout = h.cfg.Performance.AuthTimeout
	}
	paths := []string{
		"/Users/" + userID + "/Views",
		"/Users/" + userID + "/Items/Resume",
		"/Shows/NextUp?UserId=" + url.QueryEscape(userID),
	}
	for _, p := range paths {
		h.warmOne(b, token, p, timeout)
	}
}

func (h *Handler) warmOne(b *config.Backend, token, rawURL string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	u := strings.TrimRight(b.URL, "/") + rawURL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return
	}
	req.Header.Set("X-Emby-Token", token)
	req.Header.Set("Authorization", `MediaBrowser Token="`+token+`"`)
	if b.Host != "" {
		req.Host = b.Host
	}
	b2 := *b
	b2.Timeout = timeout
	tr, err := health.HopTransport(b2)
	if err != nil {
		return
	}
	c := &http.Client{Timeout: timeout, Transport: tr}
	res, err := c.Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body)
		return
	}
	max := h.lib.MaxObject()
	raw, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil || int64(len(raw)) > max {
		return
	}
	ct := strings.ToLower(res.Header.Get("Content-Type"))
	if !strings.Contains(ct, "json") {
		return
	}
	pu, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	hdr := res.Header.Clone()
	hdr.Del("Set-Cookie")
	key := libcache.Key(b.Name, store.HashToken(token), http.MethodGet, pu.Path, pu.RawQuery)
	_ = h.lib.Put(key, &libcache.Entry{
		Backend:     b.Name,
		Status:      res.StatusCode,
		Header:      hdr,
		Body:        raw,
		ContentType: ct,
		ExpiresAt:   time.Now().Add(h.lib.TTL()),
	})
}

type libSem struct {
	max int
	mu  sync.Mutex
	ch  map[string]chan struct{}
}

func newLibSem(max int) *libSem {
	if max < 1 {
		max = 1
	}
	return &libSem{max: max, ch: map[string]chan struct{}{}}
}

func (s *libSem) slot(name string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.ch[name]
	if !ok {
		ch = make(chan struct{}, s.max)
		s.ch[name] = ch
	}
	return ch
}

func (s *libSem) acquire(ctx context.Context, name string) error {
	ch := s.slot(name)
	select {
	case ch <- struct{}{}:
		libInflight.WithLabelValues(name).Inc()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *libSem) release(name string) {
	ch := s.slot(name)
	select {
	case <-ch:
		libInflight.WithLabelValues(name).Dec()
	default:
	}
}

func (s *libSem) inflight() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for name, ch := range s.ch {
		out[name] = len(ch)
	}
	return out
}
