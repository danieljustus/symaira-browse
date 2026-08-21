package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateAndStore_ShortContentReturnedWhole(t *testing.T) {
	storeDir := t.TempDir()
	content := "Short content that fits within the limit."

	result, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 1000,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored {
		t.Error("expected stored=false for short content")
	}
	if result != content {
		t.Errorf("expected content returned whole, got:\n%s", result)
	}
	// No file should have been written
	entries, _ := os.ReadDir(storeDir)
	if len(entries) > 0 {
		t.Errorf("expected no files written for short content, got %d entries", len(entries))
	}
}

func TestTruncateAndStore_LongContentHeadTailFooter(t *testing.T) {
	storeDir := t.TempDir()
	// Build content that exceeds 200 chars
	content := strings.Repeat("abcdefghij", 30) // 300 chars

	result, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 200,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stored {
		t.Error("expected stored=true for long content")
	}

	// Should contain the footer with a file path
	if !strings.Contains(result, "Full text stored:") {
		t.Errorf("expected footer with 'Full text stored:', got:\n%s", result)
	}
	if !strings.Contains(result, storeDir) {
		t.Errorf("expected footer to contain store directory path, got:\n%s", result)
	}
	if !strings.Contains(result, "offset=") {
		t.Errorf("expected footer to contain 'offset=', got:\n%s", result)
	}

	// Head+tail content (excluding footer) should be shorter than original.
	// The footer adds length, so compare only the visible content.
	footerIdx := strings.Index(result, "\n\n--- Full text stored:")
	visible := result
	if footerIdx >= 0 {
		visible = result[:footerIdx]
	}
	visibleLen := utf8.RuneCountInString(visible)
	origLen := utf8.RuneCountInString(content)
	if visibleLen >= origLen {
		t.Errorf("expected visible content (%d chars) to be shorter than original (%d chars)", visibleLen, origLen)
	}

	// Should contain head (beginning of content)
	if !strings.HasPrefix(result, "abcdefghij") {
		t.Errorf("expected result to start with head content, got:\n%s", result[:50])
	}

	// A file should have been written
	entries, _ := os.ReadDir(storeDir)
	if len(entries) == 0 {
		t.Error("expected a file to be written for long content")
	}
}

func TestTruncateAndStore_FullTextMatchesOriginal(t *testing.T) {
	storeDir := t.TempDir()
	content := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 50)

	result, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 500,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stored {
		t.Fatal("expected stored=true")
	}

	// Extract file path from footer
	filePath := extractFilePathFromFooter(t, result)
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read stored file: %v", err)
	}
	if string(data) != content {
		t.Errorf("stored file content does not match original:\nexpected len=%d, got len=%d", len(content), len(data))
	}
}

func TestTruncateAndStore_OffsetCorrectness(t *testing.T) {
	storeDir := t.TempDir()
	prefix := strings.Repeat("A", 1300)
	marker := "UNIQUE_TAIL_MARKER_42"
	suffix := strings.Repeat("B", 200)
	content := prefix + marker + suffix

	result, _, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 800,
		StoreDir:  storeDir,
		HeadRatio: 0.5,
		TailRatio: 0.5,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	offset := extractOffsetFromFooter(t, result)
	filePath := extractFilePathFromFooter(t, result)

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read stored file: %v", err)
	}

	if offset >= len(data) {
		t.Fatalf("offset %d exceeds file size %d", offset, len(data))
	}
	window := string(data[offset:])
	if !strings.Contains(window, marker) {
		t.Errorf("expected marker %q in tail window starting at offset %d, not found in window of %d chars", marker, offset, len(window))
	}
}

func TestTruncateAndStore_StoredTextCapped(t *testing.T) {
	storeDir := t.TempDir()
	maxStored := 1000
	// Create content much larger than maxStored
	content := strings.Repeat("X", 5000)

	result, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 500,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: maxStored,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stored {
		t.Fatal("expected stored=true")
	}

	filePath := extractFilePathFromFooter(t, result)
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read stored file: %v", err)
	}
	if len(data) > maxStored {
		t.Errorf("expected stored file to be capped at %d bytes, got %d bytes", maxStored, len(data))
	}
}

