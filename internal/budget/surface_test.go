package budget

import (
	"strings"
	"testing"
)

func TestApplyWithHintUsesMCPRetrievalTool(t *testing.T) {
	cache := NewCache(t.TempDir(), 0)
	data := map[string]any{"content": strings.Repeat("x", 20000)}
	result, err := ApplyWithHint(cache, data, 10, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	marker, ok := result.(Marker)
	if !ok || !marker.Truncated {
		t.Fatalf("result = %#v, want truncated Marker", result)
	}
	if !strings.Contains(marker.Hint, "cache_get") || strings.Contains(marker.Hint, "symbrowse cache get") {
		t.Fatalf("MCP hint = %q, want cache_get route", marker.Hint)
	}
	if _, err := cache.Load(marker.CacheID); err != nil {
		t.Fatalf("stored cache id %q: %v", marker.CacheID, err)
	}
}
