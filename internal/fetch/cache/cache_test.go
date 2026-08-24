package cache_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/fetch/cache"
)

func newTempCache(t *testing.T, ttl time.Duration) *cache.Cache {
	t.Helper()
	dir := t.TempDir()
	return cache.New(dir, ttl, 0)
}

func TestCacheGetMiss(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)
	_, _, ok := c.Get("https://example.com", "chrome", "markdown", "", "")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestCachePutAndGet(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)
	body := []byte("hello world")
	meta := cache.Meta{StatusCode: 200, FinalURL: "https://example.com", ContentType: "text/html"}

	if err := c.Put("https://example.com", "chrome", "markdown", "", "", body, meta); err != nil {
		t.Fatal(err)
	}

	gotBody, gotMeta, ok := c.Get("https://example.com", "chrome", "markdown", "", "")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if string(gotBody) != "hello world" {
		t.Errorf("expected body %q, got %q", "hello world", gotBody)
	}
	if gotMeta.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", gotMeta.StatusCode)
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	ttl := 50 * time.Millisecond
	c := newTempCache(t, ttl)

	if err := c.Put("https://example.com", "chrome", "markdown", "", "", []byte("data"), cache.Meta{StatusCode: 200}); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := c.Get("https://example.com", "chrome", "markdown", "", ""); !ok {
		t.Fatal("expected immediate hit")
	}

	time.Sleep(ttl + 20*time.Millisecond)

	if _, _, ok := c.Get("https://example.com", "chrome", "markdown", "", ""); ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

func TestCacheDifferentProfilesDontCollide(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)
	c.Put("https://example.com", "chrome", "markdown", "", "", []byte("chrome-body"), cache.Meta{StatusCode: 200})
	c.Put("https://example.com", "firefox", "markdown", "", "", []byte("firefox-body"), cache.Meta{StatusCode: 200})

	b1, _, ok1 := c.Get("https://example.com", "chrome", "markdown", "", "")
	b2, _, ok2 := c.Get("https://example.com", "firefox", "markdown", "", "")

	if !ok1 || !ok2 {
		t.Fatal("expected both hits")
	}
	if string(b1) != "chrome-body" || string(b2) != "firefox-body" {
		t.Errorf("cache collision: %q %q", b1, b2)
	}
}

func TestCacheAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir, 15*time.Minute, 0)
	c.Put("https://example.com", "chrome", "markdown", "", "", []byte("data"), cache.Meta{StatusCode: 200})

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			name := d.Name()
			if len(name) > 4 && name[len(name)-4:] == ".tmp" {
				t.Errorf("stale .tmp file found: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCacheDifferentFormatsDontCollide(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)
	c.Put("https://example.com", "chrome", "markdown", "", "", []byte("md-body"), cache.Meta{StatusCode: 200})
	c.Put("https://example.com", "chrome", "json", "", "", []byte("json-body"), cache.Meta{StatusCode: 200})
	c.Put("https://example.com", "chrome", "text", "", "", []byte("text-body"), cache.Meta{StatusCode: 200})

	b1, _, ok1 := c.Get("https://example.com", "chrome", "markdown", "", "")
	b2, _, ok2 := c.Get("https://example.com", "chrome", "json", "", "")
	b3, _, ok3 := c.Get("https://example.com", "chrome", "text", "", "")

	if !ok1 || !ok2 || !ok3 {
		t.Fatal("expected all three hits")
	}
	if string(b1) != "md-body" {
		t.Errorf("expected md-body, got %q", b1)
	}
	if string(b2) != "json-body" {
		t.Errorf("expected json-body, got %q", b2)
	}
	if string(b3) != "text-body" {
		t.Errorf("expected text-body, got %q", b3)
	}
}

func TestCacheDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	c := cache.New(cacheDir, 15*time.Minute, 0)
	_ = c // ensure dir created

	info, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("expected 0700 permissions, got %o", info.Mode().Perm())
	}
}

func TestCacheDirPermissionsTightened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	os.MkdirAll(cacheDir, 0755) // intentionally permissive

	cache.New(cacheDir, 15*time.Minute, 0)

	info, err := os.Stat(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("expected permissions tightened to 0700, got %o", info.Mode().Perm())
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			url := "https://example.com/page" + string(rune('A'+i%26))
			body := []byte("body")
			meta := cache.Meta{StatusCode: 200}
			c.Put(url, "chrome", "markdown", "", "", body, meta)
			c.Get(url, "chrome", "markdown", "", "")
		}(i)
	}
	wg.Wait()
}

func TestCacheDifferentContentKeysDontCollide(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)

	c.Put("https://example.com", "chrome", "markdown", "", "mc=500 il=false ct=500 mi=125",
		[]byte("small-body"), cache.Meta{StatusCode: 200})
	c.Put("https://example.com", "chrome", "markdown", "", "mc=20000 il=false ct=500 mi=5000",
		[]byte("large-body"), cache.Meta{StatusCode: 200})

	b1, _, ok1 := c.Get("https://example.com", "chrome", "markdown", "", "mc=500 il=false ct=500 mi=125")
	b2, _, ok2 := c.Get("https://example.com", "chrome", "markdown", "", "mc=20000 il=false ct=500 mi=5000")

	if !ok1 || !ok2 {
		t.Fatal("expected both hits with different content keys")
	}
	if string(b1) != "small-body" {
		t.Errorf("expected small-body, got %q", b1)
	}
	if string(b2) != "large-body" {
		t.Errorf("expected large-body, got %q", b2)
	}
}

func TestCacheSameContentKeyHits(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)
	ck := "mc=20000 il=true ct=500 mi=5000"

	c.Put("https://example.com", "chrome", "markdown", "", ck,
		[]byte("rendered-output"), cache.Meta{StatusCode: 200})

	gotBody, _, ok := c.Get("https://example.com", "chrome", "markdown", "", ck)
	if !ok {
		t.Fatal("expected cache hit with same content key")
	}
	if string(gotBody) != "rendered-output" {
		t.Errorf("expected rendered-output, got %q", gotBody)
	}
}

func TestCacheLinkToggleMisses(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)

	c.Put("https://example.com", "chrome", "markdown", "", "mc=20000 il=false ct=500 mi=5000",
		[]byte("no-links"), cache.Meta{StatusCode: 200})

	_, _, ok := c.Get("https://example.com", "chrome", "markdown", "", "mc=20000 il=true ct=500 mi=5000")
	if ok {
		t.Error("expected cache miss when include_links differs")
	}
}

func TestCacheVersionPrefixInvalidatesOldEntries(t *testing.T) {
	c := newTempCache(t, 15*time.Minute)

	_, _, ok := c.Get("https://example.com", "chrome", "markdown", "", "old-key")
	if ok {
		t.Error("expected miss for old-format keys (no version prefix)")
	}
}
