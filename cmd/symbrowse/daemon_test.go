package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveAllowedDomainsPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

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
