package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvictIfOverSize_NoEviction(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)
	c.maxSize = 1000

	body := []byte("small")
	meta := Meta{StatusCode: 200}
	c.Put("https://example.com", "chrome", "markdown", "", "", body, meta)

	c.evictIfOverSize()

	_, _, ok := c.Get("https://example.com", "chrome", "markdown", "", "")
	if !ok {
		t.Error("expected entry to still exist after eviction check")
	}
}

func TestEvictIfOverSize_EvictsOldest(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)
	c.maxSize = 200

	for i := 0; i < 5; i++ {
		url := "https://example.com/page" + string(rune('A'+i))
		body := make([]byte, 100)
		meta := Meta{StatusCode: 200, StoredAt: time.Now().Add(-time.Duration(i) * time.Hour)}
		c.Put(url, "chrome", "markdown", "", "", body, meta)
	}

	// Reset the eviction debounce so the explicit call below is not skipped.
	c.lastEviction = time.Time{}

	c.evictIfOverSize()

	totalSize := c.indexMgr.getTotalSize()
	if totalSize > c.maxSize {
		t.Errorf("expected cache size under limit, got %d", totalSize)
	}
}

func TestScanCache_Empty(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	totalSize, entries := c.scanCache()
	if totalSize != 0 {
		t.Errorf("expected total size 0, got %d", totalSize)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestScanCache_WithEntries(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	for i := 0; i < 3; i++ {
		url := "https://example.com/page" + string(rune('A'+i))
		body := []byte("body content " + string(rune('A'+i)))
		meta := Meta{StatusCode: 200}
		if err := c.Put(url, "chrome", "markdown", "", "", body, meta); err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	totalSize, entries := c.scanCache()
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	if totalSize <= 0 {
		t.Errorf("expected positive total size, got %d", totalSize)
	}
}

func TestScanCache_CorruptMetaSkipped(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	// Add a valid entry
	body := []byte("valid body")
	meta := Meta{StatusCode: 200}
	c.Put("https://example.com/valid", "chrome", "markdown", "", "", body, meta)

	// Manually create a corrupt meta file
	subdir := filepath.Join(dir, "ab") // assuming key starts with "ab"
	os.MkdirAll(subdir, 0700)
	corruptPath := filepath.Join(subdir, "corrupt.meta.json")
	os.WriteFile(corruptPath, []byte("not valid json"), 0600)

	_, entries := c.scanCache()
	if len(entries) != 1 {
		t.Errorf("expected 1 valid entry, got %d", len(entries))
	}
}

func TestScanCache_MissingBodySkipped(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	// Add a valid entry
	body := []byte("valid body")
	meta := Meta{StatusCode: 200}
	c.Put("https://example.com/valid", "chrome", "markdown", "", "", body, meta)

	// Manually create a meta file without corresponding body
	key := c.key("https://example.com/orphan", "chrome", "markdown", "", "")
	subdir := filepath.Join(dir, key[:2])
	os.MkdirAll(subdir, 0700)
	metaPath := filepath.Join(subdir, key+".meta.json")
	metaData := Meta{StatusCode: 200, StoredAt: time.Now()}
	data, _ := jsonMarshal(metaData)
	os.WriteFile(metaPath, data, 0600)

	_, entries := c.scanCache()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestReconcileIndex_CrossProcessStartup(t *testing.T) {
	dir := t.TempDir()

	// Simulate another process writing cache files directly, with no index.
	c1 := New(dir, 15*time.Minute, 0)
	body := []byte("remote process body")
	meta := Meta{StatusCode: 200, StoredAt: time.Now()}
	metaData, _ := json.Marshal(meta)
	k := c1.key("https://example.com", "chrome", "markdown", "", "")
	subdir := filepath.Join(dir, k[:2])
	os.MkdirAll(subdir, 0700)
	os.WriteFile(filepath.Join(subdir, k+".body"), body, 0600)
	os.WriteFile(filepath.Join(subdir, k+".meta.json"), metaData, 0600)

	// Delete the index so the next New() must reconcile from disk.
	os.Remove(filepath.Join(dir, indexFileName))

	// Create a fresh cache process against the same directory.
	c2 := New(dir, 15*time.Minute, 0)

	gotBody, _, ok := c2.Get("https://example.com", "chrome", "markdown", "", "")
	if !ok {
		t.Fatal("expected cache hit after cross-process reconcile")
	}
	if string(gotBody) != "remote process body" {
		t.Errorf("expected body %q, got %q", "remote process body", gotBody)
	}

	totalSize := c2.indexMgr.getTotalSize()
	if totalSize <= 0 {
		t.Errorf("expected positive total size after reconcile, got %d", totalSize)
	}
}

func TestReconcileIndex_StaleIndexPruned(t *testing.T) {
	dir := t.TempDir()

	// Write one valid entry.
	c1 := New(dir, 15*time.Minute, 0)
	c1.Put("https://example.com/keep", "chrome", "markdown", "", "",
		[]byte("keep body"), Meta{StatusCode: 200})

	// Now corrupt the index: add a phantom entry that has no files on disk.
	phantomKey := "phantom-key-TEST-NOT-A-SECRET"
	c1.indexMgr.addEntry(phantomKey, 9999, time.Now())
	c1.indexMgr.save()

	// A cleanly-parseable index is trusted as-is at startup (New skips the
	// full rescan in that case — see cache.go). Reconciliation of a
	// stale-but-valid index is an explicit operation.
	c2 := New(dir, 15*time.Minute, 0)
	c2.reconcileIndex()

	totalSize := c2.indexMgr.getTotalSize()
	entries := c2.indexMgr.getEntries()
	for _, e := range entries {
		if e.Key == phantomKey {
			t.Error("phantom entry should have been pruned during reconcile")
		}
	}

	// Only the real entry should remain.
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after reconcile, got %d", len(entries))
	}

	gotBody, _, ok := c2.Get("https://example.com/keep", "chrome", "markdown", "", "")
	if !ok || string(gotBody) != "keep body" {
		t.Errorf("expected valid entry to survive reconcile, got %q (ok=%v)", gotBody, ok)
	}

	if totalSize <= 0 {
		t.Errorf("expected positive total size, got %d", totalSize)
	}
}

// TestNew_ValidIndexSkipsFullRescan verifies that a cleanly-parseable
// on-disk index is trusted as-is at startup: files present on disk but not
// recorded in the index are not picked up, because New does not walk the
// cache directory in that case.
func TestNew_ValidIndexSkipsFullRescan(t *testing.T) {
	dir := t.TempDir()

	c1 := New(dir, 15*time.Minute, 0)
	c1.Put("https://example.com/tracked", "chrome", "markdown", "", "",
		[]byte("tracked body"), Meta{StatusCode: 200})

	// Write an extra body+meta pair directly to disk, bypassing Put, so it
	// exists on disk but is absent from the persisted index.
	untrackedKey := c1.key("https://example.com/untracked", "chrome", "markdown", "", "")
	shardDir := filepath.Join(dir, untrackedKey[:2])
	if err := os.MkdirAll(shardDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c1.bodyPath(untrackedKey), []byte("untracked body"), 0600); err != nil {
		t.Fatal(err)
	}
	metaData, _ := json.Marshal(Meta{StatusCode: 200, StoredAt: time.Now(), TTL: 15 * time.Minute})
	if err := os.WriteFile(c1.metaPath(untrackedKey), metaData, 0600); err != nil {
		t.Fatal(err)
	}

	// New should trust the valid on-disk index and skip the rescan that
	// would otherwise discover the untracked entry.
	c2 := New(dir, 15*time.Minute, 0)
	entries := c2.indexMgr.getEntries()
	if len(entries) != 1 {
		t.Errorf("expected index to remain at 1 entry (no rescan), got %d", len(entries))
	}
	for _, e := range entries {
		if e.Key == untrackedKey {
			t.Error("untracked entry should not appear without an explicit reconcile")
		}
	}
}

func TestAddEntry_UpsertNoDuplicates(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	c.Put("https://example.com/page", "chrome", "markdown", "", "",
		[]byte("first version"), Meta{StatusCode: 200})

	entriesAfterFirst := c.indexMgr.getEntries()

	c.Put("https://example.com/page", "chrome", "markdown", "", "",
		[]byte("second version, longer body"), Meta{StatusCode: 200})

	entriesAfterSecond := c.indexMgr.getEntries()

	if len(entriesAfterSecond) != len(entriesAfterFirst) {
		t.Errorf("upsert should not add duplicate: got %d entries, want %d",
			len(entriesAfterSecond), len(entriesAfterFirst))
	}

	k := c.key("https://example.com/page", "chrome", "markdown", "", "")
	count := 0
	for _, e := range entriesAfterSecond {
		if e.Key == k {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 index entry for key, got %d", count)
	}
}

func TestEvictIfOverSize_BelowWatermark(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)
	c.maxSize = 500

	for i := 0; i < 10; i++ {
		url := "https://example.com/page" + string(rune('A'+i))
		body := make([]byte, 100)
		meta := Meta{StatusCode: 200, StoredAt: time.Now().Add(-time.Duration(i) * time.Hour)}
		c.Put(url, "chrome", "markdown", "", "", body, meta)
	}

	c.lastEviction = time.Time{}
	c.evictIfOverSize()

	totalSize := c.indexMgr.getTotalSize()
	watermark := c.maxSize * 8 / 10
	if totalSize > watermark {
		t.Errorf("expected cache size %d <= watermark %d (80%% of %d)",
			totalSize, watermark, c.maxSize)
	}
}

func TestIndexPersistedAfterPut(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	c.Put("https://example.com", "chrome", "markdown", "", "",
		[]byte("persist-me"), Meta{StatusCode: 200})

	data, err := os.ReadFile(filepath.Join(dir, indexFileName))
	if err != nil {
		t.Fatalf("index file not persisted: %v", err)
	}

	var idx cacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("corrupt index file: %v", err)
	}
	if len(idx.Entries) != 1 {
		t.Errorf("expected 1 persisted entry, got %d", len(idx.Entries))
	}
	if idx.TotalSize <= 0 {
		t.Errorf("expected positive persisted total size, got %d", idx.TotalSize)
	}
}

func TestEnsureCacheDir_ReadOnlyParent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write into read-only directories")
	}
	dir := t.TempDir()
	// Make the parent read-only so MkdirAll fails.
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	cacheDir := filepath.Join(dir, "nested", "cache")
	// Should not panic; directory creation silently fails.
	ensureCacheDir(cacheDir)

	_, err := os.Stat(cacheDir)
	if !os.IsNotExist(err) {
		t.Errorf("expected cache dir not to be created, got err=%v", err)
	}
}

