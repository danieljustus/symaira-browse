package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
	"github.com/danieljustus/symaira-browse/internal/fetch/cache"
	"github.com/danieljustus/symaira-browse/internal/fetch/dom"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/robots"
)

type fakeClient struct {
	resp *fetch.Response
	err  error
}

func (c *fakeClient) Fetch(_ context.Context, _ fetch.Request) (*fetch.Response, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.resp, nil
}

func (c *fakeClient) Close() error { return nil }

type fakeEngine struct {
	tree *dom.Tree
	err  error
}

func (e *fakeEngine) Materialize(_ context.Context, _ *fetch.Response) (*dom.Tree, error) {
	return e.tree, e.err
}

type fakeCache struct {
	*cache.Cache
	putErr error
}

func (fc *fakeCache) Put(_, _, _, _, _ string, _ []byte, _ cache.Meta) error {
	return fc.putErr
}

// cachePutter is the minimal interface pipeline needs from a cache.
type cachePutter interface {
	Put(url, profile, format, session, contentKey string, body []byte, meta cache.Meta) error
}

var _ cachePutter = (*cache.Cache)(nil)
var _ cachePutter = (*fakeCache)(nil)

func TestRawHTMLFallback(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "empty",
			body: []byte{},
			want: "",
		},
		{
			name: "ascii",
			body: []byte("Hello World"),
			want: "Hello World",
		},
		{
			name: "html",
			body: []byte("<html><body>Content</body></html>"),
			want: "<html><body>Content</body></html>",
		},
		{
			name: "binary-like",
			body: []byte{0x00, 0x01, 0x02},
			want: "\x00\x01\x02",
		},
		{
			name: "unicode",
			body: []byte("Hello \u00e9\u00e8\u00ea"),
			want: "Hello \u00e9\u00e8\u00ea",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rawHTMLFallback(tt.body)
			if got != tt.want {
				t.Errorf("rawHTMLFallback() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIslandSummary(t *testing.T) {
	tests := []struct {
		name     string
		islands  []agentdom.DataIsland
		contains []string
		empty    bool
	}{
		{
			name:    "empty islands",
			islands: []agentdom.DataIsland{},
			empty:   true,
		},
		{
			name: "single island with object",
			islands: []agentdom.DataIsland{
				{
					Source: "__NEXT_DATA__",
					JSON:   []byte(`{"page":"home","props":{"id":1}}`),
				},
			},
			contains: []string{"__NEXT_DATA__", "keys="},
		},
		{
			name: "multiple islands",
			islands: []agentdom.DataIsland{
				{
					Source: "__NEXT_DATA__",
					JSON:   []byte(`{"page":"home"}`),
				},
				{
					Source: "application/ld+json",
					JSON:   []byte(`{"@type":"Article","headline":"Test"}`),
				},
			},
			contains: []string{"__NEXT_DATA__", "application/ld+json"},
		},
		{
			name: "island with array JSON",
			islands: []agentdom.DataIsland{
				{
					Source: "data-list",
					JSON:   []byte(`[1,2,3]`),
				},
			},
			contains: []string{"data-list", "raw JSON"},
		},
		{
			name: "island with malformed JSON",
			islands: []agentdom.DataIsland{
				{
					Source: "bad-json",
					JSON:   []byte(`not valid json`),
				},
			},
			contains: []string{"bad-json", "raw JSON"},
		},
		{
			name: "island with primitive JSON",
			islands: []agentdom.DataIsland{
				{
					Source: "string-value",
					JSON:   []byte(`"just a string"`),
				},
			},
			contains: []string{"string-value", "raw JSON"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IslandSummary(tt.islands)
			if tt.empty {
				if got != "" {
					t.Errorf("IslandSummary() = %q, want empty", got)
				}
				return
			}
			for _, s := range tt.contains {
				if !containsString(got, s) {
					t.Errorf("IslandSummary() = %q, should contain %q", got, s)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func newInternalTestClient(t *testing.T) fetch.Client {
	t.Helper()
	c, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRun_CachedPrivateRedirectBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	cacher := cache.New(tmpDir, 1*time.Hour, 0)
	ck := (&Options{Content: ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000}}).CacheKey()
	cacher.Put("https://example.com/page", "chrome", "markdown", "", ck, []byte("cached"), cache.Meta{
		URL:      "https://example.com/page",
		FinalURL: "http://127.0.0.1:9999/admin",
	})

	if _, _, ok := cacher.Get("https://example.com/page", "chrome", "markdown", "", ck); !ok {
		t.Fatal("expected cache hit in setup")
	}

	respBody := []byte("<html><body>fetched after cache discard</body></html>")
	tree, err := dom.Parse(respBody)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			FinalURL:    "https://example.com/page",
			StatusCode:  http.StatusOK,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			Body:        respBody,
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/page", Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			Instance: cacher,
		},
		Security: SecurityOptions{
			AllowPrivate: false,
		},
	})
	if err != nil {
		t.Fatalf("expected pipeline to complete after cache discard, got: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if !containsString(res.Output, "fetched after cache discard") {
		t.Errorf("expected fetched body to be processed, got output: %q", res.Output)
	}
}

func TestRun_RobotsCheckErrorLogged(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Hello</body></html>"))
	}))

	c, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	checker := robots.NewChecker().WithPrivate(true)
	_, err = Run(context.Background(), c, StaticEngine{}, srv.URL+"/page", Options{
		Format: FormatMarkdown,
		Security: SecurityOptions{
			AllowPrivate:  true,
			Robots:        true,
			RobotsChecker: checker,
		},
	})
	if err != nil {
		t.Fatalf("expected robots.txt error to be logged and fetch to proceed, got: %v", err)
	}
}

func TestRun_MaterializeError(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Hello</body></html>"))
	}))

	c, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	eng := &fakeEngine{err: errors.New("boom")}
	_, err = Run(context.Background(), c, eng, srv.URL, Options{
		Format: FormatMarkdown,
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err == nil {
		t.Fatal("expected error for materialize failure")
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Errorf("expected ParseError, got %T: %v", err, err)
	}
}

func TestRun_CachePutFailureLogged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX chmod semantics do not apply on Windows")
	}
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Hello</body></html>"))
	}))

	c, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if os.Getuid() == 0 {
		t.Skip("root can write into read-only directories")
	}

	tmpDir := t.TempDir()
	baseCache := cache.New(tmpDir, 1*time.Hour, 0)

	// Make the cache shard directory read-only so Put fails.
	// Use a fixed URL to avoid needing access to the unexported key method.
	shard := filepath.Join(tmpDir, "ab")
	if err := os.MkdirAll(shard, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shard, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shard, 0700) })

	_, err = Run(context.Background(), c, StaticEngine{}, srv.URL, Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			Instance: baseCache,
		},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected cache put failure to be logged and ignored, got: %v", err)
	}
}

func serveInternalServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	// Tests that omit Cache.Dir resolve to the real ~/.cache/symfetch. Point
	// HOME at a temp dir so runs are hermetic and cannot collide with the
	// developer's real cache entries.
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestRun_NoCache(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>No Cache</body></html>"))
	}))

	c, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := Run(context.Background(), c, StaticEngine{}, srv.URL, Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			NoCache: true,
		},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !containsString(res.Output, "No Cache") {
		t.Errorf("expected 'No Cache' in output, got: %q", res.Output)
	}
}

func TestRun_DefaultCacheDir(t *testing.T) {
	// DefaultDir resolves to ~/.cache/symfetch. Point HOME at a temp dir so
	// the test is hermetic and cannot collide with real cache entries.
	t.Setenv("HOME", t.TempDir())

	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Default Dir</body></html>"))
	}))

	c, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := Run(context.Background(), c, StaticEngine{}, srv.URL, Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			Dir: "",
		},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !containsString(res.Output, "Default Dir") {
		t.Errorf("expected 'Default Dir' in output, got: %q", res.Output)
	}
}

func TestRun_DefaultCacheTTL(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Default TTL</body></html>"))
	}))

	c, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	res, err := Run(context.Background(), c, StaticEngine{}, srv.URL, Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			Dir: t.TempDir(),
			TTL: 0,
		},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !containsString(res.Output, "Default TTL") {
		t.Errorf("expected 'Default TTL' in output, got: %q", res.Output)
	}
}

func TestContentKey_Deterministic(t *testing.T) {
	o1 := ContentOptions{MaxChars: 5000, IncludeLinks: true, CharThreshold: 200, MaxIslandBytes: 1000}
	o2 := ContentOptions{MaxChars: 5000, IncludeLinks: true, CharThreshold: 200, MaxIslandBytes: 1000}
	if o1.ContentKey() != o2.ContentKey() {
		t.Errorf("expected same ContentKey for identical options")
	}

	o3 := ContentOptions{MaxChars: 1000, IncludeLinks: false, CharThreshold: 200, MaxIslandBytes: 1000}
	if o1.ContentKey() == o3.ContentKey() {
		t.Errorf("expected different ContentKey for different MaxChars")
	}
}

func TestRun_CacheHitReturnsDirectly(t *testing.T) {
	tmpDir := t.TempDir()
	cacher := cache.New(tmpDir, 1*time.Hour, 0)

	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be hit when cache has valid entry")
	}))

	c := &fakeClient{
		resp: &fetch.Response{},
		err:  nil,
	}
	eng := &fakeEngine{}

	cachedBody := []byte("cached markdown output")
	ck := (&Options{Content: ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000}}).CacheKey()
	cacher.Put(srv.URL, "chrome", "markdown", "", ck, cachedBody, cache.Meta{
		URL:        srv.URL,
		FinalURL:   srv.URL,
		StatusCode: 200,
		Protocol:   "HTTP/1.1",
	})

	res, err := Run(context.Background(), c, eng, srv.URL, Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			Instance: cacher,
		},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if res.Output != "cached markdown output" {
		t.Errorf("expected cached output, got: %q", res.Output)
	}
}

func TestRun_AllowPrivate_Propagated(t *testing.T) {
	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode: 200,
			Body:       []byte("<html><body>ok</body></html>"),
			Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:   "http://127.0.0.1:9999/page",
		},
	}
	tree, err := dom.Parse([]byte("<html><body>ok</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{tree: tree}

	_, err = Run(context.Background(), c, eng, "http://127.0.0.1:9999/page", Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			NoCache: true,
		},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected no error with AllowPrivate=true, got: %v", err)
	}
}

func TestRun_CacheHit_PrivateRedirect_Discarded(t *testing.T) {
	tmpDir := t.TempDir()
	cacher := cache.New(tmpDir, 1*time.Hour, 0)

	publicURL := "https://example.com/page"
	privateRedirect := "http://127.0.0.1:9999/secret"
	ck := (&Options{Content: ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000}}).CacheKey()
	cacher.Put(publicURL, "chrome", "markdown", "", ck, []byte("cached secret"), cache.Meta{
		URL:      publicURL,
		FinalURL: privateRedirect,
	})

	respBody := []byte("<html><body>fetched fresh</body></html>")
	tree, _ := dom.Parse(respBody)

	c := &fakeClient{
		resp: &fetch.Response{
			FinalURL:    publicURL,
			StatusCode:  http.StatusOK,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			Body:        respBody,
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, publicURL, Options{
		Format: FormatMarkdown,
		Cache: CacheOptions{
			Instance: cacher,
		},
		Security: SecurityOptions{
			AllowPrivate: false,
		},
	})
	if err != nil {
		t.Fatalf("expected pipeline to complete after cache discard, got: %v", err)
	}
	if !containsString(res.Output, "fetched fresh") {
		t.Errorf("expected fresh fetch output, got: %q", res.Output)
	}
}

func TestRun_LikelyClientRendered_NavHeavy(t *testing.T) {
	thinBody := []byte(`<html><body>
		<a href="/home">Home</a>
		<a href="/about">About</a>
		<a href="/contact">Contact</a>
		<a href="/blog">Blog</a>
		<a href="/docs">Docs</a>
	</body></html>`)
	tree, err := dom.Parse(thinBody)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode: 200,
			Body:       thinBody,
			Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:   "https://example.com/app",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/app", Options{
		Format: FormatMarkdown,
		Cache:  CacheOptions{NoCache: true},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !res.Meta.LikelyClientRendered {
		t.Error("expected LikelyClientRendered=true for nav-heavy page")
	}
}

