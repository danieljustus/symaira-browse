package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxSize   = 100 * 1024 * 1024
	evictionDebounce = 30 * time.Second
)

// Meta is stored alongside each cached body.
type Meta struct {
	URL         string              `json:"url"`
	FinalURL    string              `json:"final_url"`
	StatusCode  int                 `json:"status_code"`
	ContentType string              `json:"content_type"`
	Protocol    string              `json:"protocol"`
	Headers     map[string][]string `json:"headers"`
	StoredAt    time.Time           `json:"stored_at"`
	TTL         time.Duration       `json:"ttl"`
}

// Cache is a flat-file, content-addressed response cache.
type Cache struct {
	dir          string
	ttl          time.Duration
	maxSize      int64
	mu           sync.RWMutex
	indexMgr     *indexManager
	lastSave     time.Time
	lastEviction time.Time
}

// New creates a Cache rooted at dir with the given TTL and max size.
// It ensures the directory exists with 0700 permissions and fixes
// overly permissive directories on shared systems.
// If maxSize is 0, the default (100 MB) is used.
//
// A full cache-directory rescan (reconcileIndex) only runs when the
// persisted index is missing or fails to parse; a cleanly loaded index is
// trusted as-is, avoiding startup latency proportional to cache size on
// every invocation.
func New(dir string, ttl time.Duration, maxSize int64) *Cache {
	ensureCacheDir(dir)
	im := newIndexManager(dir)
	valid, _ := im.load()
	if maxSize <= 0 {
		maxSize = defaultMaxSize
	}
	c := &Cache{dir: dir, ttl: ttl, maxSize: maxSize, indexMgr: im}
	if !valid {
		c.reconcileIndex()
	}
	return c
}

// reconcileIndex walks the cache directory and rebuilds the index from
// actual files on disk, fixing any drift caused by prior-process crashes,
// manual deletion, or incomplete writes.
func (c *Cache) reconcileIndex() {
	diskSize, diskEntries := c.scanCache()

	entries := make([]indexEntry, len(diskEntries))
	for i, e := range diskEntries {
		entries[i] = indexEntry{
			Key:      e.key,
			Size:     e.size,
			StoredAt: e.storedAt,
		}
	}

	c.indexMgr.rebuild(entries, diskSize)
	c.indexMgr.save()
}

// ensureCacheDir creates the cache directory with 0700 permissions
// and tightens existing directories that are overly permissive.
func ensureCacheDir(dir string) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		return
	}
	if info.Mode().Perm() != 0700 {
		os.Chmod(dir, 0700)
	}
}

// DefaultDir returns ~/.cache/symfetch.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "symfetch")
	}
	return filepath.Join(home, ".cache", "symfetch")
}

// cacheKeyVersion is bumped when the key scheme changes to invalidate
// incompatible old entries automatically. Bump this whenever the hash
// input fields change so stale cached results are never served.
const cacheKeyVersion = "v3"

func (c *Cache) key(url, profile, format, session, contentKey string) string {
	h := sha256.New()
	h.Write([]byte(cacheKeyVersion))
	h.Write([]byte("|"))
	h.Write([]byte(url))
	h.Write([]byte("|"))
	h.Write([]byte(profile))
	h.Write([]byte("|"))
	h.Write([]byte(format))
	h.Write([]byte("|"))
	h.Write([]byte(session))
	h.Write([]byte("|"))
	h.Write([]byte(contentKey))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *Cache) bodyPath(k string) string {
	return filepath.Join(c.dir, k[:2], k+".body")
}

func (c *Cache) metaPath(k string) string {
	return filepath.Join(c.dir, k[:2], k+".meta.json")
}

// Get returns cached body+meta if present and not expired.
func (c *Cache) Get(url, profile, format, session, contentKey string) ([]byte, *Meta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	k := c.key(url, profile, format, session, contentKey)
	metaData, err := os.ReadFile(c.metaPath(k))
	if err != nil {
		return nil, nil, false
	}
	var m Meta
	if err := json.Unmarshal(metaData, &m); err != nil {
		return nil, nil, false
	}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = c.ttl
	}
	if time.Since(m.StoredAt) > ttl {
		return nil, nil, false
	}
	body, err := os.ReadFile(c.bodyPath(k))
	if err != nil {
		return nil, nil, false
	}
	return body, &m, true
}

// Put stores the body and meta in the cache.
func (c *Cache) Put(url, profile, format, session, contentKey string, body []byte, meta Meta) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := c.key(url, profile, format, session, contentKey)
	dir := filepath.Join(c.dir, k[:2])
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	meta.StoredAt = time.Now()
	meta.TTL = c.ttl

	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	bodyTmp := c.bodyPath(k) + ".tmp"
	if err := os.WriteFile(bodyTmp, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(bodyTmp, c.bodyPath(k)); err != nil {
		return err
	}

	metaTmp := c.metaPath(k) + ".tmp"
	if err := os.WriteFile(metaTmp, metaData, 0600); err != nil {
		return err
	}
	if err := os.Rename(metaTmp, c.metaPath(k)); err != nil {
		return err
	}

	entrySize := int64(len(body)) + int64(len(metaData))
	c.indexMgr.addEntry(k, entrySize, meta.StoredAt)
	c.indexMgr.save()
	c.evictIfOverSize()
	return nil
}

type cacheEntryInfo struct {
	key      string
	storedAt time.Time
	size     int64
}

func (c *Cache) evictIfOverSize() {
	if time.Since(c.lastEviction) < evictionDebounce {
		return
	}

	totalSize := c.indexMgr.getTotalSize()
	if totalSize <= c.maxSize {
		return
	}

	entries := c.indexMgr.getEntries()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StoredAt.Before(entries[j].StoredAt)
	})

	for _, entry := range entries {
		if totalSize <= c.maxSize*8/10 {
			break
		}
		bodyPath := c.bodyPath(entry.Key)
		metaPath := c.metaPath(entry.Key)
		os.Remove(bodyPath)
		os.Remove(metaPath)
		totalSize -= entry.Size
		c.indexMgr.removeEntry(entry.Key)
		slog.Debug("evicted cache entry", "key", entry.Key)
	}

	c.lastEviction = time.Now()

	if c.indexMgr.needsSave() && time.Since(c.lastSave) > time.Minute {
		c.indexMgr.save()
		c.lastSave = time.Now()
	}
}

// scanCache walks the cache directory and rebuilds entry metadata from
// disk. It is unused in the normal eviction path (which uses the
// index manager) and exists only for startup integrity checks and tests.
func (c *Cache) scanCache() (int64, []cacheEntryInfo) {
	var totalSize int64
	var entries []cacheEntryInfo

	filepath.WalkDir(c.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" || !strings.HasSuffix(path, ".meta.json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var m Meta
		if err := json.Unmarshal(data, &m); err != nil {
			return nil
		}

		key := filepath.Base(path)
		key = key[:len(key)-len(".meta.json")]

		info, err := d.Info()
		if err != nil {
			return nil
		}
		metaSize := info.Size()

		bodyPath := c.bodyPath(key)
		bodyInfo, err := os.Stat(bodyPath)
		bodySize := int64(0)
		if err == nil {
			bodySize = bodyInfo.Size()
		}

		totalSize += metaSize + bodySize
		entries = append(entries, cacheEntryInfo{
			key:      key,
			storedAt: m.StoredAt,
			size:     metaSize + bodySize,
		})
		return nil
	})

	return totalSize, entries
}