func TestDefaultDir_UserHomeDirFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	got := DefaultDir()
	want := filepath.Join(os.TempDir(), "symfetch")
	if got != want {
		t.Errorf("DefaultDir() = %q, want %q", got, want)
	}
}

func TestCacheGet_CorruptMeta(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	body := []byte("hello")
	meta := Meta{StatusCode: 200}
	if err := c.Put("https://example.com", "chrome", "markdown", "", "", body, meta); err != nil {
		t.Fatal(err)
	}

	k := c.key("https://example.com", "chrome", "markdown", "", "")
	if err := os.WriteFile(c.metaPath(k), []byte("not valid json"), 0600); err != nil {
		t.Fatal(err)
	}

	gotBody, gotMeta, ok := c.Get("https://example.com", "chrome", "markdown", "", "")
	if ok {
		t.Error("expected cache miss for corrupt meta")
	}
	if gotBody != nil || gotMeta != nil {
		t.Errorf("expected nil body and meta on corrupt meta, got body=%v meta=%v", gotBody, gotMeta)
	}
}

func TestCacheGet_MissingBody(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	body := []byte("hello")
	meta := Meta{StatusCode: 200}
	if err := c.Put("https://example.com", "chrome", "markdown", "", "", body, meta); err != nil {
		t.Fatal(err)
	}

	k := c.key("https://example.com", "chrome", "markdown", "", "")
	if err := os.Remove(c.bodyPath(k)); err != nil {
		t.Fatal(err)
	}

	gotBody, gotMeta, ok := c.Get("https://example.com", "chrome", "markdown", "", "")
	if ok {
		t.Error("expected cache miss when body file is missing")
	}
	if gotBody != nil || gotMeta != nil {
		t.Errorf("expected nil body and meta on missing body, got body=%v meta=%v", gotBody, gotMeta)
	}
}

