package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setProfileHome points every platform home lookup at dir so Discover and
// Resolve run against a deterministic, throwaway Chrome profile tree.
func setProfileHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("LOCALAPPDATA", dir)
}

// chromeUserDataRoot mirrors userDataRoot's platform layout under a test home.
func chromeUserDataRoot(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	case "windows":
		return filepath.Join(home, "Google", "Chrome", "User Data")
	default:
		return filepath.Join(home, ".config", "google-chrome")
	}
}

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

// TestDiscoverEmptyWhenRootMissing covers the error branch of Discover: a
// missing user-data root yields an empty list, never an error.
func TestDiscoverEmptyWhenRootMissing(t *testing.T) {
	setProfileHome(t, t.TempDir())
	if got := Discover(); got != nil {
		t.Fatalf("Discover() = %#v, want nil when the user-data root is missing", got)
	}
}

// TestDiscoverFindsProfilesSortedDefaultFirst covers the full Discover happy
// path: profile marker detection, internal-dir and non-directory skipping,
// IsDefault classification and the Default-first name sort.
func TestDiscoverFindsProfilesSortedDefaultFirst(t *testing.T) {
	home := t.TempDir()
	setProfileHome(t, home)
	root := chromeUserDataRoot(home)
	for _, name := range []string{"Default", "Profile 1", "Profile 2"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Preferences"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Internal dirs and non-directory entries must be skipped.
	if err := os.MkdirAll(filepath.Join(root, "Cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Profile 3"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Discover()
	if len(got) != 3 {
		t.Fatalf("Discover() = %#v, want 3 profiles", got)
	}
	if got[0].Name != "Default" || !got[0].IsDefault {
		t.Fatalf("first profile = %#v, want Default first with IsDefault", got[0])
	}
	if got[1].Name != "Profile 1" || got[1].IsDefault {
		t.Fatalf("second profile = %#v, want Profile 1 without IsDefault", got[1])
	}
	if got[2].Name != "Profile 2" {
		t.Fatalf("third profile = %#v, want Profile 2", got[2])
	}
	for _, profile := range got {
		if profile.Browser != "chrome" {
			t.Fatalf("profile %q browser = %q", profile.Name, profile.Browser)
		}
		if want := filepath.Join(root, profile.Name); profile.Path != want {
			t.Fatalf("profile %q path = %q, want %q", profile.Name, profile.Path, want)
		}
	}
}

// TestResolveNameLookupAndErrorBranches covers the remaining Resolve error
// branches: name lookup hit/miss, path-like arguments, and non-directory
// paths rejected with os.ErrNotExist.
func TestResolveNameLookupAndErrorBranches(t *testing.T) {
	home := t.TempDir()
	setProfileHome(t, home)
	root := chromeUserDataRoot(home)
	defaultDir := filepath.Join(root, "Default")
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultDir, "Preferences"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Name lookup hit resolves against the discovered profiles.
	path, byName, err := Resolve("Default")
	if err != nil || !byName || path != defaultDir {
		t.Fatalf("Resolve(Default) = %q, %v, %v", path, byName, err)
	}
	// Name lookup miss yields os.ErrNotExist.
	if _, byName, err := Resolve("no-such-profile"); !errors.Is(err, os.ErrNotExist) || byName {
		t.Fatalf("Resolve(missing name) = %v (byName %v), want os.ErrNotExist", err, byName)
	}
	// A path-like relative argument goes through Abs and fails on Stat.
	if _, _, err := Resolve(filepath.Join("missing", "profile")); err == nil {
		t.Fatal("Resolve(relative missing path) accepted")
	}
	// A regular file is not a profile directory.
	plainFile := filepath.Join(home, "not-a-profile")
	if err := os.WriteFile(plainFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resolve(plainFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Resolve(file) = %v, want os.ErrNotExist", err)
	}
}