func TestTruncateAndStore_HeadTailProportions(t *testing.T) {
	storeDir := t.TempDir()
	head := strings.Repeat("H", 400)
	middle := strings.Repeat("M", 200)
	tail := strings.Repeat("T", 400)
	content := head + middle + tail

	result, _, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 800,
		StoreDir:  storeDir,
		HeadRatio: 0.5,
		TailRatio: 0.5,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	footerIdx := strings.Index(result, "\n\n--- Full text stored:")
	visible := result
	if footerIdx >= 0 {
		visible = result[:footerIdx]
	}
	visible = strings.TrimSpace(visible)

	if !strings.HasPrefix(visible, "HHHH") {
		t.Errorf("expected head content at start, got:\n%s", visible[:50])
	}
	if !strings.HasSuffix(visible, "TTTT") {
		t.Errorf("expected tail content at end, got:\n...%s", visible[len(visible)-50:])
	}
	if strings.Contains(visible, "MMMM") {
		t.Error("expected middle content to be excluded from truncated output")
	}
}

func TestTruncateAndStore_StoreDirCreated(t *testing.T) {
	parentDir := t.TempDir()
	storeDir := filepath.Join(parentDir, "nested", "fulltext")

	content := strings.Repeat("Y", 300)
	_, _, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 100,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(storeDir); os.IsNotExist(err) {
		t.Error("expected store directory to be created")
	}
}

func TestTruncateAndStore_ExactLimitNotTruncated(t *testing.T) {
	storeDir := t.TempDir()
	content := strings.Repeat("Z", 100)

	result, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 100,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored {
		t.Error("expected stored=false when content length equals char limit")
	}
	if result != content {
		t.Errorf("expected content returned whole at exact limit, got len=%d, want len=%d", len(result), len(content))
	}
}

func TestTruncateAndStore_EmptyContent(t *testing.T) {
	storeDir := t.TempDir()

	result, stored, err := TruncateAndStore("", StoreOptions{
		CharLimit: 1000,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored {
		t.Error("expected stored=false for empty content")
	}
	if result != "" {
		t.Errorf("expected empty result, got: %q", result)
	}
}

// --- helpers ---

func extractFilePathFromFooter(t *testing.T, result string) string {
	t.Helper()
	// Footer format: "--- Full text stored: /path/to/file (offset=N) ---"
	idx := strings.Index(result, "Full text stored:")
	if idx < 0 {
		t.Fatalf("no 'Full text stored:' in result")
	}
	after := result[idx+len("Full text stored:"):]
	after = strings.TrimSpace(after)
	// Read until " (offset="
	offsetIdx := strings.Index(after, " (offset=")
	if offsetIdx < 0 {
		t.Fatalf("no ' (offset=' in footer after path: %s", after)
	}
	return after[:offsetIdx]
}

func extractOffsetFromFooter(t *testing.T, result string) int {
	t.Helper()
	idx := strings.Index(result, "offset=")
	if idx < 0 {
		t.Fatalf("no 'offset=' in result")
	}
	after := result[idx+len("offset="):]
	// Read until ")"
	endIdx := strings.Index(after, ")")
	if endIdx < 0 {
		t.Fatalf("no ')' after offset in: %s", after)
	}
	var offset int
	if _, err := fmt.Sscanf(after[:endIdx], "%d", &offset); err != nil {
		t.Fatalf("failed to parse offset: %v", err)
	}
	return offset
}

func TestStoreOptions_SetDefaults_CharLimitZero(t *testing.T) {
	o := StoreOptions{CharLimit: 0, HeadRatio: 0.8, TailRatio: 0.2, MaxStored: 1000}
	o.setDefaults()
	if o.CharLimit != DefaultCharLimit {
		t.Errorf("expected CharLimit=%d, got %d", DefaultCharLimit, o.CharLimit)
	}
}

func TestStoreOptions_SetDefaults_HeadRatioInvalid(t *testing.T) {
	o := StoreOptions{CharLimit: 100, HeadRatio: 0, TailRatio: 0.2, MaxStored: 1000}
	o.setDefaults()
	if o.HeadRatio != 0.8 {
		t.Errorf("expected HeadRatio=0.8, got %f", o.HeadRatio)
	}

	o2 := StoreOptions{CharLimit: 100, HeadRatio: 1.5, TailRatio: 0.2, MaxStored: 1000}
	o2.setDefaults()
	if o2.HeadRatio != 0.8 {
		t.Errorf("expected HeadRatio=0.8 for >=1, got %f", o2.HeadRatio)
	}
}

func TestStoreOptions_SetDefaults_TailRatioInvalid(t *testing.T) {
	o := StoreOptions{CharLimit: 100, HeadRatio: 0.8, TailRatio: 0, MaxStored: 1000}
	o.setDefaults()
	if o.TailRatio != 0.2 {
		t.Errorf("expected TailRatio=0.2, got %f", o.TailRatio)
	}

	o2 := StoreOptions{CharLimit: 100, HeadRatio: 0.8, TailRatio: 1.0, MaxStored: 1000}
	o2.setDefaults()
	if o2.TailRatio != 0.2 {
		t.Errorf("expected TailRatio=0.2 for >=1, got %f", o2.TailRatio)
	}
}

func TestStoreOptions_SetDefaults_MaxStoredZero(t *testing.T) {
	o := StoreOptions{CharLimit: 100, HeadRatio: 0.8, TailRatio: 0.2, MaxStored: 0}
	o.setDefaults()
	if o.MaxStored != DefaultMaxStored {
		t.Errorf("expected MaxStored=%d, got %d", DefaultMaxStored, o.MaxStored)
	}
}

func TestTruncateAndStore_EmptyStoreDir(t *testing.T) {
	content := strings.Repeat("A", 200)
	_, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 100,
		StoreDir:  "",
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 1000,
	})
	if err == nil {
		t.Fatal("expected error for empty store dir")
	}
	if stored {
		t.Error("expected stored=false on error")
	}
}

