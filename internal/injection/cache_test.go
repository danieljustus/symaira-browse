package injection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCustomMatcherCacheRefreshesForChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "patterns.txt")
	writePatterns := func(pattern string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(pattern+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writePatterns("approve the first request")
	first, err := loadMatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := loadMatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	if cached != first {
		t.Fatal("unchanged pattern file did not reuse its cached matcher")
	}

	writePatterns("approve the second request")
	if err := os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second, err := loadMatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("changed pattern file reused the old matcher")
	}

	warnings, err := Scan("<html><body>approve the second request</body></html>", ScanOptions{PatternsFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Excerpt, "approve the second request") {
		t.Fatalf("warnings = %+v, want the refreshed pattern", warnings)
	}

	customMatcherMu.RLock()
	defer customMatcherMu.RUnlock()
	entries := 0
	for key := range customMatcherCache {
		if key == path {
			entries++
		}
	}
	if entries != 1 {
		t.Fatalf("cache entries for %q = %d, want 1", path, entries)
	}
}