func TestRun_LikelyClientRendered_ContentRich(t *testing.T) {
	richBody := []byte(`<html><body>
		<article>
			<h1>Go Concurrency Patterns</h1>
			<p>Go provides powerful concurrency primitives through goroutines and channels. This article explores how to use them effectively in production systems, covering best practices and common pitfalls.</p>
			<p>Goroutines are lightweight threads managed by the Go runtime. Unlike OS threads, they start with just a few kilobytes of stack, making it practical to spawn hundreds of thousands concurrently.</p>
			<p>Channels are typed conduits for communication between goroutines. They provide synchronization and data passing in a single construct, eliminating the need for locks in many cases.</p>
			<p>The select statement lets a goroutine wait on multiple channel operations, enabling complex coordination patterns without explicit synchronization primitives.</p>
			<p>In practice, most concurrent Go programs follow a few well-established patterns: fan-out/fan-in for parallel processing, pipeline for streaming data, and worker pools for bounded concurrency.</p>
		</article>
	</body></html>`)
	tree, err := dom.Parse(richBody)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode: 200,
			Body:       richBody,
			Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:   "https://example.com/article",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/article", Options{
		Format: FormatMarkdown,
		Cache:  CacheOptions{NoCache: true},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if res.Meta.LikelyClientRendered {
		t.Error("expected LikelyClientRendered=false for content-rich page")
	}
}

func TestExtractBySelector_NoMatch(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		selector string
	}{
		{
			name:     "nonexistent class",
			html:     `<html><body><p>Hello</p></body></html>`,
			selector: "div.missing",
		},
		{
			name:     "nonexistent id",
			html:     `<html><body><p>Hello</p></body></html>`,
			selector: "#nonexistent",
		},
		{
			name:     "nonexistent element",
			html:     `<html><body><p>Content</p></body></html>`,
			selector: "table.data",
		},
		{
			name:     "complex selector no match",
			html:     `<html><body><div class="a"><span class="b">Text</span></div></body></html>`,
			selector: "div.c > span.b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := parseHTMLNode(t, tt.html)
			got := extractBySelector(root, tt.selector)
			if got != nil {
				t.Errorf("expected nil for selector %q, got non-nil node", tt.selector)
			}
		})
	}
}

func TestSelectorError_Error(t *testing.T) {
	e := &SelectorError{Selector: "div.missing"}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
	if !containsString(msg, "div.missing") {
		t.Errorf("expected selector in error message, got: %q", msg)
	}
	if !containsString(msg, "matched no elements") {
		t.Errorf("expected description in error message, got: %q", msg)
	}
}

func TestSchemaError_Error(t *testing.T) {
	e := &SchemaError{Path: "@Recipe:name", Err: "not found"}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
	if !containsString(msg, "@Recipe:name") {
		t.Errorf("expected path in error message, got: %q", msg)
	}
	if !containsString(msg, "not found") {
		t.Errorf("expected error detail in message, got: %q", msg)
	}
}

func TestParseSitemapXML(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		wantLen  int
		wantURLs []string
	}{
		{
			name: "urlset with valid HTTP URLs",
			data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/page-a</loc></url>
  <url><loc>https://example.com/page-b</loc></url>
</urlset>`),
			wantLen:  2,
			wantURLs: []string{"https://example.com/page-a", "https://example.com/page-b"},
		},
		{
			name: "sitemapindex with valid URLs",
			data: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/s1.xml</loc></sitemap>
  <sitemap><loc>https://example.com/s2.xml</loc></sitemap>
</sitemapindex>`),
			wantLen:  2,
			wantURLs: []string{"https://example.com/s1.xml", "https://example.com/s2.xml"},
		},
		{
			name:    "invalid XML returns nil",
			data:    []byte(`not xml at all`),
			wantLen: 0,
		},
		{
			name:    "empty urlset returns nil",
			data:    []byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>`),
			wantLen: 0,
		},
		{
			name:    "javascript href filtered out",
			data:    []byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>javascript:alert(1)</loc></url></urlset>`),
			wantLen: 0,
		},
		{
			name:    "non-http scheme filtered out",
			data:    []byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>ftp://example.com/file</loc></url></urlset>`),
			wantLen: 0,
		},
		{
			name:    "fragment-only filtered out",
			data:    []byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>#section</loc></url></urlset>`),
			wantLen: 0,
		},
		{
			name: "mixed valid and invalid",
			data: []byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/good</loc></url>
  <url><loc>javascript:bad</loc></url>
  <url><loc>https://example.com/also-good</loc></url>
</urlset>`),
			wantLen:  2,
			wantURLs: []string{"https://example.com/good", "https://example.com/also-good"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links := parseSitemapXML(tt.data)
			if len(links) != tt.wantLen {
				t.Fatalf("parseSitemapXML() returned %d links, want %d", len(links), tt.wantLen)
			}
			for i, wantURL := range tt.wantURLs {
				if links[i].URL != wantURL {
					t.Errorf("link[%d].URL = %q, want %q", i, links[i].URL, wantURL)
				}
			}
		})
	}
}

func TestFetchSitemap(t *testing.T) {
	urlsetXML := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/page-a</loc></url>
  <url><loc>https://example.com/page-b</loc></url>
</urlset>`)

	indexXML := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/s1.xml</loc></sitemap>
