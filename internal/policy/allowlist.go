// Package policy implements the protocol-neutral security policies of
// symbrowse: the domain allowlist and the SSRF guard enforced at the network
// layer.
package policy

import (
	"fmt"
	"net/url"
	"strings"
)

// allowedSchemes are the URL schemes a request may use when an allowlist is
// active. Everything else (file, data, javascript, chrome, blob, ...) is
// denied by default because it is not a web origin the operator listed.
var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
	"ws":    true,
	"wss":   true,
}

// Allowlist is a deny-by-default domain policy. When active, a request is
// allowed only if its scheme is http(s)/ws(s) and its host matches one of the
// configured patterns. An inactive allowlist (no patterns) permits every
// request and is the default when no --allowed-domains value is supplied.
type Allowlist struct {
	active   bool
	exact    map[string]struct{}
	suffixes []string // ".example.com" entries for "*.example.com" patterns
	patterns []string // original, normalized patterns (for reporting)
}

// BlockedDomainError is returned when a request target is outside the
// configured domain allowlist.
type BlockedDomainError struct {
	URL string
}

func (e *BlockedDomainError) Error() string {
	return fmt.Sprintf("blocked_domain: %s is outside the configured domain allowlist", e.URL)
}

// ParseAllowlist builds an Allowlist from host patterns. Patterns are
// hostnames, optionally prefixed with "*." to include the apex and all
// subdomains (for example "*.example.com" matches "example.com",
// "www.example.com", and "a.b.example.com"). Matching is case-insensitive and
// ignores a single trailing dot. Entries must be bare hostnames: schemes,
// ports, paths, userinfo, and bare "*" are rejected so that a mistyped
// pattern fails loudly instead of silently widening the policy.
func ParseAllowlist(patterns []string) (*Allowlist, error) {
	allowlist := &Allowlist{
		exact:    make(map[string]struct{}),
		patterns: make([]string, 0, len(patterns)),
	}
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		normalized, wildcard, err := normalizePattern(pattern)
		if err != nil {
			return nil, err
		}
		allowlist.patterns = append(allowlist.patterns, pattern)
		if wildcard {
			allowlist.suffixes = append(allowlist.suffixes, normalized)
		} else {
			allowlist.exact[normalized] = struct{}{}
		}
	}
	allowlist.active = len(allowlist.patterns) > 0
	return allowlist, nil
}

// normalizePattern validates one pattern and returns its normalized form.
// Exact patterns come back as "example.com"; wildcard patterns come back as
// ".example.com" with wildcard=true.
func normalizePattern(pattern string) (normalized string, wildcard bool, err error) {
	lower := strings.ToLower(pattern)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "ws://") || strings.HasPrefix(lower, "wss://") {
		return "", false, fmt.Errorf("invalid allowlist pattern %q: scheme prefixes are not allowed; pass a bare hostname", pattern)
	}
	if strings.HasPrefix(lower, "*.") {
		wildcard = true
		lower = strings.TrimPrefix(lower, "*.")
	} else if strings.Contains(lower, "*") {
		return "", false, fmt.Errorf("invalid allowlist pattern %q: only a leading \"*.\" wildcard is supported", pattern)
	}
	host := strings.TrimSuffix(lower, ".")
	if host == "" {
		return "", false, fmt.Errorf("invalid allowlist pattern %q: empty host", pattern)
	}
	if strings.ContainsAny(host, "/:@?#") || strings.Contains(host, "..") {
		return "", false, fmt.Errorf("invalid allowlist pattern %q: patterns must be bare hostnames without scheme, port, path, or userinfo", pattern)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return "", false, fmt.Errorf("invalid allowlist pattern %q: empty host label", pattern)
		}
	}
	if wildcard {
		return "." + host, true, nil
	}
	return host, false, nil
}

// Active reports whether a policy is configured. An inactive allowlist allows
// every request.
func (a *Allowlist) Active() bool {
	return a != nil && a.active
}

// Patterns returns the configured patterns in the order they were supplied.
func (a *Allowlist) Patterns() []string {
	if a == nil {
		return nil
	}
	return a.patterns
}

// AllowsURL reports whether a request to u may proceed. The policy applies to
// the URL's scheme and hostname; ports, paths, and query strings are ignored.
func (a *Allowlist) AllowsURL(u *url.URL) bool {
	if !a.Active() {
		return true
	}
	if u == nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if !allowedSchemes[scheme] {
		return false
	}
	return a.AllowsHost(u.Hostname())
}

// AllowsHost reports whether hostname is covered by the configured patterns.
func (a *Allowlist) AllowsHost(hostname string) bool {
	if !a.Active() {
		return true
	}
	host := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if host == "" {
		return false
	}
	if _, ok := a.exact[host]; ok {
		return true
	}
	for _, suffix := range a.suffixes {
		// A "*.example.com" pattern covers the apex ("example.com") as well as
		// every subdomain, mirroring Safari-style content-blocker semantics.
		if host == suffix[1:] || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
