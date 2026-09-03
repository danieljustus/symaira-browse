package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("fetch cache entry not found")
	ErrExpired  = errors.New("fetch cache entry expired")
)

// Entry is one fetch-response cache entry for user-facing cache inspection.
type Entry struct {
	Key       string
	Bytes     int64
	StoredAt  time.Time
	ExpiresAt time.Time
	Expired   bool
}

// Entries returns live fetch-response entries, oldest first. Expired or
// incomplete entries are removed from the index before returning.
func (c *Cache) Entries() []Entry {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	var result []Entry
	for _, indexed := range c.indexMgr.getEntries() {
		expiresAt := time.Time{}
		if c.ttl > 0 {
			expiresAt = indexed.StoredAt.Add(c.ttl)
		}
		if !expiresAt.IsZero() && now.After(expiresAt) {
			_ = os.Remove(c.bodyPath(indexed.Key))
			_ = os.Remove(c.metaPath(indexed.Key))
			c.indexMgr.removeEntry(indexed.Key)
			continue
		}
		if _, err := os.Stat(c.bodyPath(indexed.Key)); err != nil {
			c.indexMgr.removeEntry(indexed.Key)
			continue
		}
		result = append(result, Entry{
			Key: indexed.Key, Bytes: indexed.Size, StoredAt: indexed.StoredAt,
			ExpiresAt: expiresAt,
		})
	}
	if c.indexMgr.needsSave() {
		_ = c.indexMgr.save()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StoredAt.Before(result[j].StoredAt) })
	return result
}

// LoadKey loads one fetch-response body by its content-addressed key.
func (c *Cache) LoadKey(key string) ([]byte, error) {
	if !validFetchCacheKey(key) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	raw, err := os.ReadFile(c.metaPath(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, err
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	ttl := meta.TTL
	if ttl <= 0 {
		ttl = c.ttl
	}
	if ttl > 0 && c.now().Sub(meta.StoredAt) > ttl {
		return nil, fmt.Errorf("%w: %s", ErrExpired, key)
	}
	body, err := os.ReadFile(c.bodyPath(key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, err
	}
	return body, nil
}

// Clear removes all fetch-response bodies, metadata and temporary files while
// retaining an empty index file for fast subsequent startup.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := filepath.WalkDir(c.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".body") || strings.HasSuffix(path, ".meta.json") || strings.HasSuffix(path, ".tmp") {
			return os.Remove(path)
		}
		return nil
	}); err != nil {
		return err
	}
	c.indexMgr.rebuild(nil, 0)
	return c.indexMgr.save()
}

func validFetchCacheKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	return strings.Trim(key, "0123456789abcdef") == ""
}
