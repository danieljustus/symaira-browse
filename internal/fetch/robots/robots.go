// Package robots implements robots.txt parsing and URL allowance checking
// per the Robots Exclusion Protocol (RFC 9309).
package robots

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
)

// DefaultTTL is the cache lifetime for a domain's robots.txt rules.
const DefaultTTL = 1 * time.Hour

type rule struct {
	path    string
	allowed bool
}

type group struct {
	agents   []string
	rules    []rule
	sitemaps []string
}

type cacheEntry struct {
	groups    []group
	expiresAt time.Time
}

// Checker fetches and caches robots.txt rules per domain, then checks
// whether a given URL is allowed for a specific user-agent.
type Checker struct {
	mu      sync.RWMutex
	cache   map[string]*cacheEntry
	ttl     time.Duration
	client  *http.Client
	private bool
}

func NewChecker() *Checker {
	// robots.txt is fetched with plain net/http rather than the browser-impersonating
	// TLS client because robots.txt is a well-known, non-sensitive resource that
	// sites expect to be fetched by any HTTP client. The SSRF guard is applied here
	// to prevent robots.txt fetches from being used to probe private networks via
	// DNS rebinding (public resolution on first lookup, private on second).
	return &Checker{
		cache: make(map[string]*cacheEntry),
		ttl:   DefaultTTL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// WithPrivate allows the checker to fetch robots.txt from RFC1918/loopback
// addresses. It is intended for tests and for deployments that already opt
// into private fetches at a higher layer.
func (c *Checker) WithPrivate(private bool) *Checker {
	c.mu.Lock()
	c.private = private
	c.mu.Unlock()
	return c
}

// Check returns true if the given URL is allowed for userAgent according
// to the site's robots.txt. If robots.txt cannot be fetched or parsed,
// the URL is allowed (fail-open for polite crawling).
func (c *Checker) Check(ctx context.Context, userAgent, rawURL string) (bool, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return true, fmt.Errorf("robots: parse url: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return true, nil
	}

	domain := u.Scheme + "://" + u.Host
	groups, err := c.groupsForDomain(ctx, domain)
	if err != nil {
		var blocked *fetch.ErrBlockedPrivate
		if errors.As(err, &blocked) {
			return false, nil
		}
		return true, nil
	}

	path := u.Path
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	return isAllowed(groups, userAgent, path), nil
}

func (c *Checker) groupsForDomain(ctx context.Context, domain string) ([]group, error) {
	c.mu.RLock()
	entry, ok := c.cache[domain]
	c.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.groups, nil
	}

	robotsURL := domain + "/robots.txt"

	if !c.private {
		if err := fetch.CheckSSRF(robotsURL); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil, err
	}

	// codeql[go/request-forgery] False positive: dynamic domain fetch is by design for a web fetcher, and SSRF is prevented by CheckSSRF.
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		c.mu.Lock()
		c.cache[domain] = &cacheEntry{
			groups:    nil,
			expiresAt: time.Now().Add(c.ttl),
		}
		c.mu.Unlock()
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("robots: unexpected status %d for %s", resp.StatusCode, robotsURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	groups := parse(string(body))

	c.mu.Lock()
	c.cache[domain] = &cacheEntry{
		groups:    groups,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()

	return groups, nil
}

func parse(content string) []group {
	var groups []group
	var current *group

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		colonIdx := strings.IndexByte(line, ':')
		if colonIdx < 0 {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(line[:colonIdx]))
		value := strings.TrimSpace(line[colonIdx+1:])

		switch key {
		case "user-agent":
			if current == nil || len(current.rules) > 0 {
				g := group{}
				groups = append(groups, g)
				current = &groups[len(groups)-1]
			}
			current.agents = append(current.agents, strings.ToLower(value))

		case "disallow":
			if current != nil && value != "" {
				current.rules = append(current.rules, rule{path: value, allowed: false})
			}

		case "allow":
			if current != nil && value != "" {
				current.rules = append(current.rules, rule{path: value, allowed: true})
			}

		case "sitemap":
			if value != "" {
				if current == nil {
					g := group{agents: []string{"*"}}
					groups = append(groups, g)
					current = &groups[len(groups)-1]
				}
				current.sitemaps = append(current.sitemaps, value)
			}
		}
	}

	return groups
}

func isAllowed(groups []group, userAgent, path string) bool {
	ua := strings.ToLower(userAgent)

	var bestGroup *group
	bestLen := -1

	for i := range groups {
		g := &groups[i]
		for _, agent := range g.agents {
			matched := false
			specificity := 0
			if agent == "*" {
				matched = true
				specificity = 0
			} else if strings.Contains(ua, agent) {
				matched = true
				specificity = len(agent)
			}
			if matched && specificity > bestLen {
				bestGroup = g
				bestLen = specificity
			}
		}
	}

	if bestGroup == nil {
		return true
	}

	bestRuleLen := -1
	allowed := true

	for _, r := range bestGroup.rules {
		if matchPath(r.path, path) {
			ruleLen := len(r.path)
			if ruleLen > bestRuleLen {
				bestRuleLen = ruleLen
				allowed = r.allowed
			} else if ruleLen == bestRuleLen {
				if r.allowed {
					allowed = true
				}
			}
		}
	}

	return allowed
}

func (c *Checker) Sitemaps(ctx context.Context, userAgent, rawURL string) ([]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("robots: parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil
	}
	domain := u.Scheme + "://" + u.Host
	groups, err := c.groupsForDomain(ctx, domain)
	if err != nil {
		var blocked *fetch.ErrBlockedPrivate
		if errors.As(err, &blocked) {
			return nil, err
		}
		return nil, nil
	}
	if len(groups) == 0 {
		return nil, nil
	}
	var sitemaps []string
	for _, g := range groups {
		for _, agent := range g.agents {
			matched := agent == "*" || strings.Contains(strings.ToLower(userAgent), agent)
			if !matched {
				continue
			}
			sitemaps = append(sitemaps, g.sitemaps...)
		}
	}
	return sitemaps, nil
}

func matchPath(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(path, pattern[:len(pattern)-1])
	}
	return strings.HasPrefix(path, pattern)
}
