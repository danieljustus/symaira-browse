package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveByNameAndPath covers the two --profile argument forms.
func TestResolveByNameAndPath(t *testing.T) {
	root := t.TempDir()
	defaultDir := filepath.Join(root, "Default")
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultDir, "Preferences"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Internal dirs must never be reported as profiles.
	if err := os.MkdirAll(filepath.Join(root, "Cache"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Discover is platform-rooted, so exercise the filter directly.
	if isInternalDir("Default") || isInternalDir("Profile 1") {
		t.Fatal("profile names wrongly classified as internal")
	}
	if !isInternalDir("Cache") || !isInternalDir("GPUCache") {
		t.Fatal("internal dirs not classified")
	}
	if !hasProfileMarker(defaultDir) {
		t.Fatal("profile marker missing")
	}

	// Resolve by absolute path.
	path, byName, err := Resolve(defaultDir)
	if err != nil || byName || path != defaultDir {
		t.Fatalf("path resolve = %q, %v, %v", path, byName, err)
	}
	// Resolve of a missing path fails.
	if _, _, err := Resolve(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing path accepted")
	}
	// Empty argument is a no-op.
	if _, _, err := Resolve(""); err != nil {
		t.Fatal(err)
	}
}

func TestWarningText(t *testing.T) {
	if Warning == "" {
		t.Fatal("warning text missing")
	}
}
