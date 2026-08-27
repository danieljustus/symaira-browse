// Package budget implements the token-budget and truncate-and-store layer
// (issue #23, B-19): a rough token estimator, head+foot
// truncation and a TTL'd cache that holds the full output under the cache
// out directory so no command ever writes more than its budget into the
// agent context.
package budget

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Estimate returns a rough token estimate for text. The heuristic counts
// characters (not bytes) and divides by four, the standard approximation
// for English token streams; it is deterministic and dependency-free.
func Estimate(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	tokens := runes / 4
	if tokens == 0 {
		return 1
	}
	return tokens
}

// Truncate splits content into a head and a foot that together stay within
// maxTokens (60% head / 40% foot), using the Estimate heuristic. It returns
// the slices, the estimated returned and total token counts, and whether
// truncation happened at all.
func Truncate(content []byte, maxTokens int) (head, foot []byte, tokensReturned, tokensTotal int, truncated bool) {
	tokensTotal = Estimate(string(content))
	if tokensTotal <= maxTokens {
		return content, nil, tokensTotal, tokensTotal, false
	}
	runes := []rune(string(content))
	headBudget := maxTokens * 6 / 10
	footBudget := maxTokens - headBudget
	headEnd := headBudget * 4
	if headEnd > len(runes) {
		headEnd = len(runes)
	}
	footStart := len(runes) - footBudget*4
	if footStart < headEnd {
		footStart = headEnd
	}
	head = []byte(string(runes[:headEnd]))
	foot = []byte(string(runes[footStart:]))
	return head, foot, Estimate(string(head)) + Estimate(string(foot)), tokensTotal, true
}

// Marker is the stable truncation payload delivered alongside head and foot:
// the handle lets callers fetch the full content or
// exact line ranges from the cache.
type Marker struct {
	Truncated      bool   `json:"truncated"`
	TokensReturned int    `json:"tokens_returned"`
	TokensTotal    int    `json:"tokens_total"`
	CacheID        string `json:"cache_id"`
	Hint           string `json:"hint"`
	Head           string `json:"head,omitempty"`
	Foot           string `json:"foot,omitempty"`
}

// Errors returned by the cache.
var (
	ErrNotFound = errors.New("cache entry not found")
	ErrExpired  = errors.New("cache entry expired")
)

