package robots

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseBasic(t *testing.T) {
	content := `
User-agent: *
Disallow: /admin/
Disallow: /private/
Allow: /admin/public/
`
	groups := parse(content)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	g := groups[0]
	if len(g.agents) != 1 || g.agents[0] != "*" {
		t.Fatalf("expected agent *, got %v", g.agents)
	}
	if len(g.rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(g.rules))
	}
	if g.rules[0].path != "/admin/" || g.rules[0].allowed {
		t.Errorf("rule 0: expected Disallow /admin/, got %+v", g.rules[0])
	}
	if g.rules[1].path != "/private/" || g.rules[1].allowed {
		t.Errorf("rule 1: expected Disallow /private/, got %+v", g.rules[1])
	}
	if g.rules[2].path != "/admin/public/" || !g.rules[2].allowed {
		t.Errorf("rule 2: expected Allow /admin/public/, got %+v", g.rules[2])
	}
}

func TestParseMultipleAgents(t *testing.T) {
	content := `
User-agent: Googlebot
Disallow: /secret/

User-agent: *
Disallow: /admin/
`
	groups := parse(content)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	if groups[0].agents[0] != "googlebot" {
		t.Errorf("group 0 agent: expected googlebot, got %s", groups[0].agents[0])
	}
	if groups[1].agents[0] != "*" {
		t.Errorf("group 1 agent: expected *, got %s", groups[1].agents[0])
	}
}

func TestParseComments(t *testing.T) {
	content := `
# This is a comment
User-agent: * # inline comment
Disallow: /admin/ # another comment
`
	groups := parse(content)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0].rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(groups[0].rules))
	}
	if groups[0].rules[0].path != "/admin/" {
		t.Errorf("expected /admin/, got %s", groups[0].rules[0].path)
	}
}

func TestParseMalformed(t *testing.T) {
	content := `
this is not valid
User-agent
Disallow
: no key
User-agent: *
: no key again
Disallow: /ok/
random garbage line
`
	groups := parse(content)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0].rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(groups[0].rules))
	}
	if groups[0].rules[0].path != "/ok/" {
		t.Errorf("expected /ok/, got %s", groups[0].rules[0].path)
	}
}

func TestParseEmptyDisallow(t *testing.T) {
	content := `
User-agent: *
Disallow:
Disallow: /admin/
`
	groups := parse(content)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0].rules) != 1 {
		t.Fatalf("expected 1 rule (empty Disallow ignored), got %d", len(groups[0].rules))
	}
}

func TestIsAllowedWildcard(t *testing.T) {
	groups := parse(`
User-agent: *
Disallow: /admin/
Disallow: /private/
Allow: /admin/public/
`)
	tests := []struct {
		path    string
		allowed bool
	}{
		{"/", true},
		{"/index.html", true},
		{"/admin/", false},
		{"/admin/settings", false},
		{"/admin/public/", true},
		{"/admin/public/page", true},
		{"/private/", false},
		{"/private/data", false},
	}
	for _, tt := range tests {
		got := isAllowed(groups, "TestBot", tt.path)
		if got != tt.allowed {
			t.Errorf("isAllowed(*, %q) = %v, want %v", tt.path, got, tt.allowed)
		}
	}
}

func TestIsAllowedSpecificAgent(t *testing.T) {
	groups := parse(`
User-agent: Googlebot
Disallow: /secret/

User-agent: *
Disallow: /admin/
`)
	if isAllowed(groups, "Googlebot", "/secret/") {
		t.Error("Googlebot should be disallowed from /secret/")
	}
	if !isAllowed(groups, "Googlebot", "/admin/") {
		t.Error("Googlebot should be allowed on /admin/ (no rule for it)")
	}
	if isAllowed(groups, "OtherBot", "/admin/") {
		t.Error("OtherBot should be disallowed from /admin/")
	}
	if !isAllowed(groups, "OtherBot", "/secret/") {
		t.Error("OtherBot should be allowed on /secret/")
	}
}

func TestIsAllowedLongestMatch(t *testing.T) {
	groups := parse(`
User-agent: *
Disallow: /a/
Disallow: /a/b/
Allow: /a/b/c/
`)
	tests := []struct {
		path    string
		allowed bool
	}{
		{"/a/", false},
		{"/a/x", false},
		{"/a/b/", false},
		{"/a/b/x", false},
		{"/a/b/c/", true},
		{"/a/b/c/d", true},
	}
	for _, tt := range tests {
		got := isAllowed(groups, "Bot", tt.path)
		if got != tt.allowed {
			t.Errorf("isAllowed(Bot, %q) = %v, want %v", tt.path, got, tt.allowed)
		}
	}
}

func TestIsAllowedWildcardPath(t *testing.T) {
	groups := parse(`
User-agent: *
Disallow: /search*
`)
	tests := []struct {
		path    string
		allowed bool
	}{
		{"/search", false},
		{"/search?q=test", false},
		{"/search/results", false},
		{"/other", true},
	}
	for _, tt := range tests {
		got := isAllowed(groups, "Bot", tt.path)
		if got != tt.allowed {
			t.Errorf("isAllowed(Bot, %q) = %v, want %v", tt.path, got, tt.allowed)
		}
	}
}

