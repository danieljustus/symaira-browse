package pipeline

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/dom"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/robots"
)

func parseHTMLNode(t *testing.T, h string) *html.Node {
	t.Helper()
	n, err := html.Parse(strings.NewReader(h))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIsThinContent_TriggersOnNavShell(t *testing.T) {
	node := parseHTMLNode(t, `<body>
		<a href="/home">Home</a>
		<a href="/about">About</a>
		<a href="/contact">Contact</a>
		<a href="/blog">Blog</a>
		<a href="/docs">Docs</a>
	</body>`)
	if !isThinContent(node, 500, false) {
		t.Error("expected thin content for nav-heavy page")
	}
}

func TestIsThinContent_DoesNotTriggerOnRichContent(t *testing.T) {
	node := parseHTMLNode(t, `<body>
		<article>
			<h1>Introduction to Go Concurrency</h1>
			<p>Go provides powerful concurrency primitives through goroutines and channels. This article explores how to use them effectively in production systems.</p>
			<p>Goroutines are lightweight threads managed by the Go runtime. Unlike OS threads, they start with just a few kilobytes of stack, making it practical to spawn hundreds of thousands concurrently.</p>
			<p>Channels are typed conduits for communication between goroutines. They provide synchronization and data passing in a single construct, eliminating the need for locks in many cases.</p>
			<p>The select statement lets a goroutine wait on multiple channel operations, enabling complex coordination patterns without explicit synchronization primitives.</p>
			<p>In practice, most concurrent Go programs follow a few well-established patterns: fan-out/fan-in for parallel processing, pipeline for streaming data, and worker pools for bounded concurrency.</p>
		</article>
	</body>`)
	if isThinContent(node, 500, false) {
		t.Error("expected rich content to NOT trigger thin-content fallback")
	}
}

func TestIsThinContent_TriggerOnZeroText(t *testing.T) {
	node := parseHTMLNode(t, `<body>
		<div id="app"></div>
	</body>`)
	if !isThinContent(node, 500, false) {
		t.Error("expected thin content for empty body")
	}
}

func TestIsThinContent_ShortButMostlyText(t *testing.T) {
	node := parseHTMLNode(t, `<body><p>Hello world, short page.</p></body>`)
	if isThinContent(node, 500, false) {
		t.Error("short page with low link density should NOT be thin")
	}
}

func TestIsThinContent_BelowThresholdHighLinkDensity(t *testing.T) {
	node := parseHTMLNode(t, `<body>
		<p>Some text here.</p>
		<a href="/link1">link1 text is longer</a>
		<a href="/link2">link2 text is longer</a>
		<a href="/link3">link3 text is longer</a>
	</body>`)
	if !isThinContent(node, 500, false) {
		t.Error("expected thin content: below threshold + high link density")
	}
}

func TestIsThinContent_SPASkeletonTrigger(t *testing.T) {
	node := parseHTMLNode(t, `<body>
		<p>Some text here.</p>
		<a href="/link1">link text</a>
	</body>`)
	if isThinContent(node, 500, false) {
		t.Error("low link density without SPA signal should NOT be thin")
	}
	if !isThinContent(node, 500, true) {
		t.Error("SPA skeleton signal should trigger thin content when below threshold")
	}
}

func TestIsThinContent_SPASkeletonDoesNotOverrideRichContent(t *testing.T) {
	node := parseHTMLNode(t, `<body>
		<article>
			<h1>Go Concurrency Patterns</h1>
			<p>Go provides powerful concurrency primitives through goroutines and channels. This article explores how to use them effectively in production systems, covering best practices and common pitfalls.</p>
			<p>Goroutines are lightweight threads managed by the Go runtime. Unlike OS threads, they start with just a few kilobytes of stack, making it practical to spawn hundreds of thousands concurrently.</p>
			<p>Channels are typed conduits for communication between goroutines. They provide synchronization and data passing in a single construct, eliminating the need for locks in many cases.</p>
			<p>The select statement lets a goroutine wait on multiple channel operations, enabling complex coordination patterns without explicit synchronization primitives.</p>
			<p>In practice, most concurrent Go programs follow a few well-established patterns: fan-out/fan-in for parallel processing, pipeline for streaming data, and worker pools for bounded concurrency.</p>
		</article>
	</body>`)
	if isThinContent(node, 500, true) {
		t.Error("SPA skeleton signal should NOT override rich content above threshold")
	}
}

func TestDeriveMDTwinURL_BasicPath(t *testing.T) {
	got := deriveMDTwinURL("https://example.com/docs/guide")
	want := "https://example.com/docs/guide.md"
	if got != want {
		t.Errorf("deriveMDTwinURL(%q) = %q, want %q", "https://example.com/docs/guide", got, want)
	}
}

func TestDeriveMDTwinURL_WithQueryAndFragment(t *testing.T) {
	got := deriveMDTwinURL("https://example.com/docs/guide?tab=overview#section-2")
	want := "https://example.com/docs/guide.md?tab=overview#section-2"
	if got != want {
		t.Errorf("deriveMDTwinURL(%q) = %q, want %q", "https://example.com/docs/guide?tab=overview#section-2", got, want)
	}
}

func TestDeriveMDTwinURL_RootPath(t *testing.T) {
	got := deriveMDTwinURL("https://example.com/")
	want := "https://example.com/.md"
	if got != want {
		t.Errorf("deriveMDTwinURL(%q) = %q, want %q", "https://example.com/", got, want)
	}
}

func TestDeriveMDTwinURL_InvalidURL(t *testing.T) {
	got := deriveMDTwinURL("://bad-url")
	if got != "" {
		t.Errorf("expected empty string for invalid URL, got %q", got)
	}
}

func TestDeriveLLMsTxtURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/docs/guide", "https://example.com/llms.txt"},
		{"https://example.com/docs/guide?tab=1", "https://example.com/llms.txt"},
		{"https://example.com/docs/guide#section", "https://example.com/llms.txt"},
		{"https://example.com/", "https://example.com/llms.txt"},
	}
	for _, tt := range tests {
		got := deriveLLMsTxtURL(tt.input)
		if got != tt.want {
			t.Errorf("deriveLLMsTxtURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDeriveLLMsTxtURL_InvalidURL(t *testing.T) {
	got := deriveLLMsTxtURL("://bad-url")
	if got != "" {
		t.Errorf("expected empty string for invalid URL, got %q", got)
	}
}

func TestCountTextContent(t *testing.T) {
	node := parseHTMLNode(t, `<body><p>Hello World</p></body>`)
	got := countTextContent(node)
	if got != 11 {
		t.Errorf("countTextContent = %d, want 11", got)
	}
}

func TestCountLinkContent(t *testing.T) {
	node := parseHTMLNode(t, `<body>
		<p>Plain text</p>
		<a href="/foo">Link text here</a>
	</body>`)
	got := countLinkContent(node)
	if got != 14 {
		t.Errorf("countLinkContent = %d, want 14", got)
	}
}

type urlSwitchClient struct {
	responses   map[string]*fetch.Response
	errors      map[string]error
	defaultResp *fetch.Response
	defaultErr  error
}

func (c *urlSwitchClient) Fetch(_ context.Context, req fetch.Request) (*fetch.Response, error) {
	if c.errors != nil {
		if err, ok := c.errors[req.URL]; ok {
			return nil, err
		}
	}
	if c.responses != nil {
		if resp, ok := c.responses[req.URL]; ok {
			return resp, nil
		}
	}
	return c.defaultResp, c.defaultErr
}

func (c *urlSwitchClient) Close() error { return nil }

func mustParseTree(t *testing.T, rawHTML []byte) *dom.Tree {
	t.Helper()
	tree, err := dom.Parse(rawHTML)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func defaultTestOpts() Options {
	return Options{
		Format:  FormatMarkdown,
		Content: ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000},
		Security: SecurityOptions{
			AllowPrivate: true,
		},
	}
}

func TestTryFallback_SSRFBlockedMDTwin(t *testing.T) {
	c := &fakeClient{resp: &fetch.Response{}}
	eng := &fakeEngine{}
	opts := Options{
		Format:  FormatMarkdown,
		Content: ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000},
		Security: SecurityOptions{
			AllowPrivate: false,
		},
	}

	result, resp, ok := tryFallback(context.Background(), c, eng, "http://127.0.0.1:9999/page", opts)
	if ok {
		t.Error("expected fallback to fail for SSRF-blocked private URL")
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

func TestTryFallback_MDTwin404_FallsToLLMsTxt(t *testing.T) {
	llmsBody := []byte("<html><body><p>LLMs fallback content from the site-level file that is long enough to render.</p></body></html>")
	llmsTree := mustParseTree(t, llmsBody)

	client := &urlSwitchClient{
		responses: map[string]*fetch.Response{
			"https://example.com/page.md": {
				StatusCode: 404,
				Body:       []byte("<html><body>Not Found</body></html>"),
				FinalURL:   "https://example.com/page.md",
			},
			"https://example.com/llms.txt": {
				StatusCode: 200,
				Body:       llmsBody,
				Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
				FinalURL:   "https://example.com/llms.txt",
			},
		},
	}
	eng := &fakeEngine{tree: llmsTree}
	opts := defaultTestOpts()

	result, resp, ok := tryFallback(context.Background(), client, eng, "https://example.com/page", opts)
	if !ok {
		t.Fatal("expected fallback to succeed via llms.txt after .md 404")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 from llms.txt, got %d", resp.StatusCode)
	}
}

func TestFetchAndProcess_SSRFBlocked(t *testing.T) {
	c := &fakeClient{resp: &fetch.Response{}}
	eng := &fakeEngine{}
	opts := Options{
		Format:  FormatMarkdown,
		Content: ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000},
		Security: SecurityOptions{
			AllowPrivate: false,
		},
	}

	result, resp, ok := fetchAndProcess(
		context.Background(), c, eng,
		"http://192.168.1.1/admin", "http://192.168.1.1/admin",
		opts,
	)
	if ok {
		t.Error("expected fetchAndProcess to fail when SSRF guard blocks URL")
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

func TestFetchAndProcess_RobotsBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("User-agent: *\nDisallow: /\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body><p>Blocked content</p></body></html>"))
	}))
	defer srv.Close()

	checker := robots.NewChecker().WithPrivate(true)
	c := &fakeClient{resp: &fetch.Response{
		StatusCode: 200,
		Body:       []byte("<html><body><p>Blocked content</p></body></html>"),
		FinalURL:   srv.URL + "/page",
		Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
	}}
	tree := mustParseTree(t, []byte("<html><body><p>Blocked content</p></body></html>"))
	eng := &fakeEngine{tree: tree}

	opts := Options{
		Format:  FormatMarkdown,
		Content: ContentOptions{MaxChars: 20000, CharThreshold: 500, MaxIslandBytes: 5000},
		Security: SecurityOptions{
			AllowPrivate:  true,
			Robots:        true,
			RobotsChecker: checker,
		},
	}

	result, resp, ok := fetchAndProcess(
		context.Background(), c, eng,
		srv.URL+"/page", srv.URL+"/page",
		opts,
	)
	if ok {
		t.Error("expected fetchAndProcess to fail when robots.txt disallows URL")
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

func TestFetchAndProcess_FetchError(t *testing.T) {
	c := &fakeClient{err: errors.New("connection refused")}
	eng := &fakeEngine{}
	opts := defaultTestOpts()

	result, resp, ok := fetchAndProcess(
		context.Background(), c, eng,
		"https://example.com/page", "https://example.com/page",
		opts,
	)
	if ok {
		t.Error("expected fetchAndProcess to fail when fetch returns error")
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

func TestFetchAndProcess_HTTP4xxError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"404", 404},
		{"500", 500},
		{"503", 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeClient{resp: &fetch.Response{
				StatusCode: tt.statusCode,
				Body:       []byte("Error"),
				FinalURL:   "https://example.com/page",
			}}
			eng := &fakeEngine{}
			opts := defaultTestOpts()

			result, resp, ok := fetchAndProcess(
				context.Background(), c, eng,
				"https://example.com/page", "https://example.com/page",
				opts,
			)
			if ok {
				t.Errorf("expected fetchAndProcess to fail for HTTP %d", tt.statusCode)
			}
			if result != nil {
				t.Error("expected nil result")
			}
			if resp != nil {
				t.Error("expected nil response")
			}
		})
	}
}

