package libcache

import (
	"net/http"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
)

func TestKeySeparatedByTokenAndBackend(t *testing.T) {
	a := Key("server-a", "tok1", http.MethodGet, "/Users/u/Views", "")
	b := Key("server-a", "tok2", http.MethodGet, "/Users/u/Views", "")
	c := Key("server-b", "tok1", http.MethodGet, "/Users/u/Views", "")
	if a == b || a == c {
		t.Fatal("keys must include token and backend")
	}
}

func TestHitMissAndDropToken(t *testing.T) {
	c := New(config.LibraryCache{TTL: time.Hour, MaxBytes: 1 << 20, MaxObject: 1 << 20})
	key := Key("server-a", "hash", http.MethodGet, "/Users/u/Views", "")
	if c.Get(key) != nil {
		t.Fatal("empty get")
	}
	if !c.Put(key, &Entry{Backend: "server-a", Status: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"ok":1}`)}) {
		t.Fatal("put")
	}
	if got := c.Get(key); got == nil || string(got.Body) != `{"ok":1}` {
		t.Fatalf("hit %+v", got)
	}
	other := Key("server-a", "other", http.MethodGet, "/Users/u/Views", "")
	if c.Get(other) != nil {
		t.Fatal("other token")
	}
	c.DropToken("server-a", "hash")
	if c.Get(key) != nil {
		t.Fatal("dropped")
	}
}

func TestIsLibraryAndInvalidate(t *testing.T) {
	if !IsLibraryRequest(http.MethodGet, "/Users/abc/Views") {
		t.Fatal("views")
	}
	if !IsLibraryRequest(http.MethodGet, "/Users/abc/Items/Latest") {
		t.Fatal("latest")
	}
	if IsLibraryRequest(http.MethodGet, "/Users/abc/Items") {
		t.Fatal("general items must not match")
	}
	if !IsInvalidateRequest(http.MethodPost, "/Sessions/Playing/Progress") {
		t.Fatal("playing")
	}
	if IsInvalidateRequest(http.MethodGet, "/Sessions/Playing") {
		t.Fatal("GET playing is not a mutation")
	}
}
