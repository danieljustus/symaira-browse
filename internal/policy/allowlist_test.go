package policy

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseAllowlistAcceptsValidPatterns(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		want     []string // normalized exact hosts
		wantSfx  []string // normalized suffix entries
		active   bool
	}{
		{name: "empty is inactive", patterns: nil, active: false},
		{name: "blank entries ignored", patterns: []string{"", "  "}, active: false},
		{
			name:     "exact and wildcard",
			patterns: []string{"example.com", "*.example.org"},
			want:     []string{"example.com"},
			wantSfx:  []string{".example.org"},
			active:   true,
		},
		{
			name:     "case and trailing dot normalized",
			patterns: []string{"EXAMPLE.COM.", "*.Example.ORG."},
			want:     []string{"example.com"},
			wantSfx:  []string{".example.org"},
			active:   true,
		},
		{
			name:     "ip literal accepted",
			patterns: []string{"127.0.0.1", "10.0.0.1"},
			want:     []string{"127.0.0.1", "10.0.0.1"},
			active:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := ParseAllowlist(tt.patterns)
			if err != nil {
				t.Fatalf("ParseAllowlist(%q) error: %v", tt.patterns, err)
			}
			if a.Active() != tt.active {
				t.Fatalf("Active() = %v, want %v", a.Active(), tt.active)
			}
			for _, host := range tt.want {
				if _, ok := a.exact[host]; !ok {
					t.Errorf("exact set missing %q: %v", host, a.exact)
				}
			}
			for _, suffix := range tt.wantSfx {
				found := false
				for _, s := range a.suffixes {
					if s == suffix {
						found = true
					}
				}
				if !found {
					t.Errorf("suffix set missing %q: %v", suffix, a.suffixes)
				}
			}
		})
	}
}

func TestParseAllowlistRejectsInvalidPatterns(t *testing.T) {
	invalid := []string{
		"*",
		"*.",           // wildcard with empty host
		"http://x.com", // scheme prefix
		"https://x.com",
		"ws://x.com",
		"example.com:8080", // port
		"example.com/path", // path
		"user@example.com", // userinfo
		"foo..example.com", // empty label
		".example.com",     // leading dot without wildcard
		"exa*mple.com",     // wildcard not at start
		"*example.com",
	}
	for _, pattern := range invalid {
		t.Run(pattern, func(t *testing.T) {
			if _, err := ParseAllowlist([]string{pattern}); err == nil {
				t.Fatalf("ParseAllowlist(%q) succeeded, want error", pattern)
			}
		})
	}
}

func TestAllowlistAllowsHost(t *testing.T) {
	a, err := ParseAllowlist([]string{"example.com", "*.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		"example.com",
		"EXAMPLE.COM",
		"example.com.",
		"www.example.org",
		"example.org",     // wildcard includes the apex
		"a.b.example.org", // wildcard spans multiple labels
		"deep.a.b.example.org",
	}
	for _, host := range allowed {
		if !a.AllowsHost(host) {
			t.Errorf("AllowsHost(%q) = false, want true", host)
		}
	}
	denied := []string{
		"notexample.com", // suffix attack: must not match example.com
		"evilexample.com",
		"example.com.evil.org",
		"www.example.com", // exact pattern does not cover subdomains
		"sub.example.org.evil.net",
		"",    // empty host
		"org", // bare TLD is neither the apex nor a subdomain
	}
	for _, host := range denied {
		if a.AllowsHost(host) {
			t.Errorf("AllowsHost(%q) = true, want false", host)
		}
	}
}

func TestAllowlistAllowsURL(t *testing.T) {
	a, err := ParseAllowlist([]string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		"https://example.com/",
		"http://example.com:8080/path?q=1", // port and path ignored
		"wss://example.com/socket",
		"ws://example.com/",
		"https://user:pass@example.com/", // userinfo stripped by Hostname
	}
	for _, raw := range allowed {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !a.AllowsURL(u) {
			t.Errorf("AllowsURL(%q) = false, want true", raw)
		}
	}
	denied := []string{
		"https://www.example.com/",   // subdomain not covered by exact pattern
		"https://example.org/",       // foreign host
		"http://127.0.0.1:8080/",     // loopback not implicitly allowed
		"file:///etc/passwd",         // non-web scheme
		"data:text/html,<h1>hi</h1>", // non-web scheme
		"javascript:alert(1)",        // non-web scheme
		"about:blank",
		"chrome://settings",
		"blob:https://example.com/uuid",
	}
	for _, raw := range denied {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if a.AllowsURL(u) {
			t.Errorf("AllowsURL(%q) = true, want false", raw)
		}
	}
}

func TestAllowlistInactiveAllowsEverything(t *testing.T) {
	a, err := ParseAllowlist(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://anything.example/", "file:///etc/passwd", "data:text/plain,hi"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !a.AllowsURL(u) {
			t.Errorf("inactive allowlist denied %q", raw)
		}
	}
	var nilList *Allowlist
	if !nilList.AllowsURL(nil) {
		t.Error("nil allowlist must allow everything (inactive default)")
	}
}

func TestAllowlistIDNRequiresPunycode(t *testing.T) {
	a, err := ParseAllowlist([]string{"xn--bcher-kva.example"})
	if err != nil {
		t.Fatal(err)
	}
	// Chrome resolves internationalized domain names to punycode before the
	// network layer sees them, so a punycode pattern matches.
	if !a.AllowsHost("xn--bcher-kva.example") {
		t.Error("punycode pattern must match punycode host")
	}
	// A Unicode host must not match: the failure direction is deny, which is
	// safe. Operators must supply the punycode form.
	if a.AllowsHost("bücher.example") {
		t.Error("unicode host must not match punycode pattern")
	}
}

func TestAllowlistPatternsReportedInOrder(t *testing.T) {
	a, err := ParseAllowlist([]string{"b.example", "a.example"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(a.Patterns(), ",")
	if got != "b.example,a.example" {
		t.Fatalf("Patterns() = %q", got)
	}
	if a.Patterns() == nil {
		t.Fatal("Patterns() must not be nil for an active allowlist")
	}
}