</sitemapindex>`)

	nonXML := []byte(`<html><body>Not XML</body></html>`)

	tests := []struct {
		name    string
		resp    *fetch.Response
		respErr error
		wantLen int
	}{
		{
			name:    "urlset XML parsed correctly",
			resp:    &fetch.Response{Body: urlsetXML, StatusCode: 200},
			wantLen: 2,
		},
		{
			name:    "sitemapindex XML parsed correctly",
			resp:    &fetch.Response{Body: indexXML, StatusCode: 200},
			wantLen: 1,
		},
		{
			name:    "non-XML body returns no links",
			resp:    &fetch.Response{Body: nonXML, StatusCode: 200},
			wantLen: 0,
		},
		{
			name:    "fetch error returns nil",
			respErr: errors.New("network error"),
			wantLen: 0,
		},
		{
			name:    "empty body returns no links",
			resp:    &fetch.Response{Body: []byte{}, StatusCode: 200},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeClient{resp: tt.resp, err: tt.respErr}
			o := Options{Security: SecurityOptions{AllowPrivate: true}}
			links := fetchSitemap(context.Background(), c, "https://example.com/sitemap.xml", o)
			if len(links) != tt.wantLen {
				t.Fatalf("fetchSitemap() returned %d links, want %d", len(links), tt.wantLen)
			}
		})
	}
}

func TestIsSafeHref_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		href string
		want bool
	}{
		{"empty string", "", false},
		{"fragment only", "#section", false},
		{"fragment with text", "#top", false},
		{"javascript scheme", "javascript:alert(1)", false},
		{"javascript uppercase", "JAVASCRIPT:alert(1)", false},
		{"javascript mixed case", "JavaScript:void(0)", false},
		{"data scheme", "data:text/html,<h1>Hello</h1>", false},
		{"data uppercase", "DATA:text/html,<h1>Hello</h1>", false},
		{"vbscript scheme", "vbscript:MsgBox(1)", false},
		{"vbscript mixed case", "VBScript:MsgBox(1)", false},
		{"valid https URL", "https://example.com/page", true},
		{"valid http URL", "http://example.com/page", true},
		{"valid relative path", "/page/about", true},
		{"valid relative path dot", "../page", true},
		{"valid query string", "/page?tab=overview", true},
		{"valid fragment plus path", "/page#section", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSafeHref(tt.href); got != tt.want {
				t.Errorf("isSafeHref(%q) = %v, want %v", tt.href, got, tt.want)
			}
		})
	}
}

func TestSortCandidates(t *testing.T) {
	tests := []struct {
		name      string
		input     []CandidateURL
		wantOrder []float64
	}{
		{
			name:      "unsorted descending",
			input:     []CandidateURL{{Score: 0.3}, {Score: 0.9}, {Score: 0.5}, {Score: 1.0}},
			wantOrder: []float64{1.0, 0.9, 0.5, 0.3},
		},
		{
			name:      "already sorted",
			input:     []CandidateURL{{Score: 1.0}, {Score: 0.8}, {Score: 0.6}},
			wantOrder: []float64{1.0, 0.8, 0.6},
		},
		{
			name:      "single element",
			input:     []CandidateURL{{Score: 0.5}},
			wantOrder: []float64{0.5},
		},
		{
			name:      "empty slice",
			input:     []CandidateURL{},
			wantOrder: []float64{},
		},
		{
			name:      "all same score",
			input:     []CandidateURL{{Score: 0.7}, {Score: 0.7}, {Score: 0.7}},
			wantOrder: []float64{0.7, 0.7, 0.7},
		},
		{
			name:      "reverse sorted becomes descending",
			input:     []CandidateURL{{Score: 0.1}, {Score: 0.5}, {Score: 0.9}},
			wantOrder: []float64{0.9, 0.5, 0.1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortCandidates(tt.input)
			if len(tt.input) != len(tt.wantOrder) {
				t.Fatalf("got %d elements, want %d", len(tt.input), len(tt.wantOrder))
			}
			for i, want := range tt.wantOrder {
				if tt.input[i].Score != want {
					t.Errorf("position %d: got score %f, want %f", i, tt.input[i].Score, want)
				}
			}
		})
	}
}

func TestFindCandidatesFromSitemaps(t *testing.T) {
	sitemapXML := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/page-a</loc></url>
  <url><loc>https://example.com/page-b</loc></url>
  <url><loc>https://example.com/page-cat</loc></url>
</urlset>`

	var srvURL string
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("User-agent: *\nDisallow:\nSitemap: " + srvURL + "/sitemap.xml\n"))
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml")
			w.Write([]byte(sitemapXML))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	srvURL = srv.URL

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := robots.NewChecker().WithPrivate(true)

	c := &fakeClient{
		resp: &fetch.Response{
			Body:       []byte(sitemapXML),
			StatusCode: 200,
		},
	}

	o := Options{
		Security: SecurityOptions{
			AllowPrivate:  true,
			RobotsChecker: checker,
		},
	}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "page-cat", o)
	if len(candidates) == 0 {
		t.Fatal("expected candidates from sitemaps, got none")
	}

	for _, cand := range candidates {
		if cand.Source != "sitemap" {
			t.Errorf("expected source 'sitemap', got %q", cand.Source)
		}
		if cand.Score <= 0 {
			t.Errorf("expected positive score, got %f", cand.Score)
		}
		if !containsString(cand.URL, "example.com") {
			t.Errorf("expected URL containing example.com, got %q", cand.URL)
		}
	}
}

func TestFindCandidatesFromSitemaps_NilChecker(t *testing.T) {
	u, _ := url.Parse("https://example.com")
	c := &fakeClient{resp: &fetch.Response{Body: []byte{}, StatusCode: 200}}
	o := Options{Security: SecurityOptions{AllowPrivate: true}}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "test", o)
	if candidates != nil {
		t.Errorf("expected nil candidates when RobotsChecker is nil, got %v", candidates)
	}
}

