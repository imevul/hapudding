package imgcache

import (
	"container/list"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imevul/hapudding/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	reqCount = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hap_cache_requests_total",
		Help: "Image cache lookups and stores",
	}, []string{"backend", "result"})
	byteGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hap_cache_bytes",
		Help: "Current image cache payload bytes",
	})
	objGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hap_cache_objects",
		Help: "Current image cache object count",
	})
	diskByteGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hap_cache_disk_bytes",
		Help: "Current image disk cache payload bytes",
	})
	diskObjGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hap_cache_disk_objects",
		Help: "Current image disk cache object count",
	})
)

// Entry is a stored image response (no Set-Cookie).
type Entry struct {
	Backend      string
	Status       int
	Header       http.Header
	Body         []byte
	ETag         string
	LastModified string
	ContentType  string
	Hits         int64
	StoredAt     time.Time
	ExpiresAt    time.Time
}

// Stats is a snapshot for /hap/cache.
type Stats struct {
	Enabled  bool      `json:"enabled"`
	Bytes    int64     `json:"bytes"`
	Objects  int       `json:"objects"`
	Hits     int64     `json:"hits"`
	Misses   int64     `json:"misses"`
	Stores   int64     `json:"stores"`
	Evicts   int64     `json:"evicts"`
	MaxBytes int64     `json:"maxBytes"`
	Disk     DiskStats `json:"disk"`
}

type DiskStats struct {
	Enabled  bool   `json:"enabled"`
	Path     string `json:"path,omitempty"`
	Bytes    int64  `json:"bytes"`
	Objects  int    `json:"objects"`
	Hits     int64  `json:"hits"`
	Stores   int64  `json:"stores"`
	Evicts   int64  `json:"evicts"`
	MaxBytes int64  `json:"maxBytes"`
}

type Cache struct {
	cfg     config.Cache
	mu      sync.Mutex
	ll      *list.List
	idx     map[string]*list.Element
	n       int64
	hit     int64
	mis     int64
	put     int64
	ev      int64
	diskLL  *list.List
	diskIdx map[string]*list.Element
	diskN   int64
	dhit    int64
	dput    int64
	dev     int64
}

type item struct {
	key string
	ent *Entry
}

func New(cfg config.Cache) *Cache {
	c := &Cache{
		cfg:     cfg,
		ll:      list.New(),
		idx:     map[string]*list.Element{},
		diskLL:  list.New(),
		diskIdx: map[string]*list.Element{},
	}
	c.loadDisk()
	return c
}

func Key(backend, path, rawQuery, accept string) string {
	return backend + "\n" + path + "\n" + rawQuery + "\n" + accept
}

func (c *Cache) Get(key string) *Entry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if el, ok := c.idx[key]; ok {
		it := el.Value.(*item)
		if it.ent.ExpiresAt.IsZero() || !time.Now().After(it.ent.ExpiresAt) {
			c.ll.MoveToFront(el)
			it.ent.Hits++
			c.hit++
			reqCount.WithLabelValues(it.ent.Backend, "hit").Inc()
			out := cloneEntry(it.ent)
			c.mu.Unlock()
			return out
		}
		c.removeLocked(el)
		c.observeLocked()
	}
	c.mu.Unlock()
	if ent := c.getDisk(key); ent != nil {
		return ent
	}
	c.mu.Lock()
	c.mis++
	backend, _, _ := strings.Cut(key, "\n")
	reqCount.WithLabelValues(backend, "miss").Inc()
	c.mu.Unlock()
	return nil
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
	el := c.ll.PushFront(&item{key: key, ent: stored})
	c.idx[key] = el
	c.n += size
	c.put++
	reqCount.WithLabelValues(ent.Backend, "store").Inc()
	c.observeLocked()
	c.putDiskLocked(key, stored)
	return true
}

