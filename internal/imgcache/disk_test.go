package imgcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imevul/hapudding/internal/config"
)

func on() *bool  { v := true; return &v }
func off() *bool { v := false; return &v }

func diskCfg(dir string, diskMax int64) config.Cache {
	return config.Cache{
		MaxBytes:   1 << 20,
		MaxObject:  1 << 20,
		DefaultTTL: time.Hour,
		MaxTTL:     time.Hour,
		Disk:       config.CacheDisk{Enabled: on(), Path: dir, MaxBytes: diskMax},
	}
}

func TestDiskSurvivesNew(t *testing.T) {
	dir := t.TempDir()
	key := Key("server-a", "/Items/1/Images/Primary", "tag=a", "image/webp")
	c1 := New(diskCfg(dir, 1<<20))
	if !c1.Put(key, &Entry{Backend: "server-a", Status: 200, Header: map[string][]string{"Content-Type": {"image/png"}}, Body: []byte("png-bytes"), ExpiresAt: time.Now().Add(time.Hour)}) {
		t.Fatal("put")
	}
	c2 := New(diskCfg(dir, 1<<20))
	got := c2.Get(key)
	if got == nil || string(got.Body) != "png-bytes" {
		t.Fatalf("want disk hit after New, got %+v", got)
	}
	st := c2.Stats()
	if !st.Disk.Enabled || st.Disk.Hits < 1 || st.Disk.Objects < 1 {
		t.Fatalf("disk stats %+v", st.Disk)
	}
}

func TestDiskExpiredIsMiss(t *testing.T) {
	dir := t.TempDir()
	key := Key("server-a", "/Items/1/Images/Primary", "", "")
	c := New(diskCfg(dir, 1<<20))
	if !c.Put(key, &Entry{Backend: "server-a", Body: []byte("old"), ExpiresAt: time.Now().Add(8 * time.Millisecond)}) {
		t.Fatal("put")
	}
	time.Sleep(20 * time.Millisecond)
	c2 := New(diskCfg(dir, 1<<20))
	if c2.Get(key) != nil {
		t.Fatal("expired disk entry must miss")
	}
}

func TestDiskEvictsCold(t *testing.T) {
	dir := t.TempDir()
	cfg := diskCfg(dir, 25)
	c := New(cfg)
	k1 := Key("a", "/Items/1/Images/Primary", "", "")
	k2 := Key("a", "/Items/2/Images/Primary", "", "")
	k3 := Key("a", "/Items/3/Images/Primary", "", "")
	body := []byte("1234567890") // 10
	if !c.Put(k1, &Entry{Backend: "a", Body: body, ExpiresAt: time.Now().Add(time.Hour)}) {
		t.Fatal("k1")
	}
	if !c.Put(k2, &Entry{Backend: "a", Body: body, ExpiresAt: time.Now().Add(time.Hour)}) {
		t.Fatal("k2")
	}
	if c.Get(k2) == nil {
		t.Fatal("k2 hot")
	}
	if !c.Put(k3, &Entry{Backend: "a", Body: body, ExpiresAt: time.Now().Add(time.Hour)}) {
		t.Fatal("k3")
	}
	c2 := New(cfg)
	if c2.Get(k1) != nil {
		t.Fatal("cold k1 should have been evicted from disk")
	}
	if c2.Get(k2) == nil {
		t.Fatal("hot k2 should remain on disk")
	}
}

func TestDiskDisabledDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	c := New(config.Cache{
		MaxBytes:  1 << 20,
		MaxObject: 1 << 20,
		Disk:      config.CacheDisk{Enabled: off(), Path: dir, MaxBytes: 1 << 20},
	})
	key := Key("a", "/Items/1/Images/Primary", "", "")
	if !c.Put(key, &Entry{Backend: "a", Body: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)}) {
		t.Fatal("put")
	}
	ents, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("disk disabled must not write, files=%v", ents)
	}
	if _, err := os.Stat(filepath.Join(dir, keyHash(key)+".bin")); !os.IsNotExist(err) {
		t.Fatal("bin must not exist")
	}
}
