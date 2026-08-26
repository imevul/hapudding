package libcache

import (
	"container/list"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/imevul/hapudding/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	reqCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hap_library_cache_requests_total",
		Help: "Library JSON cache lookups and stores",
	}, []string{"backend", "result"})
	byteGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hap_library_cache_bytes",
		Help: "Current library cache payload bytes",
	})
	objGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hap_library_cache_objects",
		Help: "Current library cache object count",
	})
)

type Entry struct {
	Backend     string
	Status      int
	Header      http.Header
	Body        []byte
	ContentType string
	Hits        int64
	StoredAt    time.Time
	ExpiresAt   time.Time
}

type Stats struct {
	Enabled  bool   `json:"enabled"`
	Bytes    int64  `json:"bytes"`
	Objects  int    `json:"objects"`
	Hits     int64  `json:"hits"`
	Misses   int64  `json:"misses"`
	Stores   int64  `json:"stores"`
	Evicts   int64  `json:"evicts"`
	MaxBytes int64  `json:"maxBytes"`
	TTL      string `json:"ttl,omitempty"`
}

type Cache struct {
	cfg config.LibraryCache
	mu  sync.Mutex
	ll  *list.List
	idx map[string]*list.Element
	n   int64
	hit int64
	mis int64
	put int64
	ev  int64
}

type item struct {
	key string
	ent *Entry
}

func New(cfg config.LibraryCache) *Cache {
	return &Cache{
		cfg: cfg,
		ll:  list.New(),
		idx: map[string]*list.Element{},
	}
}

func Key(backend, tokenHash, method, path, rawQuery string) string {
	return backend + "\n" + tokenHash + "\n" + strings.ToUpper(method) + "\n" + path + "\n" + rawQuery
}

func (c *Cache) Get(key string) *Entry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.idx[key]
	if !ok {
		c.mis++
		backend, _, _ := strings.Cut(key, "\n")
		reqCount.WithLabelValues(backend, "miss").Inc()
		return nil
	}
	it := el.Value.(*item)
	if !it.ent.ExpiresAt.IsZero() && time.Now().After(it.ent.ExpiresAt) {
		c.removeLocked(el)
		c.mis++
		reqCount.WithLabelValues(it.ent.Backend, "miss").Inc()
		c.observeLocked()
		return nil
	}
	c.ll.MoveToFront(el)
	it.ent.Hits++
	c.hit++
	reqCount.WithLabelValues(it.ent.Backend, "hit").Inc()
	return cloneEntry(it.ent)
}

func (c *Cache) Put(key string, ent *Entry) bool {
	if c == nil || ent == nil {
		return false
	}
	size := int64(len(ent.Body))
	if size == 0 || size > c.cfg.MaxObject || size > c.cfg.MaxBytes {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[key]; ok {
		c.removeLocked(el)
	}
	for c.n+size > c.cfg.MaxBytes && c.ll.Len() > 0 {
		back := c.ll.Back()
		it := back.Value.(*item)
		reqCount.WithLabelValues(it.ent.Backend, "evict").Inc()
		c.removeLocked(back)
		c.ev++
	}
	if c.n+size > c.cfg.MaxBytes {
		c.observeLocked()
		return false
	}
	stored := cloneEntry(ent)
	stored.StoredAt = time.Now()
	if stored.ExpiresAt.IsZero() && c.cfg.TTL > 0 {
		stored.ExpiresAt = stored.StoredAt.Add(c.cfg.TTL)
	}
	el := c.ll.PushFront(&item{key: key, ent: stored})
	c.idx[key] = el
	c.n += size
	c.put++
	reqCount.WithLabelValues(ent.Backend, "store").Inc()
	c.observeLocked()
	return true
}

func (c *Cache) DropToken(backend, tokenHash string) {
	if c == nil || backend == "" || tokenHash == "" {
		return
	}
	prefix := backend + "\n" + tokenHash + "\n"
	c.mu.Lock()
	defer c.mu.Unlock()
	var drop []*list.Element
	for el := c.ll.Front(); el != nil; el = el.Next() {
		it := el.Value.(*item)
		if strings.HasPrefix(it.key, prefix) {
			drop = append(drop, el)
		}
	}
	for _, el := range drop {
		reqCount.WithLabelValues(backend, "invalidate").Inc()
		c.removeLocked(el)
		c.ev++
	}
	c.observeLocked()
}

func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{Enabled: false}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := ""
	if c.cfg.TTL > 0 {
		ttl = c.cfg.TTL.String()
	}
	return Stats{
		Enabled:  true,
		Bytes:    c.n,
		Objects:  c.ll.Len(),
		Hits:     c.hit,
		Misses:   c.mis,
		Stores:   c.put,
		Evicts:   c.ev,
		MaxBytes: c.cfg.MaxBytes,
		TTL:      ttl,
	}
}

func (c *Cache) MaxObject() int64 {
	if c == nil {
		return 0
	}
	return c.cfg.MaxObject
}

func (c *Cache) TTL() time.Duration {
	if c == nil {
		return 0
	}
	return c.cfg.TTL
}

func (c *Cache) removeLocked(el *list.Element) {
	if el == nil {
		return
	}
	it := el.Value.(*item)
	c.n -= int64(len(it.ent.Body))
	if c.n < 0 {
		c.n = 0
	}
	delete(c.idx, it.key)
	c.ll.Remove(el)
}

func (c *Cache) observeLocked() {
	byteGauge.Set(float64(c.n))
	objGauge.Set(float64(c.ll.Len()))
}

func cloneEntry(e *Entry) *Entry {
	out := *e
	out.Header = e.Header.Clone()
	out.Body = append([]byte(nil), e.Body...)
	return &out
}

func IsLibraryPath(path string) bool {
	p := strings.ToLower(path)
	if strings.Contains(p, "/items/resume") {
		return true
	}
	if strings.Contains(p, "/shows/nextup") {
		return true
	}
	if strings.Contains(p, "/items/latest") {
		return true
	}
	if strings.Contains(p, "/users/") && strings.HasSuffix(p, "/views") {
		return true
	}
	return false
}

func IsLibraryRequest(method, path string) bool {
	if !strings.EqualFold(method, http.MethodGet) && !strings.EqualFold(method, http.MethodHead) {
		return false
	}
	return IsLibraryPath(path)
}

func IsInvalidateRequest(method, path string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodDelete, http.MethodPut:
	default:
		return false
	}
	p := strings.ToLower(path)
	if strings.Contains(p, "/sessions/playing") {
		return true
	}
	if strings.Contains(p, "/playeditems") {
		return true
	}
	if strings.Contains(p, "/favoriteitems") {
		return true
	}
	if strings.Contains(p, "/userdata") {
		return true
	}
	if strings.Contains(p, "/rating") {
		return true
	}
	return false
}
