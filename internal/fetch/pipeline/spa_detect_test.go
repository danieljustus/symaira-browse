package pipeline

import (
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
)

func parseSPATestHTML(t *testing.T, h string) (*html.Node, []byte) {
	t.Helper()
	body := []byte(h)
	n, err := html.Parse(strings.NewReader(h))
	if err != nil {
		t.Fatal(err)
	}
	return n, body
}

func TestDetectSPASkeleton_Positive_NextJS(t *testing.T) {
	spaHTML := `<html><head><title>My App</title></head><body>
	<div id="__next">
		<div id="app"></div>
	</div>
	<script id="__NEXT_DATA__">{"props":{"pageProps":{}},"page":"/"}</script>
	` + strings.Repeat("<!-- padding to exceed size threshold -->\n", 200) + `</body></html>`
	root, body := parseSPATestHTML(t, spaHTML)
	islands := []semantic.DataIsland{
		{Source: "__NEXT_DATA__", JSON: []byte(`{"props":{"pageProps":{}}}`)},
	}
	if !DetectSPASkeleton(body, root, islands) {
		t.Error("expected SPA skeleton detection for Next.js page with __NEXT_DATA__ + empty root")
	}
}

func TestDetectSPASkeleton_Positive_EmptyAppRoot(t *testing.T) {
	spaHTML := `<html><head><title>App</title></head><body>
	<div id="app"></div>
	<script type="application/json" id="initial-state">{"state":{"count":0}}</script>
	` + strings.Repeat("<!-- padding -->\n", 200) + `</body></html>`
	root, body := parseSPATestHTML(t, spaHTML)
	islands := []semantic.DataIsland{
		{Source: "initial-state", JSON: []byte(`{"state":{"count":0}}`)},
	}
	if !DetectSPASkeleton(body, root, islands) {
		t.Error("expected SPA skeleton detection for empty app root + initial-state island")
	}
}

func TestDetectSPASkeleton_Negative_ServerRendered(t *testing.T) {
	ssrHTML := `<html><head><title>Go Concurrency</title></head><body>
	<article>
		<h1>Go Concurrency Patterns</h1>
		<p>Go provides powerful concurrency primitives through goroutines and channels. This article explores how to use them effectively in production systems, covering best practices and common pitfalls.</p>
		<p>Goroutines are lightweight threads managed by the Go runtime. Unlike OS threads, they start with just a few kilobytes of stack, making it practical to spawn hundreds of thousands concurrently.</p>
		<p>Channels are typed conduits for communication between goroutines. They provide synchronization and data passing in a single construct, eliminating the need for locks in many cases.</p>
		<p>The select statement lets a goroutine wait on multiple channel operations, enabling complex coordination patterns without explicit synchronization primitives.</p>
		<p>In practice, most concurrent Go programs follow a few well-established patterns: fan-out/fan-in for parallel processing, pipeline for streaming data, and worker pools for bounded concurrency.</p>
		<p>Understanding these patterns deeply allows developers to write efficient, maintainable concurrent programs that scale well across multiple CPU cores.</p>
		<p>Each pattern has its own trade-offs and ideal use cases, which we will explore in detail throughout this comprehensive guide to Go concurrency.</p>
	</article>
	</body></html>`
	root, body := parseSPATestHTML(t, ssrHTML)
	if DetectSPASkeleton(body, root, nil) {
		t.Error("server-rendered article should NOT be detected as SPA skeleton")
	}
}

func TestDetectSPASkeleton_Negative_SmallPage(t *testing.T) {
	smallHTML := `<html><head><title>Tiny</title></head><body><p>Hello</p></body></html>`
	root, body := parseSPATestHTML(t, smallHTML)
	if DetectSPASkeleton(body, root, nil) {
		t.Error("small page should NOT be detected as SPA skeleton")
	}
}