func TestRun_CSSSelector(t *testing.T) {
	body := []byte(`<html><body>
		<nav>Navigation stuff here</nav>
		<div id="content">
			<h1>Article Title</h1>
			<p>This is the main article content that should be extracted by CSS selector.</p>
		</div>
		<footer>Footer stuff here</footer>
	</body></html>`)
	tree, err := dom.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        body,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/page",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/page", Options{
		Format:      FormatMarkdown,
		CSSSelector: "#content",
		Cache:       CacheOptions{NoCache: true},
		Security:    SecurityOptions{AllowPrivate: true},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !containsString(res.Output, "Article Title") {
		t.Errorf("expected 'Article Title' in output, got: %q", res.Output)
	}
	if !containsString(res.Output, "main article content") {
		t.Errorf("expected 'main article content' in output, got: %q", res.Output)
	}
}

func TestRun_CSSSelector_NoMatch(t *testing.T) {
	body := []byte(`<html><body><p>Hello</p></body></html>`)
	tree, err := dom.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        body,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/page",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	_, err = Run(context.Background(), c, eng, "https://example.com/page", Options{
		Format:      FormatMarkdown,
		CSSSelector: "div.nonexistent",
		Cache:       CacheOptions{NoCache: true},
		Security:    SecurityOptions{AllowPrivate: true},
	})
	if err == nil {
		t.Fatal("expected error for non-matching CSS selector")
	}
	var selErr *SelectorError
	if !errors.As(err, &selErr) {
		t.Errorf("expected SelectorError, got %T: %v", err, err)
	}
}

func TestRun_SchemaPath(t *testing.T) {
	body := []byte(`<html><head>
<script type="application/ld+json">{"@type":"Article","headline":"Test Headline","author":"Test Author"}</script>
</head><body>
<p>This page has enough content to pass the character threshold for processing through the pipeline.</p>
<p>Additional content to ensure the pipeline works correctly end to end without thin content detection.</p>
</body></html>`)
	tree, err := dom.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        body,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/article",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/article", Options{
		Format:     FormatMarkdown,
		SchemaPath: "@Article:headline",
		Cache:      CacheOptions{NoCache: true},
		Security:   SecurityOptions{AllowPrivate: true},
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !containsString(res.Output, "Test Headline") {
		t.Errorf("expected 'Test Headline' in output, got: %q", res.Output)
	}
}

func TestRun_SchemaPath_NoMatch(t *testing.T) {
	body := []byte(`<html><head>
<script type="application/ld+json">{"@type":"Article","headline":"Test Headline"}</script>
</head><body>
<p>Content here to pass threshold.</p>
</body></html>`)
	tree, err := dom.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        body,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/article",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/article", Options{
		Format:     FormatMarkdown,
		SchemaPath: "@Recipe:name",
		Cache:      CacheOptions{NoCache: true},
		Security:   SecurityOptions{AllowPrivate: true},
	})
	if err != nil {
		t.Fatalf("expected no error for schema miss (should log warning), got: %v", err)
	}
	if res.Output != "" {
		t.Errorf("expected empty output for schema miss, got: %q", res.Output)
	}
}

func TestRun_SchemaPath_MalformedPath(t *testing.T) {
	body := []byte(`<html><head>
<script type="application/ld+json">{"@type":"Article","headline":"Test"}</script>
</head><body>
<p>Content here to pass the character threshold for processing through the pipeline.</p>
</body></html>`)
	tree, err := dom.Parse(body)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        body,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/article",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	_, err = Run(context.Background(), c, eng, "https://example.com/article", Options{
		Format:     FormatMarkdown,
		SchemaPath: "Recipe:name",
		Cache:      CacheOptions{NoCache: true},
		Security:   SecurityOptions{AllowPrivate: true},
	})
	if err == nil {
		t.Fatal("expected SchemaError for malformed path")
	}
	var schemaErr *SchemaError
	if !errors.As(err, &schemaErr) {
		t.Errorf("expected SchemaError, got %T: %v", err, err)
	}
}

// --- Issue #178 regression tests ---

func TestExtractBySelector_Match(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		selector string
		wantText string
	}{
		{
			name:     "h1 element",
			html:     `<html><body><h1>Hello World</h1><p>Other</p></body></html>`,
			selector: "h1",
			wantText: "Hello World",
		},
		{
			name:     "body element",
			html:     `<html><body><p>Body content here</p></body></html>`,
			selector: "body",
			wantText: "Body content here",
		},
		{
			name:     "multi-match p elements",
			html:     `<html><body><p>First</p><p>Second</p><p>Third</p></body></html>`,
			selector: "p",
			wantText: "First",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := parseHTMLNode(t, tt.html)
			got := extractBySelector(root, tt.selector)
			if got == nil {
				t.Fatalf("extractBySelector(%q) returned nil, want non-nil", tt.selector)
			}
			if !containsString(renderText(got), tt.wantText) {
				t.Errorf("extractBySelector(%q) text = %q, want to contain %q", tt.selector, renderText(got), tt.wantText)
			}
		})
	}
}