func TestTruncateAndStore_MkdirAllError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can create directories in read-only locations")
	}

	parentDir := t.TempDir()
	if err := os.Chmod(parentDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parentDir, 0700) })

	storeDir := filepath.Join(parentDir, "subdir")
	content := strings.Repeat("A", 200)
	_, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 100,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 1000,
	})
	if err == nil {
		t.Fatal("expected error for MkdirAll failure")
	}
	if stored {
		t.Error("expected stored=false on error")
	}
}

func TestTruncateAndStore_WriteFileError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write into read-only directories")
	}

	storeDir := t.TempDir()
	if err := os.Chmod(storeDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(storeDir, 0700) })

	content := strings.Repeat("B", 200)
	_, stored, err := TruncateAndStore(content, StoreOptions{
		CharLimit: 100,
		StoreDir:  storeDir,
		HeadRatio: 0.8,
		TailRatio: 0.2,
		MaxStored: 1000,
	})
	if err == nil {
		t.Fatal("expected error for read-only store dir")
	}
	if stored {
		t.Error("expected stored=false on error")
	}
}

func TestTruncateTo_ShortString(t *testing.T) {
	result := truncateTo("abc", 10)
	if result != "abc" {
		t.Errorf("expected 'abc' for short string, got %q", result)
	}
}

func TestTailOf_ShortString(t *testing.T) {
	result := tailOf("abc", 10)
	if result != "abc" {
		t.Errorf("expected 'abc' for short string, got %q", result)
	}
}

func TestComputeTailOffset_ShortString(t *testing.T) {
	offset := computeTailOffset("abc", 10)
	if offset != 0 {
		t.Errorf("expected offset=0 for short string, got %d", offset)
	}
}

func TestCapBytes_UnderLimit(t *testing.T) {
	result := capBytes("hello", 100)
	if result != "hello" {
		t.Errorf("expected 'hello' unchanged, got %q", result)
	}
}

func TestTruncateTo_NExceedsLength(t *testing.T) {
	result := truncateTo("hi", 5)
	if result != "hi" {
		t.Errorf("expected 'hi', got %q", result)
	}
}
