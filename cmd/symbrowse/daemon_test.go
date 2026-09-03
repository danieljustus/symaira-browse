package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/config"
	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// setTestHome redirects os.UserHomeDir to dir on every platform. Go reads
// $HOME on unix but %USERPROFILE% on windows, so a bare t.Setenv("HOME", ...)
// leaves the config loader looking at the real CI profile on windows-latest,
// which makes the global config.toml written below unreachable.
func setTestHome(t *testing.T, dir string) {
	t.Helper()
	// Keep the home-directory fixture authoritative when the host test runner
	// exports XDG paths pointing at a different profile.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

func TestResolveAllowedDomainsPrecedence(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	t.Run("flag wins over env and config", func(t *testing.T) {
		t.Setenv("SYMBROWSE_ALLOWED_DOMAINS", "env.example.com")
		got := resolveAllowedDomains("flag.example.com, *.flag.example.com")
		want := []string{"flag.example.com", "*.flag.example.com"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("env wins over config", func(t *testing.T) {
		global := filepath.Join(home, ".config", "symbrowse", "config.toml")
		if err := os.MkdirAll(filepath.Dir(global), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(global, []byte("allowed_domains = [\"config.example.com\"]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SYMBROWSE_ALLOWED_DOMAINS", "env.example.com")
		got := resolveAllowedDomains("")
		want := []string{"env.example.com"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("config is the fallback", func(t *testing.T) {
		_ = os.Unsetenv("SYMBROWSE_ALLOWED_DOMAINS")
		got := resolveAllowedDomains("")
		want := []string{"config.example.com"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("nothing configured yields nil", func(t *testing.T) {
		_ = os.Unsetenv("SYMBROWSE_ALLOWED_DOMAINS")
		if err := os.Remove(filepath.Join(home, ".config", "symbrowse", "config.toml")); err != nil {
			t.Fatal(err)
		}
		if got := resolveAllowedDomains(""); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
}

func TestSplitDomainsSkipsEmptyParts(t *testing.T) {
	got := splitDomains("a.example.com, , *.b.example.com,")
	want := []string{"a.example.com", "*.b.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveBoolPolicyPrecedence(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	t.Run("flag wins over env and config", func(t *testing.T) {
		t.Setenv("SYMBROWSE_SSRF", "false")
		if !resolveBoolPolicy("SYMBROWSE_SSRF", true, func(*config.Config) bool { return false }) {
			t.Fatal("flag true must win over env false")
		}
	})

	t.Run("env wins over config", func(t *testing.T) {
		global := filepath.Join(home, ".config", "symbrowse", "config.toml")
		if err := os.MkdirAll(filepath.Dir(global), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(global, []byte("ssrf_enabled = true\nallow_private = true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SYMBROWSE_SSRF", "false")
		if resolveBoolPolicy("SYMBROWSE_SSRF", false, func(cfg *config.Config) bool { return cfg.SSRFEnabled }) {
			t.Fatal("env false must win over config true")
		}
	})

	t.Run("config is the fallback", func(t *testing.T) {
		_ = os.Unsetenv("SYMBROWSE_SSRF")
		_ = os.Unsetenv("SYMBROWSE_ALLOW_PRIVATE")
		if !resolveBoolPolicy("SYMBROWSE_SSRF", false, func(cfg *config.Config) bool { return cfg.SSRFEnabled }) {
			t.Fatal("config true must be used without flag or env")
		}
		if !resolveBoolPolicy("SYMBROWSE_ALLOW_PRIVATE", false, func(cfg *config.Config) bool { return cfg.AllowPrivate }) {
			t.Fatal("config true must be used for allow_private")
		}
	})

	t.Run("nothing configured yields false", func(t *testing.T) {
		_ = os.Unsetenv("SYMBROWSE_SSRF")
		if err := os.Remove(filepath.Join(home, ".config", "symbrowse", "config.toml")); err != nil {
			t.Fatal(err)
		}
		if resolveBoolPolicy("SYMBROWSE_SSRF", false, func(cfg *config.Config) bool { return cfg.SSRFEnabled }) {
			t.Fatal("expected false without any configuration")
		}
	})
}

// TestResolveDaemonPolicyReflectsFlags guards issue #370: daemon.status must
// report the policy the daemon actually enforces. Before the fix the server
// options carried a zero PolicyStatus, so `daemon status --json` answered
// "ssrf_enabled": false while the SSRF guard was demonstrably active.
func TestResolveDaemonPolicyReflectsFlags(t *testing.T) {
	setTestHome(t, t.TempDir())
	_ = os.Unsetenv("SYMBROWSE_SSRF")
	_ = os.Unsetenv("SYMBROWSE_ALLOW_PRIVATE")
	_ = os.Unsetenv("SYMBROWSE_ALLOWED_DOMAINS")

	command := newDaemonCommand()
	if err := command.Flags().Set("ssrf", "true"); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("allowed-domains", "example.com"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	policy := resolveDaemonPolicy(command, cfg)
	if !policy.SSRFEnabled {
		t.Fatal("ssrf_enabled must be true when the daemon runs with --ssrf")
	}
	if policy.AllowPrivate {
		t.Fatal("allow_private must stay false without --allow-private")
	}
	if !reflect.DeepEqual(policy.AllowedDomains, []string{"example.com"}) {
		t.Fatalf("allowed domains = %v, want [example.com]", policy.AllowedDomains)
	}
}

// TestDaemonStatusReportsConfiguredPolicy verifies the status payload carries
// the server's policy end to end (issue #370).
func TestDaemonStatusReportsConfiguredPolicy(t *testing.T) {
	want := daemon.PolicyStatus{AllowedDomains: []string{"example.com"}, SSRFEnabled: true}
	server := daemon.NewServer(daemon.Options{SocketPath: filepath.Join(t.TempDir(), "s.sock"), Policy: want})
	raw, err := json.Marshal(map[string]any{"policy": server.Policy()})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Policy daemon.PolicyStatus `json:"policy"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Policy.SSRFEnabled || !reflect.DeepEqual(got.Policy.AllowedDomains, want.AllowedDomains) {
		t.Fatalf("status policy = %#v, want %#v", got.Policy, want)
	}
}
