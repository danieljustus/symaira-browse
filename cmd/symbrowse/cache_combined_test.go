package main

import (
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/budget"
	fetchcache "github.com/danieljustus/symaira-browse/internal/fetch/cache"
)

func TestCombinedCacheEntriesAndClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cacheRoot := t.TempDir()
	t.Setenv("SYMBROWSE_CACHE_DIR", cacheRoot)
	t.Setenv("SYMBROWSE_CACHE_TTL_HOURS", "1")

	outputCache := budget.NewCache(cacheRoot+"/out", time.Hour)
	outputID, err := outputCache.Store([]byte("full output"))
	if err != nil {
		t.Fatal(err)
	}
	responseCache := fetchcache.New(cacheRoot+"/fetch", time.Hour, 0)
	if err := responseCache.Put("https://example.com", "chrome", "markdown", "", "key", []byte("response"), fetchcache.Meta{URL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}

	entries, err := allCacheEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("combined entries = %+v, want output and fetch entries", entries)
	}
	seenOutput, seenFetch := false, false
	var fetchID string
	for _, entry := range entries {
		switch entry.Kind {
		case "output":
			seenOutput = entry.ID == outputID
		case "fetch-response":
			seenFetch = true
			fetchID = entry.ID
		}
	}
	if !seenOutput || !seenFetch {
		t.Fatalf("combined entries = %+v", entries)
	}
	if content, err := loadCacheContent(outputID); err != nil || string(content) != "full output" {
		t.Fatalf("output cache read = %q, err = %v", content, err)
	}
	if content, err := loadCacheContent(fetchID); err != nil || string(content) != "response" {
		t.Fatalf("fetch cache read = %q, err = %v", content, err)
	}

	cleared, err := clearAllCaches()
	if err != nil || cleared != 2 {
		t.Fatalf("clearAllCaches = %d, err = %v", cleared, err)
	}
	if entries, err := allCacheEntries(); err != nil || len(entries) != 0 {
		t.Fatalf("entries after clear = %+v, err = %v", entries, err)
	}
}
