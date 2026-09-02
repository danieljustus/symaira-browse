package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/config"
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
