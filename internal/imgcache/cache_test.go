package imgcache

import (
	"net/http"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
)

func TestLRUEvictsColdKeepsHot(t *testing.T) {
	c := New(config.Cache{MaxBytes: 30, MaxObject: 20, DefaultTTL: time.Hour, MaxTTL: time.Hour})
	if !c.Put(Key("a", "/1", "", ""), &Entry{Backend: "a", Body: []byte("cold-image-xx")}) {
		t.Fatal("put cold")
	}
	if !c.Put(Key("a", "/2", "", ""), &Entry{Backend: "a", Body: []byte("hot-image-xxx")}) {
		t.Fatal("put hot")
	}
	if got := c.Get(Key("a", "/2", "", "")); got == nil {
		t.Fatal("hot should hit")
	}
	if !c.Put(Key("a", "/3", "", ""), &Entry{Backend: "a", Body: []byte("new-image-xxx")}) {
		t.Fatal("put new")
	}
	if c.Get(Key("a", "/1", "", "")) != nil {
		t.Fatal("cold should be evicted")
	}
	if c.Get(Key("a", "/2", "", "")) == nil {
		t.Fatal("hot should remain")
	}
	st := c.Stats()
	if st.Evicts < 1 {
		t.Fatalf("want evict, stats=%+v", st)
	}
}

func TestTTLExpiryIsMiss(t *testing.T) {
	c := New(config.Cache{MaxBytes: 100, MaxObject: 50, DefaultTTL: time.Millisecond, MaxTTL: time.Hour})
	ent := &Entry{Backend: "a", Body: []byte("png"), ExpiresAt: time.Now().Add(5 * time.Millisecond)}
	if !c.Put(Key("a", "/i", "", ""), ent) {
		t.Fatal("put")
	}
	time.Sleep(15 * time.Millisecond)
	if c.Get(Key("a", "/i", "", "")) != nil {
		t.Fatal("expired should miss")
	}
}

func TestBackendsDoNotShareKeys(t *testing.T) {
	c := New(config.Cache{MaxBytes: 200, MaxObject: 50, DefaultTTL: time.Hour, MaxTTL: time.Hour})
	c.Put(Key("server-a", "/Items/1/Images/Primary", "", ""), &Entry{Backend: "server-a", Body: []byte("if")})
	c.Put(Key("server-b", "/Items/1/Images/Primary", "", ""), &Entry{Backend: "server-b", Body: []byte("ex")})
	if got := c.Get(Key("server-a", "/Items/1/Images/Primary", "", "")); got == nil || string(got.Body) != "if" {
		t.Fatalf("server-a %+v", got)
	}
	if got := c.Get(Key("server-b", "/Items/1/Images/Primary", "", "")); got == nil || string(got.Body) != "ex" {
		t.Fatalf("server-b %+v", got)
	}
}

func TestIsItemImagePath(t *testing.T) {
	if !IsItemImagePath("/Items/abc/Images/Primary") || !IsCacheableRequest(http.MethodGet, "/Items/abc/Images/Primary/0") {
		t.Fatal("item images")
	}
	if IsItemImagePath("/Users/abc/Images/Primary") {
		t.Fatal("user avatar")
	}
	if IsItemImagePath("/Users/u/Items") || IsCacheableRequest(http.MethodGet, "/Items/abc") {
		t.Fatal("json listings")
	}
	if IsCacheableRequest(http.MethodPost, "/Items/abc/Images/Primary") {
		t.Fatal("POST")
	}
}

func TestStoreTTL(t *testing.T) {
	def, max := 15*time.Minute, 24*time.Hour
	if _, ok := StoreTTL("no-store", def, max); ok {
		t.Fatal("no-store")
	}
	if _, ok := StoreTTL("private, max-age=60", def, max); ok {
		t.Fatal("private")
	}
	if _, ok := StoreTTL("no-cache", def, max); ok {
		t.Fatal("no-cache")
	}
	ttl, ok := StoreTTL("public", def, max)
	if !ok || ttl != def {
		t.Fatalf("public no max-age: %s %v", ttl, ok)
	}
	ttl, ok = StoreTTL("public, max-age=31536000, immutable", def, max)
	if !ok || ttl != max {
		t.Fatalf("capped max-age: %s", ttl)
	}
	ttl, ok = StoreTTL("", def, max)
	if !ok || ttl != def {
		t.Fatalf("empty: %s %v", ttl, ok)
	}
}

func TestFreshForValidators(t *testing.T) {
	e := &Entry{ETag: `"abc"`, LastModified: "Sat, 04 Jul 2026 23:48:51 GMT"}
	req := httptestReq(http.Header{"If-None-Match": {`"abc"`}})
	if !FreshFor(e, req) {
		t.Fatal("etag")
	}
	req = httptestReq(http.Header{"If-Modified-Since": {"Sat, 04 Jul 2026 23:48:51 GMT"}})
	if !FreshFor(e, req) {
		t.Fatal("ims")
	}
	req = httptestReq(http.Header{"If-None-Match": {`"other"`}})
	if FreshFor(e, req) {
		t.Fatal("mismatch")
	}
}

func httptestReq(h http.Header) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header = h
	return r
}
