package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEstimateHeuristic(t *testing.T) {
	if Estimate("") != 0 {
		t.Fatal("empty text must estimate 0 tokens")
	}
	// ~80 chars / 4 = 20 tokens; the heuristic is deterministic.
	if got := Estimate(strings.Repeat("a", 80)); got != 20 {
		t.Fatalf("Estimate(80 chars) = %d, want 20", got)
	}
	if got := Estimate("hi"); got != 1 {
		t.Fatalf("Estimate(2 chars) = %d, want 1 (floor)", got)
	}
	// Unicode counts runes, not bytes.
	if got := Estimate(strings.Repeat("ä", 40)); got != 10 {
		t.Fatalf("Estimate(40 runes) = %d, want 10", got)
	}
}

func TestTruncateStaysWithinBudget(t *testing.T) {
	content := []byte(strings.Repeat("0123456789", 2000)) // 20k chars = 5000 tokens
	head, foot, returned, total, truncated := Truncate(content, 500)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if total != 5000 {
		t.Fatalf("total = %d, want 5000", total)
	}
	if returned > 500 {
		t.Fatalf("returned %d tokens exceeds the 500 budget", returned)
	}
	if len(head) == 0 || len(foot) == 0 {
		t.Fatal("head and foot must both be non-empty")
	}
	if !strings.HasPrefix(string(head), "0123456789") || !strings.HasSuffix(string(foot), "0123456789") {
		t.Fatal("head/foot slices are not from the content edges")
	}
}

func TestTruncateNoopUnderBudget(t *testing.T) {
	content := []byte("small output")
	head, foot, returned, total, truncated := Truncate(content, 1000)
	if truncated {
		t.Fatal("small content must not be truncated")
	}
	if string(head) != string(content) || foot != nil {
		t.Fatalf("head/foot = %q / %q", head, foot)
	}
	if returned != total {
		t.Fatalf("returned %d != total %d", returned, total)
	}
}

func TestCacheStoreLoadListClear(t *testing.T) {
	cache := NewCache(t.TempDir(), time.Hour)
	id, err := cache.Store([]byte("full content"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "out_") {
		t.Fatalf("id = %q, want out_ prefix", id)
	}
	data, err := cache.Load(id)
	if err != nil || string(data) != "full content" {
		t.Fatalf("Load = %q, err = %v", data, err)
	}
	entries, err := cache.List()
	if err != nil || len(entries) != 1 || entries[0].ID != id || entries[0].Bytes != int64(len("full content")) {
		t.Fatalf("List = %+v, err = %v", entries, err)
	}
	if entries[0].Expired {
		t.Fatal("fresh entry must not be expired")
	}
	if err := cache.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(id); err == nil {
		t.Fatal("Load after Clear must fail")
	}
}

func TestCacheExpiry(t *testing.T) {
	cache := NewCache(t.TempDir(), 0) // TTL 0 disables expiry
	id, err := cache.Store([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Load(id); err != nil {
		t.Fatalf("no-TTL entry must stay readable: %v", err)
	}

	expiring := NewCache(t.TempDir(), time.Hour)
	id2, err := expiring.Store([]byte("y"))
	if err != nil {
		t.Fatal(err)
	}
	// Rewrite the metadata with an already-past expiry (deterministic).
	past := cacheMeta{ID: id2, CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour)}
	raw, _ := json.Marshal(past)
	if err := os.WriteFile(filepath.Join(expiring.Root, id2+".meta.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := expiring.Load(id2); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired entry must fail with an expired error, got %v", err)
	}
	entries, err := expiring.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("List must purge expired entries: %+v, %v", entries, err)
	}
}

func TestLineRange(t *testing.T) {
	content := []byte("line1\nline2\nline3\nline4")
	if got := LineRange(content, 2, 3); got != "line2\nline3" {
		t.Fatalf("range 2-3 = %q", got)
	}
	if got := LineRange(content, 0, 0); got != string(content) {
		t.Fatalf("range 0-0 = %q", got)
	}
	if got := LineRange(content, 3, 99); got != "line3\nline4" {
		t.Fatalf("clamped range = %q", got)
	}
	if got := LineRange(content, 9, 10); got != "" {
		t.Fatalf("out-of-range = %q", got)
	}
}

func TestApplyTruncatesAndStores(t *testing.T) {
	cache := NewCache(t.TempDir(), time.Hour)
	payload := map[string]any{"body": strings.Repeat("x", 20000)}
	out, err := Apply(cache, payload, 100)
	if err != nil {
		t.Fatal(err)
	}
	marker, ok := out.(Marker)
	if !ok {
		t.Fatalf("out = %#v, want Marker", out)
	}
	if !marker.Truncated || marker.CacheID == "" || marker.TokensTotal == 0 || marker.Head == "" || marker.Foot == "" {
		t.Fatalf("marker = %+v", marker)
	}
	if !strings.Contains(marker.Hint, marker.CacheID) {
		t.Fatalf("hint %q does not reference cache id %q", marker.Hint, marker.CacheID)
	}
	stored, err := cache.Load(marker.CacheID)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(stored, &roundTrip); err != nil {
		t.Fatalf("stored payload is not valid JSON: %v", err)
	}
	if roundTrip["body"] != payload["body"] {
		t.Fatal("stored payload differs from the original")
	}
}

func TestApplyNoopWithinBudget(t *testing.T) {
	cache := NewCache(t.TempDir(), time.Hour)
	payload := map[string]any{"body": "small"}
	out, err := Apply(cache, payload, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, isMarker := out.(Marker); isMarker {
		t.Fatal("small payload must not be truncated")
	}
}

func TestApplyFailsClosedWithoutCache(t *testing.T) {
	payload := map[string]any{"body": strings.Repeat("x", 20000)}
	if _, err := Apply(nil, payload, 100); err == nil || !strings.Contains(err.Error(), "no output cache") {
		t.Fatalf("err = %v, want fail-closed error", err)
	}
}