// cacheMeta is the sidecar file describing one cache entry.
type cacheMeta struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Entry describes one cache entry for cache list.
type Entry struct {
	ID        string    `json:"id"`
	Bytes     int64     `json:"bytes"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Expired   bool      `json:"expired"`
}

// Cache stores truncated output blobs under Root with a per-entry TTL.
// Entries are single files named <id>.json plus <id>.meta.json; expired
// entries are purged lazily on access and explicitly by PurgeExpired.
type Cache struct {
	Root string
	TTL  time.Duration

	mu sync.Mutex
}

// NewCache creates a cache rooted at dir with the given TTL. A non-positive
// TTL disables expiry (entries live until cleared).
func NewCache(dir string, ttl time.Duration) *Cache {
	return &Cache{Root: dir, TTL: ttl}
}

// NewID returns a short unique cache id (out_<12 hex chars>).
func (c *Cache) NewID() string {
	now := time.Now().UnixNano()
	var buf [6]byte
	for i := 0; i < 6; i++ {
		buf[i] = byte(now >> (8 * i))
	}
	return "out_" + hex.EncodeToString(buf[:])
}

// Store writes data under a fresh id and returns the id.
func (c *Cache) Store(data []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.NewID()
	now := time.Now()
	expires := time.Time{}
	if c.TTL > 0 {
		expires = now.Add(c.TTL)
	}
	if err := os.MkdirAll(c.Root, 0o700); err != nil {
		return "", fmt.Errorf("create cache directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(c.Root, id+".json"), data, 0o600); err != nil {
		return "", fmt.Errorf("write cache entry: %w", err)
	}
	meta, _ := json.Marshal(cacheMeta{ID: id, CreatedAt: now, ExpiresAt: expires})
	if err := os.WriteFile(filepath.Join(c.Root, id+".meta.json"), meta, 0o600); err != nil {
		return "", fmt.Errorf("write cache metadata: %w", err)
	}
	return id, nil
}

// Load returns the stored content for id. Expired entries are removed and
// reported as ErrExpired.
func (c *Cache) Load(id string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	meta, err := c.loadMeta(id)
	if err != nil {
		return nil, err
	}
	if !meta.ExpiresAt.IsZero() && time.Now().After(meta.ExpiresAt) {
		_ = c.remove(id)
		return nil, fmt.Errorf("%w: %s", ErrExpired, id)
	}
	data, err := os.ReadFile(filepath.Join(c.Root, id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	return data, nil
}

// List returns all entries, oldest first, with their expiry state. Expired
// entries are purged first so the listing only shows live entries.
func (c *Cache) List() ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.purgeExpiredLocked()
	entries, err := filepath.Glob(filepath.Join(c.Root, "*.meta.json"))
	if err != nil {
		return nil, err
	}
	var out []Entry
	now := time.Now()
	for _, metaPath := range entries {
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta cacheMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		info, err := os.Stat(filepath.Join(c.Root, meta.ID+".json"))
		if err != nil {
			continue
		}
		expired := !meta.ExpiresAt.IsZero() && now.After(meta.ExpiresAt)
		out = append(out, Entry{ID: meta.ID, Bytes: info.Size(), CreatedAt: meta.CreatedAt, ExpiresAt: meta.ExpiresAt, Expired: expired})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Clear removes every cache entry (content and metadata).
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := filepath.Glob(filepath.Join(c.Root, "*"))
	if err != nil {
		return err
	}
	for _, path := range entries {
		_ = os.Remove(path)
	}
	return nil
}

// PurgeExpired removes entries whose TTL has passed.
func (c *Cache) PurgeExpired() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.purgeExpiredLocked()
}

func (c *Cache) purgeExpiredLocked() error {
	if c.TTL <= 0 {
		return nil
	}
	entries, err := filepath.Glob(filepath.Join(c.Root, "*.meta.json"))
	if err != nil {
		return err
	}
	now := time.Now()
	for _, metaPath := range entries {
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta cacheMeta
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		if !meta.ExpiresAt.IsZero() && now.After(meta.ExpiresAt) {
			_ = c.remove(meta.ID)
		}
	}
	return nil
}

func (c *Cache) loadMeta(id string) (cacheMeta, error) {
	raw, err := os.ReadFile(filepath.Join(c.Root, id+".meta.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return cacheMeta{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return cacheMeta{}, err
	}
	var meta cacheMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return cacheMeta{}, err
	}
	return meta, nil
}

func (c *Cache) remove(id string) error {
	_ = os.Remove(filepath.Join(c.Root, id+".json"))
	_ = os.Remove(filepath.Join(c.Root, id+".meta.json"))
	return nil
}

// Apply truncates data to the token budget when its JSON serialization
// exceeds maxTokens. The full serialization is stored in the cache and the
// returned Marker carries head+foot plus the cache handle. A nil cache or a
// cache write failure is an error: the budget must never silently fall back
// to returning the full payload.
func Apply(cache *Cache, data any, maxTokens int) (any, error) {
	full, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("serialize output for token budget: %w", err)
	}
	if maxTokens <= 0 {
		return data, nil
	}
	head, foot, returned, total, truncated := Truncate(full, maxTokens)
	if !truncated {
		return data, nil
	}
	if cache == nil {
		return nil, errors.New("token budget exceeded but no output cache is configured")
	}
	id, err := cache.Store(full)
	if err != nil {
		return nil, fmt.Errorf("token budget exceeded and the output cache is unavailable: %w", err)
	}
	return Marker{
		Truncated:      true,
		TokensReturned: returned,
		TokensTotal:    total,
		CacheID:        id,
		Hint:           fmt.Sprintf("symbrowse cache get %s --range 40-120", id),
		Head:           string(head),
		Foot:           string(foot),
	}, nil
}

// LineRange extracts the 1-indexed inclusive line range a..b from content.
// Both bounds are clamped to the content; a==0 means "from the first line"
// and b==0 means "to the last line".
func LineRange(content []byte, a, b int) string {
	lines := strings.Split(string(content), "\n")
	if a < 1 {
		a = 1
	}
	if b < a || b > len(lines) {
		b = len(lines)
	}
	if a > len(lines) {
		return ""
	}
	return strings.Join(lines[a-1:b], "\n")
}