func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{Enabled: false}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Enabled:  true,
		Bytes:    c.n,
		Objects:  c.ll.Len(),
		Hits:     c.hit,
		Misses:   c.mis,
		Stores:   c.put,
		Evicts:   c.ev,
		MaxBytes: c.cfg.MaxBytes,
		Disk: DiskStats{
			Enabled:  c.diskEnabled(),
			Path:     c.cfg.Disk.Path,
			Bytes:    c.diskN,
			Objects:  c.diskLL.Len(),
			Hits:     c.dhit,
			Stores:   c.dput,
			Evicts:   c.dev,
			MaxBytes: c.cfg.Disk.MaxBytes,
		},
	}
}

func (c *Cache) MaxObject() int64 {
	if c == nil {
		return 0
	}
	return c.cfg.MaxObject
}

func (c *Cache) DefaultTTL() time.Duration { return c.cfg.DefaultTTL }
func (c *Cache) MaxTTL() time.Duration     { return c.cfg.MaxTTL }

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
	diskByteGauge.Set(float64(c.diskN))
	if c.diskLL != nil {
		diskObjGauge.Set(float64(c.diskLL.Len()))
	}
}

func cloneEntry(e *Entry) *Entry {
	out := *e
	out.Header = e.Header.Clone()
	out.Body = append([]byte(nil), e.Body...)
	return &out
}

// IsItemImagePath is GET/HEAD artwork under /Items/{id}/Images/, not user avatars.
func IsItemImagePath(path string) bool {
	p := strings.ToLower(path)
	if strings.Contains(p, "/users/") {
		return false
	}
	i := strings.Index(p, "/items/")
	if i < 0 {
		return false
	}
	return strings.Contains(p[i:], "/images/")
}

func IsCacheableRequest(method, path string) bool {
	if !strings.EqualFold(method, http.MethodGet) && !strings.EqualFold(method, http.MethodHead) {
		return false
	}
	return IsItemImagePath(path)
}

// StoreTTL returns how long a 200 image may be kept. ok is false when Cache-Control forbids it.
func StoreTTL(cacheControl string, defaultTTL, maxTTL time.Duration) (time.Duration, bool) {
	if cacheControlForbids(cacheControl) {
		return 0, false
	}
	if maxAge, ok := cacheControlMaxAge(cacheControl); ok {
		ttl := time.Duration(maxAge) * time.Second
		if maxTTL > 0 && ttl > maxTTL {
			ttl = maxTTL
		}
		if ttl <= 0 {
			return 0, false
		}
		return ttl, true
	}
	if defaultTTL <= 0 {
		return 0, false
	}
	return defaultTTL, true
}

func cacheControlForbids(cc string) bool {
	for _, p := range strings.Split(strings.ToLower(cc), ",") {
		dir, _, _ := strings.Cut(strings.TrimSpace(p), "=")
		dir = strings.TrimSpace(dir)
		if dir == "no-store" || dir == "private" || dir == "no-cache" {
			return true
		}
	}
	return false
}

func cacheControlMaxAge(cc string) (int64, bool) {
	for _, p := range strings.Split(strings.ToLower(cc), ",") {
		p = strings.TrimSpace(p)
		dir, val, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(dir) != "max-age" {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

func FreshFor(e *Entry, req *http.Request) bool {
	if e == nil || req == nil {
		return false
	}
	if inm := req.Header.Get("If-None-Match"); inm != "" && e.ETag != "" {
		return etagMatch(inm, e.ETag)
	}
	if ims := req.Header.Get("If-Modified-Since"); ims != "" && e.LastModified != "" {
		since, err1 := http.ParseTime(ims)
		mod, err2 := http.ParseTime(e.LastModified)
		if err1 == nil && err2 == nil && !mod.After(since) {
			return true
		}
	}
	return false
}

func etagMatch(ifNoneMatch, etag string) bool {
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)
	etag = strings.TrimSpace(etag)
	if ifNoneMatch == "*" {
		return true
	}
	want := strings.Trim(etag, `"`)
	for _, part := range strings.Split(ifNoneMatch, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		if strings.Trim(part, `"`) == want {
			return true
		}
	}
	return false
}