func TestDetectSPASkeleton_Negative_LargeTextNoHydration(t *testing.T) {
	largeText := strings.Repeat("This is a paragraph of real content that provides substantial visible text on the page. ", 50)
	ssrHTML := `<html><head><title>Article</title></head><body><article><p>` + largeText + `</p></article></body></html>`
	root, body := parseSPATestHTML(t, ssrHTML)
	if DetectSPASkeleton(body, root, nil) {
		t.Error("page with substantial visible text should NOT be detected as SPA skeleton")
	}
}

func TestDetectSPASkeleton_Negative_NextJSWithSSRContent(t *testing.T) {
	ssrContent := strings.Repeat("This is server-rendered content that provides substantial visible text on the page with real article content. ", 20)
	nextHTML := `<html><head><title>Next SSR</title></head><body>
	<div id="__next"><p>` + ssrContent + `</p></div>
	<script id="__NEXT_DATA__">{"props":{"pageProps":{}},"page":"/"}</script>
	</body></html>`
	root, body := parseSPATestHTML(t, nextHTML)
	islands := []semantic.DataIsland{
		{Source: "__NEXT_DATA__", JSON: []byte(`{"props":{"pageProps":{}}}`)},
	}
	if DetectSPASkeleton(body, root, islands) {
		t.Error("Next.js page with substantial SSR content should NOT be detected as SPA skeleton")
	}
}

func TestCountVisibleText_SkipsScript(t *testing.T) {
	h := `<body>
		<script>var data = "lots of invisible text here that should not be counted in visible text"</script>
		<p>Hello World</p>
	</body>`
	n, _ := parseSPATestHTML(t, h)
	got := countVisibleText(n)
	if got != 11 { // "Hello World" = 11 bytes
		t.Errorf("countVisibleText = %d, want 11 (should skip script content)", got)
	}
}

func TestCountVisibleText_SkipsStyle(t *testing.T) {
	h := `<body>
		<style>.hidden { display: none; }</style>
		<p>Visible text</p>
	</body>`
	n, _ := parseSPATestHTML(t, h)
	got := countVisibleText(n)
	if got != 12 { // "Visible text" = 12 bytes
		t.Errorf("countVisibleText = %d, want 12 (should skip style content)", got)
	}
}

func TestHasHydrationIslands_True(t *testing.T) {
	islands := []semantic.DataIsland{
		{Source: "ld+json", JSON: []byte(`{"@type":"Article"}`)},
		{Source: "__NEXT_DATA__", JSON: []byte(`{"pageProps":{}}`)},
	}
	if !hasHydrationIslands(islands) {
		t.Error("expected hydration islands detection for __NEXT_DATA__")
	}
}

func TestHasHydrationIslands_False(t *testing.T) {
	islands := []semantic.DataIsland{
		{Source: "ld+json", JSON: []byte(`{"@type":"Article"}`)},
	}
	if hasHydrationIslands(islands) {
		t.Error("ld+json alone should NOT be detected as hydration")
	}
}

func TestHasEmptyFrameworkRoot_True(t *testing.T) {
	n, _ := parseSPATestHTML(t, `<body><div id="app"></div></body>`)
	if !hasEmptyFrameworkRoot(n) {
		t.Error("expected empty framework root detection for <div id='app'>")
	}
}

func TestHasEmptyFrameworkRoot_False_WithContent(t *testing.T) {
	n, _ := parseSPATestHTML(t, `<body><div id="app"><p>Content here</p></div></body>`)
	if hasEmptyFrameworkRoot(n) {
		t.Error("<div id='app'> with content should NOT be detected as empty framework root")
	}
}

func TestHasEmptyFrameworkRoot_False_NoRoot(t *testing.T) {
	n, _ := parseSPATestHTML(t, `<body><div id="container"><p>Content</p></div></body>`)
	if hasEmptyFrameworkRoot(n) {
		t.Error("non-framework root ID should NOT be detected")
	}
}