func renderText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func TestProbeAncestors(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<html><body><a href="/page-a">Page A</a><a href="/page-b">Page B</a></body></html>`))
	}))
	defer srv.Close()

	c := newInternalTestClient(t)
	defer c.Close()

	tests := []struct {
		name    string
		rawURL  string
		wantNil bool
	}{
		{
			name:    "single segment returns nil",
			rawURL:  srv.URL + "/",
			wantNil: true,
		},
		{
			name:    "multi-segment probes ancestors",
			rawURL:  srv.URL + "/docs/api/endpoint",
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := Options{
				Security: SecurityOptions{
					AllowPrivate: true,
				},
			}
			hints := probeAncestors(context.Background(), c, tt.rawURL, o)
			if tt.wantNil && hints != nil {
				t.Errorf("expected nil hints, got %v", hints)
			}
			if !tt.wantNil && hints == nil {
				t.Error("expected non-nil hints")
			}
		})
	}
}

func TestFindCandidatesFromSitemaps_NoSitemaps(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow:\n"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := robots.NewChecker().WithPrivate(true)

	c := &fakeClient{
		resp: &fetch.Response{Body: []byte{}, StatusCode: 200},
	}
	o := Options{
		Security: SecurityOptions{
			AllowPrivate:  true,
			RobotsChecker: checker,
		},
	}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "test", o)
	if len(candidates) != 0 {
		t.Errorf("expected no candidates when no sitemaps defined, got %d", len(candidates))
	}
}

func TestFindCandidatesFromSitemaps_SitemapError(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := robots.NewChecker().WithPrivate(true)

	c := &fakeClient{
		resp: &fetch.Response{Body: []byte{}, StatusCode: 200},
	}
	o := Options{
		Security: SecurityOptions{
			AllowPrivate:  true,
			RobotsChecker: checker,
		},
	}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "test", o)
	if len(candidates) != 0 {
		t.Errorf("expected no candidates when sitemap fetch fails, got %d", len(candidates))
	}
}

// --- Issue #177 regression tests ---

func TestCacheKey_IncludesAllOptions(t *testing.T) {
	base := Options{
		Format:  FormatMarkdown,
		Content: ContentOptions{MaxChars: 20000, IncludeLinks: false, CharThreshold: 500, MaxIslandBytes: 5000},
	}

	withSelector := base
	withSelector.CSSSelector = "div.article"
	if base.CacheKey() == withSelector.CacheKey() {
		t.Error("expected different CacheKey when CSSSelector differs")
	}

	withFM := base
	withFM.Frontmatter = true
	if base.CacheKey() == withFM.CacheKey() {
		t.Error("expected different CacheKey when Frontmatter differs")
	}

	withSchema := base
	withSchema.SchemaPath = "@Article:name"
	if base.CacheKey() == withSchema.CacheKey() {
		t.Error("expected different CacheKey when SchemaPath differs")
	}

	dup := base
	dup.CSSSelector = "div.article"
	if withSelector.CacheKey() != dup.CacheKey() {
		t.Error("expected same CacheKey for identical options")
	}
}

func TestRun_CacheHit_WithFrontmatter_NoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	cacher := cache.New(tmpDir, 1*time.Hour, 0)

	cachedBody := []byte("# Cached Output\n\nSome content here.")
	ck := (&Options{
		Format:      FormatMarkdown,
		Content:     ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000},
		Frontmatter: true,
	}).CacheKey()
	cacher.Put("https://example.com/page", "chrome", "markdown", "", ck, cachedBody, cache.Meta{
		URL:        "https://example.com/page",
		FinalURL:   "https://example.com/page",
		StatusCode: 200,
		Protocol:   "HTTP/1.1",
	})

	c := &fakeClient{resp: &fetch.Response{}}
	eng := &fakeEngine{}

	var panicVal interface{}
	var res *Result
	func() {
		defer func() { panicVal = recover() }()
		var runErr error
		res, runErr = Run(context.Background(), c, eng, "https://example.com/page", Options{
			Format:      FormatMarkdown,
			Frontmatter: true,
			Cache:       CacheOptions{Instance: cacher},
			Security:    SecurityOptions{AllowPrivate: true},
		})
		if runErr != nil {
			t.Errorf("unexpected error: %v", runErr)
		}
	}()

	if panicVal != nil {
		t.Fatalf("cache hit with frontmatter=true panicked: %v", panicVal)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if res.Output != string(cachedBody) {
		t.Errorf("expected cached output, got: %q", res.Output)
	}
	if res.Doc == nil {
		t.Fatal("expected non-nil Doc on cache hit")
	}
	if res.Doc.URL != "https://example.com/page" {
		t.Errorf("expected Doc.URL to be the original URL, got: %q", res.Doc.URL)
	}
}

func TestCacheKey_DifferentCSSSelectors_DifferentOutput(t *testing.T) {
	html := []byte(`<html><body>
		<nav>Navigation stuff</nav>
		<div id="article"><h1>Article Title</h1><p>Main article content here with enough text.</p></div>
		<div id="sidebar"><p>Sidebar content here with enough text to be extracted.</p></div>
	</body></html>`)

	tree1, err := dom.Parse(html)
	if err != nil {
		t.Fatal(err)
	}
	tree2, err := dom.Parse(html)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode: 200, Body: html,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/page",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng1 := &fakeEngine{tree: tree1}
	eng2 := &fakeEngine{tree: tree2}
	tmpDir := t.TempDir()
	cacher := cache.New(tmpDir, 1*time.Hour, 0)
	secOpts := SecurityOptions{AllowPrivate: true}

	res1, err := Run(context.Background(), c, eng1, "https://example.com/page", Options{
		Format:      FormatMarkdown,
		CSSSelector: "#article",
		Cache:       CacheOptions{Instance: cacher},
		Security:    secOpts,
	})
	if err != nil {
		t.Fatal(err)
	}

	res2, err := Run(context.Background(), c, eng2, "https://example.com/page", Options{
		Format:      FormatMarkdown,
		CSSSelector: "#sidebar",
		Cache:       CacheOptions{Instance: cacher},
		Security:    secOpts,
	})
	if err != nil {
		t.Fatal(err)
	}

	if res1.Output == res2.Output {
		t.Errorf("expected different output for different CSS selectors, but both returned same output")
	}
}

func TestCacheKey_DifferentSchemaPaths_DifferentOutput(t *testing.T) {
	html := []byte(`<html><head>
<script type="application/ld+json">{"@type":"Article","headline":"Test Headline","author":"Test Author"}</script>
</head><body>
<p>Content here to pass the character threshold for processing through the pipeline. Extra text ensures no thin content detection.</p>
<p>Additional paragraphs to ensure enough content is present in the page for the pipeline to work correctly.</p>
</body></html>`)
	tree1, err := dom.Parse(html)
	if err != nil {
		t.Fatal(err)
	}
	tree2, err := dom.Parse(html)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode: 200, Body: html,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/article",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng1 := &fakeEngine{tree: tree1}
	eng2 := &fakeEngine{tree: tree2}
	tmpDir := t.TempDir()
	cacher := cache.New(tmpDir, 1*time.Hour, 0)
	secOpts := SecurityOptions{AllowPrivate: true}

	res1, err := Run(context.Background(), c, eng1, "https://example.com/article", Options{
		Format:     FormatMarkdown,
		SchemaPath: "@Article:headline",
		Cache:      CacheOptions{Instance: cacher},
		Security:   secOpts,
	})
	if err != nil {
		t.Fatal(err)
	}

	res2, err := Run(context.Background(), c, eng2, "https://example.com/article", Options{
		Format:     FormatMarkdown,
		SchemaPath: "@Article:author",
		Cache:      CacheOptions{Instance: cacher},
		Security:   secOpts,
	})
	if err != nil {
		t.Fatal(err)
	}

	if res1.Output == res2.Output {
		t.Errorf("expected different output for different schema paths, but both returned same output")
	}
}

func TestOptions_SetDefaults_StoreFullText_CharLimitZero(t *testing.T) {
	o := Options{
		StoreFullText: true,
		CharLimit:     0,
	}
	o.setDefaults()
	if o.CharLimit != DefaultCharLimit {
		t.Errorf("expected CharLimit=%d after setDefaults, got %d", DefaultCharLimit, o.CharLimit)
	}
}

func TestRun_StoreFullText(t *testing.T) {
	longBody := []byte(`<html><body><article>` + strings.Repeat("<p>"+strings.Repeat("word ", 50)+"</p>", 200) + `</article></body></html>`)
	tree, err := dom.Parse(longBody)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        longBody,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/long",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}
	storeDir := t.TempDir()

	res, err := Run(context.Background(), c, eng, "https://example.com/long", Options{
		Format: FormatMarkdown,
		Content: ContentOptions{
			MaxChars: 100000,
		},
		Cache: CacheOptions{NoCache: true},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
		StoreFullText: true,
		CharLimit:     2000,
		StoreDir:      storeDir,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(res.Output, "Full text stored:") {
		t.Errorf("expected 'Full text stored:' footer in output, got:\n%s", res.Output[:min(200, len(res.Output))])
	}
}

func TestRun_StoreFullText_DefaultStoreDir(t *testing.T) {
	longBody := []byte(`<html><body><article>` + strings.Repeat("<p>"+strings.Repeat("word ", 50)+"</p>", 200) + `</article></body></html>`)
	tree, err := dom.Parse(longBody)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        longBody,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/long",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/long", Options{
		Format: FormatMarkdown,
		Content: ContentOptions{
			MaxChars: 100000,
		},
		Cache: CacheOptions{NoCache: true},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
		StoreFullText: true,
		CharLimit:     2000,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(res.Output, "Full text stored:") {
		t.Errorf("expected 'Full text stored:' footer, got:\n%s", res.Output[:min(200, len(res.Output))])
	}
}

func TestRun_StoreFullText_ShortContent(t *testing.T) {
	shortBody := []byte(`<html><body><p>Short content</p></body></html>`)
	tree, err := dom.Parse(shortBody)
	if err != nil {
		t.Fatal(err)
	}

	c := &fakeClient{
		resp: &fetch.Response{
			StatusCode:  200,
			Body:        shortBody,
			Headers:     map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
			FinalURL:    "https://example.com/short",
			ContentType: "text/html; charset=utf-8",
		},
	}
	eng := &fakeEngine{tree: tree}

	res, err := Run(context.Background(), c, eng, "https://example.com/short", Options{
		Format: FormatMarkdown,
		Content: ContentOptions{
			MaxChars: 20000,
		},
		Cache: CacheOptions{NoCache: true},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
		StoreFullText: true,
		CharLimit:     50000,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if strings.Contains(res.Output, "Full text stored:") {
		t.Errorf("should not contain footer for short content, got:\n%s", res.Output)
	}
}

func TestFindCandidatesFromSitemaps_SSRFBlocked(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow:\nSitemap: http://127.0.0.1/sitemap.xml\n"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := robots.NewChecker().WithPrivate(true)

	c := &fakeClient{resp: &fetch.Response{Body: []byte{}, StatusCode: 200}}
	o := Options{
		Security: SecurityOptions{
			AllowPrivate:  false,
			Robots:        true,
			RobotsChecker: checker,
		},
	}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "test", o)
	if len(candidates) != 0 {
		t.Errorf("expected no candidates when sitemap URL is blocked by SSRF, got %d", len(candidates))
	}
}

func TestFindCandidatesFromSitemaps_RobotsDisallowed(t *testing.T) {
	var srvURL string
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow: /sitemap.xml\nSitemap: " + srvURL + "/sitemap.xml\n"))
	}))
	defer srv.Close()
	srvURL = srv.URL

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := robots.NewChecker().WithPrivate(true)

	c := &fakeClient{resp: &fetch.Response{Body: []byte{}, StatusCode: 200}}
	o := Options{
		Security: SecurityOptions{
			AllowPrivate:  true,
			Robots:        true,
			RobotsChecker: checker,
		},
	}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "test", o)
	if len(candidates) != 0 {
		t.Errorf("expected no candidates when sitemap is disallowed by robots.txt, got %d", len(candidates))
	}
}

func TestFindCandidatesFromSitemaps_RobotsCheckError(t *testing.T) {
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow:\nSitemap: http://[::1/sitemap.xml\n"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := robots.NewChecker().WithPrivate(true)

	c := &fakeClient{resp: &fetch.Response{Body: []byte{}, StatusCode: 200}}
	o := Options{
		Security: SecurityOptions{
			AllowPrivate:  true,
			Robots:        true,
			RobotsChecker: checker,
		},
	}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "test", o)
	if len(candidates) != 0 {
		t.Errorf("expected no candidates when robots check errors on malformed sitemap URL, got %d", len(candidates))
	}
}

func TestFindCandidatesFromSitemaps_MaxEntriesTruncation(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString("\n<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")
	for i := 0; i < maxSitemapEntries+1; i++ {
		_, _ = fmt.Fprintf(&b, "  <url><loc>https://example.com/page-%d</loc></url>\n", i)
	}
	b.WriteString("</urlset>")

	var srvURL string
	srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow:\nSitemap: " + srvURL + "/sitemap.xml\n"))
	}))
	defer srv.Close()
	srvURL = srv.URL

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	checker := robots.NewChecker().WithPrivate(true)

	c := &fakeClient{
		resp: &fetch.Response{
			Body:       []byte(b.String()),
			StatusCode: 200,
		},
	}
	o := Options{
		Security: SecurityOptions{
			AllowPrivate:  true,
			Robots:        true,
			RobotsChecker: checker,
		},
	}

	candidates := findCandidatesFromSitemaps(context.Background(), c, u, "page-0", o)
	if len(candidates) == 0 {
		t.Fatal("expected candidates from oversized sitemap, got none")
	}
	if len(candidates) > 3 {
		t.Errorf("expected rankCandidates to limit results to 3, got %d", len(candidates))
	}
}

func TestApplyRelevanceFilter_EmptyQuery(t *testing.T) {
	output := "unchanged output"
	got := applyRelevanceFilter(output, FormatMarkdown, "", 0, &agentdom.Document{})
	if got != output {
		t.Errorf("expected %q, got %q", output, got)
	}
}

func TestApplyRelevanceFilter_Markdown(t *testing.T) {
	md := "# Heading A\n\nalpha beta gamma\n\n# Heading B\n\ndelta epsilon zeta\n"
	got := applyRelevanceFilter(md, FormatMarkdown, "delta", 1, nil)
	if !strings.Contains(got, "delta") {
		t.Errorf("expected filtered markdown to contain 'delta', got:\n%s", got)
	}
	if strings.Contains(got, "alpha") {
		t.Errorf("expected filtered markdown to omit 'alpha', got:\n%s", got)
	}
}

func TestApplyRelevanceFilter_JSONNilDoc(t *testing.T) {
	output := "unchanged json output"
	got := applyRelevanceFilter(output, FormatJSON, "query", 1, nil)
	if got != output {
		t.Errorf("expected %q, got %q", output, got)
	}
}

func TestApplyRelevanceFilter_JSON(t *testing.T) {
	doc := &agentdom.Document{
		URL: "https://example.com",
		Content: []agentdom.Element{
			{Category: "heading", Text: "Alpha section"},
			{Category: "paragraph", Text: "Beta section"},
		},
	}
	got := applyRelevanceFilter("fallback", FormatJSON, "beta", 1, doc)
	if got == "fallback" {
		t.Fatal("expected JSON output, got fallback")
	}
	if !strings.Contains(got, "Beta section") {
		t.Errorf("expected JSON to contain 'Beta section', got:\n%s", got)
	}
	if strings.Contains(got, "Alpha section") {
		t.Errorf("expected JSON to omit 'Alpha section', got:\n%s", got)
	}
}

func TestApplyRelevanceFilter_DefaultFormat(t *testing.T) {
	output := "unchanged text output"
	got := applyRelevanceFilter(output, FormatText, "query", 1, &agentdom.Document{})
	if got != output {
		t.Errorf("expected %q, got %q", output, got)
	}
}

func TestProbeAncestors_EdgeCases(t *testing.T) {
	c := newInternalTestClient(t)
	defer c.Close()

	// 1. Invalid URL check
	{
		o := Options{Security: SecurityOptions{AllowPrivate: true}}
		hints := probeAncestors(context.Background(), c, "://invalid-url", o)
		if hints != nil {
			t.Errorf("expected nil hints for invalid URL, got %v", hints)
		}
	}

	// 2. Single segment / empty path check
	{
		o := Options{Security: SecurityOptions{AllowPrivate: true}}
		hints := probeAncestors(context.Background(), c, "https://example.com", o)
		if hints != nil {
			t.Errorf("expected nil hints for single segment URL, got %v", hints)
		}
	}

	// 3. SSRF checks (AllowPrivate is false, block private IP)
	{
		o := Options{Security: SecurityOptions{AllowPrivate: false}}
		hints := probeAncestors(context.Background(), c, "http://127.0.0.1/docs/api/endpoint", o)
		if hints != nil {
			t.Errorf("expected nil hints for SSRF-blocked URL, got %v", hints)
		}
		// Root URL block as well
		hintsRoot := probeAncestors(context.Background(), c, "http://127.0.0.1/endpoint", o)
		if hintsRoot != nil {
			t.Errorf("expected nil hints for SSRF-blocked root URL, got %v", hintsRoot)
		}
	}

	// 4. Robots.txt block check
	{
		srv := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				w.Header().Set("Content-Type", "text/plain")
				w.Write([]byte("User-agent: *\nDisallow: /docs/\n"))
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><body><a href="/docs/api/page">Page</a></body></html>`))
		}))
		defer srv.Close()

		checker := robots.NewChecker().WithPrivate(true)
		o := Options{
			Security: SecurityOptions{
				AllowPrivate:  true,
				Robots:        true,
				RobotsChecker: checker,
			},
		}
		hints := probeAncestors(context.Background(), c, srv.URL+"/docs/api/endpoint", o)
		if hints != nil {
			t.Errorf("expected nil hints due to robots.txt block on ancestor, got %v", hints)
		}

		// Block at root
		srv2 := serveInternalServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/robots.txt" {
				w.Header().Set("Content-Type", "text/plain")
				w.Write([]byte("User-agent: *\nDisallow: /\n"))
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><body>Root</body></html>`))
		}))
		defer srv2.Close()
		hints2 := probeAncestors(context.Background(), c, srv2.URL+"/endpoint", o)
		if hints2 != nil {
			t.Errorf("expected nil hints due to robots.txt block on root, got %v", hints2)
		}
	}
}

func TestFindCandidatesFromAncestor(t *testing.T) {
	resp := &fetch.Response{
		StatusCode: 200,
		Body: []byte(`<html><body>
			<a href="/docs/api/endpoint-v2">New Endpoint</a>
			<a href="/docs/api/some-other">Other</a>
			<a href="mailto:test@example.com">Email</a>
			<a href="javascript:void(0)">JS</a>
		</body></html>`),
		FinalURL: "https://example.com/docs/api/",
	}
	candidates := findCandidatesFromAncestor(resp, "https://example.com/docs/api/", "endpoint")
	if len(candidates) == 0 {
		t.Fatal("expected candidates, got none")
	}
	if candidates[0].Title != "New Endpoint" {
		t.Errorf("expected candidate 'New Endpoint', got: %s", candidates[0].Title)
	}

	// Case with empty response body
	emptyResp := &fetch.Response{StatusCode: 200, Body: []byte{}}
	if res := findCandidatesFromAncestor(emptyResp, "https://example.com", "target"); res != nil {
		t.Errorf("expected nil candidates for empty body, got %v", res)
	}
}
