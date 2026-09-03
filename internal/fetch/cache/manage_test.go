package cache

import (
	"strings"
	"testing"
	"time"
)

func TestEntriesLoadKeyAndClear(t *testing.T) {
	cache := New(t.TempDir(), time.Hour, 0)
	const contentKey = "mc=20000 il=false ct=500 mi=5000"
	if err := cache.Put("https://example.com/page", "chrome", "markdown", "", contentKey, []byte("cached response"), Meta{
		URL:        "https://example.com/page",
		FinalURL:   "https://example.com/page",
		StatusCode: 200,
	}); err != nil {
		t.Fatal(err)
	}
	key := cache.key("https://example.com/page", "chrome", "markdown", "", contentKey)

	entries := cache.Entries()
	if len(entries) != 1 || entries[0].Key != key {
		t.Fatalf("entries = %+v, want key %s", entries, key)
	}
	body, err := cache.LoadKey(key)
	if err != nil || string(body) != "cached response" {
		t.Fatalf("LoadKey = %q, err = %v", body, err)
	}
	if _, err := cache.LoadKey("../../secret"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("invalid key error = %v", err)
	}

	if err := cache.Clear(); err != nil {
		t.Fatal(err)
	}
	if entries := cache.Entries(); len(entries) != 0 {
		t.Fatalf("entries after clear = %+v", entries)
	}
	if _, err := cache.LoadKey(key); err == nil {
		t.Fatal("LoadKey after clear must fail")
	}
}