func TestFetchAndProcess_MaterializeError(t *testing.T) {
	c := &fakeClient{resp: &fetch.Response{
		StatusCode: 200,
		Body:       []byte("<html><body>content</body></html>"),
		FinalURL:   "https://example.com/page",
		Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
	}}
	eng := &fakeEngine{err: errors.New("parse failed")}
	opts := defaultTestOpts()

	result, resp, ok := fetchAndProcess(
		context.Background(), c, eng,
		"https://example.com/page", "https://example.com/page",
		opts,
	)
	if ok {
		t.Error("expected fetchAndProcess to fail when Materialize returns error")
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if resp != nil {
		t.Error("expected nil response")
	}
}

func TestFetchAndProcess_MarkdownRenderPath(t *testing.T) {
	rawHTML := []byte(`<html><body>
		<article>
			<h1>Test Article</h1>
			<p>This is test content for the markdown rendering path in fetchAndProcess.</p>
		</article>
	</body></html>`)
	tree := mustParseTree(t, rawHTML)

	c := &fakeClient{resp: &fetch.Response{
		StatusCode: 200,
		Body:       rawHTML,
		FinalURL:   "https://example.com/article",
		Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
	}}
	eng := &fakeEngine{tree: tree}
	opts := defaultTestOpts()
	opts.Format = FormatMarkdown

	result, resp, ok := fetchAndProcess(
		context.Background(), c, eng,
		"https://example.com/article", "https://example.com/article",
		opts,
	)
	if !ok {
		t.Fatal("expected fetchAndProcess to succeed for valid markdown render path")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if result.Output == "" {
		t.Error("expected non-empty output from Markdown render")
	}
}

func TestDeriveMDTwinURL_EmptyString(t *testing.T) {
	got := deriveMDTwinURL("")
	want := ".md"
	if got != want {
		t.Errorf("deriveMDTwinURL(%q) = %q, want %q", "", got, want)
	}
}

func TestDeriveLLMsTxtURL_EmptyString(t *testing.T) {
	got := deriveLLMsTxtURL("")
	want := "/llms.txt"
	if got != want {
		t.Errorf("deriveLLMsTxtURL(%q) = %q, want %q", "", got, want)
	}
}

func TestTryFetchAndProcess(t *testing.T) {
	rawHTML := []byte(`<html><body><p>Hello fallbacks</p></body></html>`)
	tree := mustParseTree(t, rawHTML)
	c := &fakeClient{resp: &fetch.Response{
		StatusCode: 200,
		Body:       rawHTML,
		FinalURL:   "https://example.com/fallback-page",
		Headers:    map[string][]string{"Content-Type": {"text/html; charset=utf-8"}},
	}}
	eng := &fakeEngine{tree: tree}
	opts := defaultTestOpts()

	result, resp, ok := tryFetchAndProcess(
		context.Background(), c, eng,
		"https://example.com/fallback-page", "https://example.com/fallback-page",
		opts,
	)
	if !ok {
		t.Fatal("expected tryFetchAndProcess to succeed")
	}
	if result == nil || resp == nil {
		t.Fatal("expected non-nil result and response")
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}
