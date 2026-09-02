package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultPathsHonorsXDGHomeOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "xdg-state"))

	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		ConfigDir: filepath.Join(home, "xdg-config", appName),
		CacheDir:  filepath.Join(home, "xdg-cache", appName),
		StateDir:  filepath.Join(home, "xdg-state", appName),
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestLoadWithOverridesIncludesStableEnvironmentSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	for _, name := range []string{
		"SYMBROWSE_LOG_LEVEL", "SYMBROWSE_LOG_FORMAT", "SYMBROWSE_CONFIG_DIR",
		"SYMBROWSE_CACHE_DIR", "SYMBROWSE_STATE_DIR", "SYMBROWSE_EXECUTABLE_PATH",
		"SYMBROWSE_CDP_ENDPOINT", "SYMBROWSE_ALLOWED_DOMAINS", "SYMBROWSE_SSRF",
		"SYMBROWSE_ALLOW_PRIVATE", "SYMBROWSE_HEADLESS", "SYMBROWSE_CACHE_TTL_HOURS",
		"SYMBROWSE_IDLE_TIMEOUT", "SYMBROWSE_OPERATION_TIMEOUT", "SYMBROWSE_READ_TIMEOUT",
		"SYMBROWSE_STATE_EXPIRE_DAYS", "SYMBROWSE_AUTOSAVE", "SYMBROWSE_AUTOSAVE_INTERVAL",
		"SYMBROWSE_AUTOSAVE_KEY", "SYMBROWSE_UPLOAD_DIRS", "SYMBROWSE_DAEMON_LOG",
		"SYMBROWSE_APPROVAL_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("SYMBROWSE_IDLE_TIMEOUT", "120")
	t.Setenv("SYMBROWSE_OPERATION_TIMEOUT", "45")
	t.Setenv("SYMBROWSE_READ_TIMEOUT", "90")
	t.Setenv("SYMBROWSE_STATE_EXPIRE_DAYS", "14")
	t.Setenv("SYMBROWSE_AUTOSAVE", "always")
	t.Setenv("SYMBROWSE_AUTOSAVE_INTERVAL", "7")
	t.Setenv("SYMBROWSE_AUTOSAVE_KEY", "session-state")
	t.Setenv("SYMBROWSE_UPLOAD_DIRS", "/tmp/uploads, /tmp/second")
	t.Setenv("SYMBROWSE_DAEMON_LOG", filepath.Join(home, "daemon.log"))
	t.Setenv("SYMBROWSE_APPROVAL_TIMEOUT", "12")

	result, err := LoadWithOverrides(FlagOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.IdleTimeoutSeconds != 120 || result.Config.OperationTimeoutSeconds != 45 || result.Config.ReadTimeoutSeconds != 90 {
		t.Fatalf("timeouts = %#v", result.Config)
	}
	if result.Config.StateExpireDays != 14 || result.Config.AutosavePolicy != "always" || result.Config.AutosaveIntervalSeconds != 7 || result.Config.AutosaveKey != "session-state" {
		t.Fatalf("state/autosave settings = %#v", result.Config)
	}
	if !reflect.DeepEqual(result.Config.UploadDirs, []string{"/tmp/uploads", "/tmp/second"}) {
		t.Fatalf("upload dirs = %#v", result.Config.UploadDirs)
	}
	if result.Config.DaemonLogPath != filepath.Join(home, "daemon.log") || result.Config.ApprovalTimeoutSeconds != 12 {
		t.Fatalf("daemon/approval settings = %#v", result.Config)
	}
	for _, field := range []string{"idle_timeout", "operation_timeout", "read_timeout", "state_expire_days", "autosave", "autosave_interval", "autosave_key", "upload_dirs", "daemon_log", "approval_timeout"} {
		if result.Sources[field] != "env" {
			t.Fatalf("source[%q] = %q, want env", field, result.Sources[field])
		}
	}
}

func TestLoadWithOverridesReadsXDGGlobalConfiguration(t *testing.T) {
	home := t.TempDir()
	xdgConfig := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(xdgConfig, appName), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdgConfig, appName, "config.toml"), []byte("state_dir = \"global-state\"\nread_timeout = 77\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := LoadWithOverrides(FlagOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.StateDir != "global-state" || result.Config.ReadTimeoutSeconds != 77 {
		t.Fatalf("config = %#v", result.Config)
	}
	if result.Sources["state_dir"] != "global" || result.Sources["read_timeout"] != "global" {
		t.Fatalf("sources = %#v", result.Sources)
	}
}