func TestCachePut_ReadOnlyDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write into read-only directories")
	}
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	// Create the shard directory and make it read-only.
	k := c.key("https://example.com", "chrome", "markdown", "", "")
	shard := filepath.Join(dir, k[:2])
	if err := os.MkdirAll(shard, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shard, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shard, 0700) })

	err := c.Put("https://example.com", "chrome", "markdown", "", "", []byte("hello"), Meta{StatusCode: 200})
	if err == nil {
		t.Error("expected Put to fail on read-only shard directory")
	}
}

func TestIndexManager_LoadCorruptIndex(t *testing.T) {
	dir := t.TempDir()
	im := newIndexManager(dir)
	if err := os.WriteFile(im.indexPath(), []byte("not valid json"), 0600); err != nil {
		t.Fatal(err)
	}

	valid, err := im.load()
	if err != nil {
		t.Errorf("expected load to swallow corrupt index, got err=%v", err)
	}
	if valid {
		t.Error("expected corrupt index to be reported as invalid")
	}
	if !im.loaded {
		t.Error("expected index manager to be marked loaded after corrupt index")
	}
}

func TestIndexManager_SaveReadOnlyDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write into read-only directories")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })

	im := newIndexManager(dir)
	im.addEntry("key", 1, time.Now())
	if err := im.save(); err == nil {
		t.Error("expected save to fail on read-only directory")
	}
}