func TestIsAllowedNoGroups(t *testing.T) {
	if !isAllowed(nil, "Bot", "/anything") {
		t.Error("no groups should allow everything")
	}
	if !isAllowed([]group{}, "Bot", "/anything") {
		t.Error("empty groups should allow everything")
	}
}

func TestMatchPath(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		match   bool
	}{
		{"", "/", true},
		{"", "/anything", true},
		{"/admin/", "/admin/", true},
		{"/admin/", "/admin/settings", true},
		{"/admin/", "/other", false},
		{"/search*", "/search", true},
		{"/search*", "/search?q=test", true},
		{"/search*", "/other", false},
	}
	for _, tt := range tests {
		got := matchPath(tt.pattern, tt.path)
		if got != tt.match {
			t.Errorf("matchPath(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.match)
		}
	}
}

func TestCheckerIntegration(t *testing.T) {
	robotsTxt := `
User-agent: *
Disallow: /admin/
Allow: /admin/public/
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, robotsTxt)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewChecker()
	c.private = true
	ctx := context.Background()

	allowed, err := c.Check(ctx, "TestBot", srv.URL+"/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("/index.html should be allowed")
	}

	allowed, err = c.Check(ctx, "TestBot", srv.URL+"/admin/settings")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("/admin/settings should be disallowed")
	}

	allowed, err = c.Check(ctx, "TestBot", srv.URL+"/admin/public/page")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("/admin/public/page should be allowed")
	}
}

func TestCheckerCacheTTL(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		fmt.Fprint(w, "User-agent: *\nDisallow: /admin/\n")
	}))
	defer srv.Close()

	c := NewChecker()
	c.private = true
	c.ttl = 50 * time.Millisecond
	ctx := context.Background()

	c.Check(ctx, "Bot", srv.URL+"/page1")
	c.Check(ctx, "Bot", srv.URL+"/page2")
	if callCount != 1 {
		t.Errorf("expected 1 fetch (cached), got %d", callCount)
	}

	time.Sleep(60 * time.Millisecond)

	c.Check(ctx, "Bot", srv.URL+"/page3")
	if callCount != 2 {
		t.Errorf("expected 2 fetches (cache expired), got %d", callCount)
	}
}

func TestChecker404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewChecker()
	c.private = true
	ctx := context.Background()

	allowed, err := c.Check(ctx, "Bot", srv.URL+"/anything")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("404 robots.txt should allow everything")
	}
}

func TestCheckerFailOpen(t *testing.T) {
	c := NewChecker()
	ctx := context.Background()

	allowed, err := c.Check(ctx, "Bot", "http://127.0.0.1:1/anything")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("private address should be blocked by SSRF guard")
	}
}

func TestCheckerNonHTTPScheme(t *testing.T) {
	c := NewChecker()
	ctx := context.Background()

	allowed, err := c.Check(ctx, "Bot", "ftp://example.com/file")
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Error("non-HTTP scheme should be allowed")
	}
}

func TestCheckerEmptyPath(t *testing.T) {
	robotsTxt := `User-agent: *
Disallow: /`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, robotsTxt)
	}))
	defer srv.Close()

	c := NewChecker()
	c.private = true
	ctx := context.Background()

	allowed, err := c.Check(ctx, "Bot", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("root path with Disallow: / should be disallowed")
	}
}

func TestIsAllowedMostSpecificAgentWins(t *testing.T) {
	groups := parse(`
User-agent: *
Disallow: /private/

User-agent: Googlebot
Allow: /private/docs/
`)
	if !isAllowed(groups, "Googlebot", "/private/docs/") {
		t.Error("Googlebot should be allowed on /private/docs/ via most-specific match")
	}
	if !isAllowed(groups, "Googlebot", "/private/other") {
		t.Error("Googlebot should be allowed on /private/other (no rule in most-specific group)")
	}
	if isAllowed(groups, "OtherBot", "/private/docs/") {
		t.Error("OtherBot should be disallowed on /private/docs/ via * rule")
	}
}

// ---------------------------------------------------------------------------
// WithPrivate setter
// ---------------------------------------------------------------------------

func TestCheckerWithPrivate(t *testing.T) {
	c := NewChecker()
	if c.private {
		t.Error("expected private=false by default")
	}

	ret := c.WithPrivate(true)
	if !c.private {
		t.Error("expected private=true after WithPrivate(true)")
	}
	if ret != c {
		t.Error("WithPrivate should return the same checker for chaining")
	}

	c.WithPrivate(false)
	if c.private {
		t.Error("expected private=false after WithPrivate(false)")
	}
}

// ---------------------------------------------------------------------------
// Check: SSRF guard application (private=false blocks robots.txt fetch)
// ---------------------------------------------------------------------------

func TestCheckerSSRFGuardApplication(t *testing.T) {
	c := NewChecker()
	// private defaults to false; SSRF guard should block fetching from 127.0.0.1
	allowed, err := c.Check(context.Background(), "Bot", "http://127.0.0.1:9999/page")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("expected disallowed for private URL when SSRF guard is active")
	}
}

func TestCheckerSSRFGuardDisabledWithWithPrivate(t *testing.T) {
	robotsTxt := "User-agent: *\nDisallow: /secret/\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, robotsTxt)
	}))
	defer srv.Close()

	c := NewChecker().WithPrivate(true)

	allowed, err := c.Check(context.Background(), "Bot", srv.URL+"/secret/page")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Error("expected disallowed for /secret/page")
	}
}

// ---------------------------------------------------------------------------
// Check: groupsForDomain error paths
// ---------------------------------------------------------------------------

func TestCheckerUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewChecker().WithPrivate(true)
	_, err := c.groupsForDomain(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error for non-200/non-404 status")
	}
}

func TestCheckerInvalidURL(t *testing.T) {
	c := NewChecker()
	allowed, err := c.Check(context.Background(), "Bot", "://bad-url")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
	if !allowed {
		t.Error("expected fail-open (allowed=true) for invalid URL")
	}
}

func TestParseSitemapEntries(t *testing.T) {
	content := `
User-agent: *
Disallow: /admin/
Sitemap: https://example.com/sitemap1.xml
Sitemap: https://example.com/sitemap2.xml
`
	groups := parse(content)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0].sitemaps) != 2 {
		t.Fatalf("expected 2 sitemaps, got %d", len(groups[0].sitemaps))
	}
	if groups[0].sitemaps[0] != "https://example.com/sitemap1.xml" {
		t.Errorf("sitemap 0 = %q", groups[0].sitemaps[0])
	}
	if groups[0].sitemaps[1] != "https://example.com/sitemap2.xml" {
		t.Errorf("sitemap 1 = %q", groups[0].sitemaps[1])
	}
}

func TestParseSitemapBeforeUserAgent(t *testing.T) {
	content := `
Sitemap: https://example.com/sitemap.xml
User-agent: *
Disallow: /admin/
`
	groups := parse(content)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0].sitemaps) != 1 {
		t.Fatalf("expected 1 sitemap, got %d", len(groups[0].sitemaps))
	}
	if groups[0].sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Errorf("sitemap = %q", groups[0].sitemaps[0])
	}
}

func TestCheckerSitemapsBasic(t *testing.T) {
	robotsTxt := `
User-agent: *
Disallow: /admin/
Sitemap: https://example.com/sitemap1.xml
Sitemap: https://example.com/sitemap2.xml
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, robotsTxt)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewChecker().WithPrivate(true)
	ctx := context.Background()

	sitemaps, err := c.Sitemaps(ctx, "symfetch", srv.URL+"/page")
	if err != nil {
		t.Fatal(err)
	}
	if len(sitemaps) != 2 {
		t.Fatalf("expected 2 sitemaps, got %d", len(sitemaps))
	}
	if sitemaps[0] != "https://example.com/sitemap1.xml" {
		t.Errorf("sitemap 0 = %q", sitemaps[0])
	}
}

