package imgcache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type diskMeta struct {
	Key          string              `json:"key"`
	Backend      string              `json:"backend"`
	Status       int                 `json:"status"`
	Header       map[string][]string `json:"header"`
	ETag         string              `json:"etag"`
	LastModified string              `json:"lastModified"`
	ContentType  string              `json:"contentType"`
	StoredAt     time.Time           `json:"storedAt"`
	ExpiresAt    time.Time           `json:"expiresAt"`
	Size         int64               `json:"size"`
}

type diskItem struct {
	key     string
	hash    string
	size    int64
	expires time.Time
}

func (c *Cache) diskEnabled() bool {
	return c != nil && c.cfg.Disk.Enabled != nil && *c.cfg.Disk.Enabled && c.cfg.Disk.Path != ""
}

func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (c *Cache) diskPaths(hash string) (meta, bin string) {
	dir := c.cfg.Disk.Path
	return filepath.Join(dir, hash+".meta"), filepath.Join(dir, hash+".bin")
}

func (c *Cache) loadDisk() {
	if !c.diskEnabled() {
		return
	}
	if err := os.MkdirAll(c.cfg.Disk.Path, 0o755); err != nil {
		return
	}
	ents, err := os.ReadDir(c.cfg.Disk.Path)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta") {
			continue
		}
		hash := strings.TrimSuffix(e.Name(), ".meta")
		metaPath, binPath := c.diskPaths(hash)
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			c.removeDiskFiles(hash)
			continue
		}
		var meta diskMeta
		if json.Unmarshal(raw, &meta) != nil || meta.Key == "" || meta.Size <= 0 {
			c.removeDiskFiles(hash)
			continue
		}
		if !meta.ExpiresAt.IsZero() && now.After(meta.ExpiresAt) {
			c.removeDiskFiles(hash)
			continue
		}
		if st, err := os.Stat(binPath); err != nil || st.Size() != meta.Size {
			c.removeDiskFiles(hash)
			continue
		}
		c.addDiskIndex(meta.Key, hash, meta.Size, meta.ExpiresAt)
	}
	for c.cfg.Disk.MaxBytes > 0 && c.diskN > c.cfg.Disk.MaxBytes && c.diskLL.Len() > 0 {
		back := c.diskLL.Back()
		it := back.Value.(*diskItem)
		backend, _, _ := strings.Cut(it.key, "\n")
		reqCount.WithLabelValues(backend, "disk_evict").Inc()
		c.removeDiskIndexLocked(back, true)
		c.dev++
	}
	c.observeLocked()
}

func (c *Cache) addDiskIndex(key, hash string, size int64, expires time.Time) {
	if el, ok := c.diskIdx[key]; ok {
		c.removeDiskIndexLocked(el, false)
	}
	it := &diskItem{key: key, hash: hash, size: size, expires: expires}
	c.diskIdx[key] = c.diskLL.PushFront(it)
	c.diskN += size
}

func (c *Cache) getDisk(key string) *Entry {
	if !c.diskEnabled() {
		return nil
	}
	c.mu.Lock()
	el, ok := c.diskIdx[key]
	if !ok {
		c.mu.Unlock()
		return nil
	}
	it := el.Value.(*diskItem)
	if !it.expires.IsZero() && time.Now().After(it.expires) {
		c.removeDiskIndexLocked(el, true)
		c.observeLocked()
		c.mu.Unlock()
		return nil
	}
	hash := it.hash
	c.mu.Unlock()

	metaPath, binPath := c.diskPaths(hash)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		c.dropDiskKey(key, hash)
		return nil
	}
	var meta diskMeta
	if json.Unmarshal(raw, &meta) != nil {
		c.dropDiskKey(key, hash)
		return nil
	}
	body, err := os.ReadFile(binPath)
	if err != nil || int64(len(body)) != meta.Size {
		c.dropDiskKey(key, hash)
		return nil
	}
	if !meta.ExpiresAt.IsZero() && time.Now().After(meta.ExpiresAt) {
		c.dropDiskKey(key, hash)
		return nil
	}
	ent := &Entry{
		Backend:      meta.Backend,
		Status:       meta.Status,
		Header:       http.Header(meta.Header).Clone(),
		Body:         body,
		ETag:         meta.ETag,
		LastModified: meta.LastModified,
		ContentType:  meta.ContentType,
		StoredAt:     meta.StoredAt,
		ExpiresAt:    meta.ExpiresAt,
	}
	if ent.Status == 0 {
		ent.Status = http.StatusOK
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.diskIdx[key]; ok {
		c.diskLL.MoveToFront(el)
	}
	c.dhit++
	reqCount.WithLabelValues(ent.Backend, "disk_hit").Inc()
	c.putMemoryLocked(key, ent)
	return cloneEntry(ent)
}

