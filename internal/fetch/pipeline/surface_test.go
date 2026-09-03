package pipeline

import "testing"

func TestCacheIDFromOutput(t *testing.T) {
	const id = "out_0123456789ab"
	if got := CacheIDFromOutput("content\n\n--- Full text stored: cache_id=" + id + " ---"); got != id {
		t.Fatalf("CacheIDFromOutput = %q, want %q", got, id)
	}
	if got := CacheIDFromOutput("--- Full text stored: /tmp/legacy.txt (offset=10) ---"); got != "" {
		t.Fatalf("legacy footer id = %q, want empty", got)
	}
	if got := CacheIDFromOutput("cache_id=out_not-valid"); got != "" {
		t.Fatalf("invalid id = %q, want empty", got)
	}
}