func TestCheckerSitemapsNoSitemaps(t *testing.T) {
	robotsTxt := `
User-agent: *
Disallow: /admin/
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, robotsTxt)
	}))
	defer srv.Close()

	c := NewChecker().WithPrivate(true)
	ctx := context.Background()

	sitemaps, err := c.Sitemaps(ctx, "symfetch", srv.URL+"/page")
	if err != nil {
		t.Fatal(err)
	}
	if len(sitemaps) != 0 {
		t.Errorf("expected 0 sitemaps, got %d", len(sitemaps))
	}
}

func TestCheckerSitemapsPrivateBlocked(t *testing.T) {
	c := NewChecker()
	ctx := context.Background()

	_, err := c.Sitemaps(ctx, "Bot", "http://127.0.0.1:9999/page")
	if err == nil {
		t.Error("expected error for private URL")
	}
}

func TestCheckerSitemapsNonHTTP(t *testing.T) {
	c := NewChecker()
	ctx := context.Background()

	sitemaps, err := c.Sitemaps(ctx, "Bot", "ftp://example.com/file")
	if err != nil {
		t.Fatal(err)
	}
	if len(sitemaps) != 0 {
		t.Errorf("expected 0 sitemaps for non-HTTP, got %d", len(sitemaps))
	}
}

func TestCheckerSitemaps404RobotsTxt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewChecker().WithPrivate(true)
	ctx := context.Background()

	sitemaps, err := c.Sitemaps(ctx, "Bot", srv.URL+"/page")
	if err != nil {
		t.Fatal(err)
	}
	if len(sitemaps) != 0 {
		t.Errorf("expected 0 sitemaps when robots.txt 404, got %d", len(sitemaps))
	}
}
