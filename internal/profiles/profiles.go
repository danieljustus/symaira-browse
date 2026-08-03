// Package profiles discovers existing Chrome/Chromium user profiles so
// symbrowse can reuse a human's logged-in sessions (issue B-38).
package profiles

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// Profile describes one discovered browser profile.
type Profile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Browser   string `json:"browser"` // chrome | chromium
	IsDefault bool   `json:"is_default"`
}

// Discover returns the profiles found in the platform's Chrome user-data
// directory. A missing directory yields an empty list, never an error.
func Discover() []Profile {
	root := userDataRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var found []Profile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Profile directories carry a Preferences or a "Default" marker; skip
		// internal dirs (Cache, GPUCache, ...) that are not profiles.
		if isInternalDir(name) {
			continue
		}
		if !hasProfileMarker(filepath.Join(root, name)) {
			continue
		}
		found = append(found, Profile{
			Name:      name,
			Path:      filepath.Join(root, name),
			Browser:   "chrome",
			IsDefault: name == "Default",
		})
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].IsDefault != found[j].IsDefault {
			return found[i].IsDefault
		}
		return found[i].Name < found[j].Name
	})
	return found
}

// Resolve maps a --profile argument (name or path) to an absolute profile
// directory. Names are resolved against the discovered profiles; paths are
// used as-is after absolutization. Returns the path and whether it was
// resolved by name.
func Resolve(arg string) (string, bool, error) {
	if arg == "" {
		return "", false, nil
	}
	if filepath.IsAbs(arg) || looksLikePath(arg) {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return "", false, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", false, err
		}
		if !info.IsDir() {
			return "", false, os.ErrNotExist
		}
		return abs, false, nil
	}
	for _, profile := range Discover() {
		if profile.Name == arg {
			return profile.Path, true, nil
		}
	}
	return "", false, os.ErrNotExist
}

// Warning is the startup warning emitted when a profile is reused: a running
// Chrome locks the profile, and the domain allowlist cannot be enforced for
// the human's own profile.
const Warning = "reusing a Chrome profile: a running Chrome instance locks the profile, and the domain allowlist is not enforceable for a human-owned profile"

func looksLikePath(arg string) bool {
	return filepath.Base(arg) != arg
}

func isInternalDir(name string) bool {
	switch name {
	case "Default", "Profile 1", "Profile 2", "Profile 3", "Profile 4", "Profile 5", "Profile 6", "Profile 7", "Profile 8", "Profile 9", "Profile 10":
		return false
	}
	// Anything not named Default/Profile N is internal (Cache, GPUCache, ...).
	return true
}

func hasProfileMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "Preferences"))
	return err == nil
}

func userDataRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "User Data")
	default:
		return filepath.Join(home, ".config", "google-chrome")
	}
}
