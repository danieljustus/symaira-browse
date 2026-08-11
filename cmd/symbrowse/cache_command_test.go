package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestParseRange(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		wantA   int
		wantB   int
		wantErr string
	}{
		{name: "empty spec", spec: "", wantA: 0, wantB: 0},
		{name: "whitespace only", spec: "   ", wantA: 0, wantB: 0},
		{name: "full range", spec: "40-120", wantA: 40, wantB: 120},
		{name: "open start", spec: "-120", wantA: 0, wantB: 120},
		{name: "open end", spec: "40-", wantA: 40, wantB: 0},
		{name: "single line", spec: "1-1", wantA: 1, wantB: 1},
		{name: "surrounding whitespace", spec: " 5-10 ", wantA: 5, wantB: 10},
		{name: "start below one", spec: "0-5", wantErr: "start must be a positive line number"},
		{name: "dash inside end", spec: "-5-10", wantErr: "end must be >= start"},
		{name: "non-numeric start", spec: "abc-5", wantErr: "start must be a positive line number"},
		{name: "end below start", spec: "40-10", wantErr: "end must be >= start"},
		{name: "non-numeric end", spec: "40-x", wantErr: "end must be >= start"},
		{name: "negative end", spec: "40--5", wantErr: "end must be >= start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := parseRange(tc.spec)
			if tc.wantErr != "" {
				want := fmt.Sprintf("invalid range %q: %s", tc.spec, tc.wantErr)
				if err == nil || err.Error() != want {
					t.Fatalf("err = %v, want %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRange(%q) returned an error: %v", tc.spec, err)
			}
			if start != tc.wantA || end != tc.wantB {
				t.Fatalf("parseRange(%q) = %d,%d, want %d,%d", tc.spec, start, end, tc.wantA, tc.wantB)
			}
		})
	}
}

func TestCacheFromConfigUsesEnvOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cacheDir := filepath.Join(t.TempDir(), "cache")
	t.Setenv("SYMBROWSE_CACHE_DIR", cacheDir)
	t.Setenv("SYMBROWSE_CACHE_TTL_HOURS", "7")

	cache, err := cacheFromConfig()
	if err != nil {
		t.Fatalf("cacheFromConfig returned an error: %v", err)
	}
	if cache.Root != filepath.Join(cacheDir, "out") {
		t.Fatalf("cache root = %q, want %q", cache.Root, filepath.Join(cacheDir, "out"))
	}
	if cache.TTL != 7*time.Hour {
		t.Fatalf("cache ttl = %v, want 7h", cache.TTL)
	}
}

func TestCacheFromConfigDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SYMBROWSE_CACHE_DIR", "")
	t.Setenv("SYMBROWSE_CACHE_TTL_HOURS", "")

	cache, err := cacheFromConfig()
	if err != nil {
		t.Fatalf("cacheFromConfig returned an error: %v", err)
	}
	wantRoot := filepath.Join(home, ".cache", "symbrowse", "out")
	if cache.Root != wantRoot {
		t.Fatalf("cache root = %q, want %q", cache.Root, wantRoot)
	}
	if cache.TTL != 24*time.Hour {
		t.Fatalf("cache ttl = %v, want 24h", cache.TTL)
	}
}