func (c *Cache) putMemoryLocked(key string, ent *Entry) {
	size := int64(len(ent.Body))
	if size == 0 || size > c.cfg.MaxObject || size > c.cfg.MaxBytes {
		return
	}
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
		return
	}
	stored := cloneEntry(ent)
	el := c.ll.PushFront(&item{key: key, ent: stored})
	c.idx[key] = el
	c.n += size
	c.observeLocked()
}

func (c *Cache) putDiskLocked(key string, ent *Entry) {
	if !c.diskEnabled() || ent == nil {
		return
	}
	size := int64(len(ent.Body))
	if size == 0 || size > c.cfg.MaxObject || size > c.cfg.Disk.MaxBytes {
		return
	}
	hash := keyHash(key)
	if el, ok := c.diskIdx[key]; ok {
		c.removeDiskIndexLocked(el, false)
	}
	for c.diskN+size > c.cfg.Disk.MaxBytes && c.diskLL.Len() > 0 {
		back := c.diskLL.Back()
		reqCount.WithLabelValues(ent.Backend, "disk_evict").Inc()
		c.removeDiskIndexLocked(back, true)
		c.dev++
	}
	if c.diskN+size > c.cfg.Disk.MaxBytes {
		c.observeLocked()
		return
	}
	meta := diskMeta{
		Key:          key,
		Backend:      ent.Backend,
		Status:       ent.Status,
		Header:       ent.Header,
		ETag:         ent.ETag,
		LastModified: ent.LastModified,
		ContentType:  ent.ContentType,
		StoredAt:     ent.StoredAt,
		ExpiresAt:    ent.ExpiresAt,
		Size:         size,
	}
	if err := c.writeDiskFiles(hash, meta, ent.Body); err != nil {
		return
	}
	c.addDiskIndex(key, hash, size, ent.ExpiresAt)
	c.dput++
	reqCount.WithLabelValues(ent.Backend, "disk_store").Inc()
	c.observeLocked()
}

func (c *Cache) writeDiskFiles(hash string, meta diskMeta, body []byte) error {
	if err := os.MkdirAll(c.cfg.Disk.Path, 0o755); err != nil {
		return err
	}
	metaPath, binPath := c.diskPaths(hash)
	metaTmp, binTmp := metaPath+".tmp", binPath+".tmp"
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(binTmp, body, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(metaTmp, raw, 0o644); err != nil {
		_ = os.Remove(binTmp)
		return err
	}
	if err := os.Rename(binTmp, binPath); err != nil {
		_ = os.Remove(binTmp)
		_ = os.Remove(metaTmp)
		return err
	}
	if err := os.Rename(metaTmp, metaPath); err != nil {
		_ = os.Remove(metaTmp)
		_ = os.Remove(binPath)
		return err
	}
	return nil
}

func (c *Cache) dropDiskKey(key, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.diskIdx[key]; ok {
		c.removeDiskIndexLocked(el, true)
	} else {
		c.removeDiskFiles(hash)
	}
	c.observeLocked()
}

func (c *Cache) removeDiskIndexLocked(el *list.Element, deleteFiles bool) {
	if el == nil {
		return
	}
	it := el.Value.(*diskItem)
	c.diskN -= it.size
	if c.diskN < 0 {
		c.diskN = 0
	}
	delete(c.diskIdx, it.key)
	c.diskLL.Remove(el)
	if deleteFiles {
		c.removeDiskFiles(it.hash)
	}
}

func (c *Cache) removeDiskFiles(hash string) {
	if hash == "" || c.cfg.Disk.Path == "" {
		return
	}
	meta, bin := c.diskPaths(hash)
	_ = os.Remove(meta)
	_ = os.Remove(bin)
	_ = os.Remove(meta + ".tmp")
	_ = os.Remove(bin + ".tmp")
}
