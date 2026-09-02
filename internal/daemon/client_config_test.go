package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDaemonLogPathFollowsResolvedStateDirectory(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	stateDir := filepath.Join(home, "configured-state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("SYMBROWSE_STATE_DIR", "")
	t.Setenv("SYMBROWSE_DAEMON_LOG", "")
	if err := os.MkdirAll(filepath.Join(configHome, "symbrowse"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "symbrowse", "config.toml"), []byte("state_dir = \""+filepath.ToSlash(stateDir)+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, want := DefaultDaemonLogPath(), filepath.Join(stateDir, "daemon.log"); got != want {
		t.Fatalf("DefaultDaemonLogPath() = %q, want %q", got, want)
	}
}

func TestExplicitDaemonLogPathWins(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit.log")
	t.Setenv("SYMBROWSE_DAEMON_LOG", explicit)
	if got := DefaultDaemonLogPath(); got != explicit {
		t.Fatalf("DefaultDaemonLogPath() = %q, want %q", got, explicit)
	}
}