func TestCacheReconcile_CorruptIndexRebuilt(t *testing.T) {
	dir := t.TempDir()
	c1 := New(dir, 15*time.Minute, 0)
	c1.Put("https://example.com/keep", "chrome", "markdown", "", "", []byte("keep body"), Meta{StatusCode: 200})

	// Corrupt the persisted index.
	if err := os.WriteFile(filepath.Join(dir, indexFileName), []byte("not valid json"), 0600); err != nil {
		t.Fatal(err)
	}

	c2 := New(dir, 15*time.Minute, 0)

	gotBody, _, ok := c2.Get("https://example.com/keep", "chrome", "markdown", "", "")
	if !ok {
		t.Fatal("expected cache hit after reconciling from disk")
	}
	if string(gotBody) != "keep body" {
		t.Errorf("expected body %q, got %q", "keep body", gotBody)
	}

	entries := c2.indexMgr.getEntries()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after reconcile, got %d", len(entries))
	}
}

func jsonMarshal(v interface{}) ([]byte, error) {
	return []byte(`{"status_code":200}`), nil
}

func TestIndexManager_RemoveEntry_NotFound(t *testing.T) {
	dir := t.TempDir()
	im := newIndexManager(dir)
	im.addEntry("existing-key", 100, time.Now())

	found := im.removeEntry("nonexistent-key")
	if found {
		t.Error("expected false when removing nonexistent key")
	}
	entries := im.getEntries()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry to remain, got %d", len(entries))
	}
}

func TestIndexManager_Load_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read any file")
	}
	dir := t.TempDir()
	im := newIndexManager(dir)
	os.WriteFile(im.indexPath(), []byte("valid"), 0600)

	if err := os.Chmod(im.indexPath(), 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(im.indexPath(), 0600) })

	_, err := im.load()
	if err == nil {
		t.Error("expected error when reading index file with no permissions")
	}
}

func TestIndexManager_Rebuild(t *testing.T) {
	dir := t.TempDir()
	im := newIndexManager(dir)

	entries := []indexEntry{
		{Key: "key1", Size: 100, StoredAt: time.Now()},
		{Key: "key2", Size: 200, StoredAt: time.Now()},
	}
	im.rebuild(entries, 300)

	if im.getTotalSize() != 300 {
		t.Errorf("expected total size 300, got %d", im.getTotalSize())
	}
	got := im.getEntries()
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d", len(got))
	}
}

func TestCachePut_MetaMarshalError(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	body := []byte("body")
	meta := Meta{StatusCode: 200}
	if err := c.Put("https://example.com", "chrome", "markdown", "", "", body, meta); err != nil {
		t.Fatal(err)
	}

	got, _, ok := c.Get("https://example.com", "chrome", "markdown", "", "")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if string(got) != "body" {
		t.Errorf("expected 'body', got %q", got)
	}
}

func TestCacheScanCache_MissingMeta(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 15*time.Minute, 0)

	subdir := filepath.Join(dir, "ab")
	os.MkdirAll(subdir, 0700)
	os.WriteFile(filepath.Join(subdir, "testdata.body"), []byte("body"), 0600)

	totalSize, entries := c.scanCache()
	if totalSize != 0 {
		t.Errorf("expected 0 total size for orphan body, got %d", totalSize)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for orphan body, got %d", len(entries))
	}
}
