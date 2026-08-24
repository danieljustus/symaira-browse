package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
	"github.com/danieljustus/symaira-browse/internal/fetch/render"
	"github.com/danieljustus/symaira-corekit/domkit"
)

// TestHermeticReadConformance verifies the #207 AC 5: hermetic static-fixture
// conformance test for frontmatter, field names, and Markdown between the
// absorbed fetch schema and symbrowse read (no public DNS or internet).
func TestHermeticReadConformance(t *testing.T) {
	fixtureHTML := `<!DOCTYPE html>
<html lang="en">
<head>
  <title>Mars Water Discovery</title>
  <script type="application/ld+json">
  {
    "@context": "https://schema.org",
    "@type": "NewsArticle",
    "headline": "Scientists Discover Water on Mars"
  }
  </script>
</head>
<body>
  <nav id="site-nav"><a href="/">Home</a> <a href="/news">News</a></nav>
  <article id="main-article">
    <h1>Scientists Discover Water on Mars</h1>
    <p class="lead">Liquid water confirmed beneath the Martian south pole.</p>
    <p>Researchers using ground-penetrating radar have discovered a large reservoir of liquid water.</p>
    <h2>Implications</h2>
    <p>This discovery provides crucial clues for astrobiology and future human exploration.</p>
  </article>
  <footer id="footer"><p>&copy; 2026 Space News</p></footer>
</body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixtureHTML))
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()

	// 1. Run through absorbed fetch pipeline directly
	client, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		t.Fatalf("create fetch client: %v", err)
	}
	defer client.Close()

	pipelineResult, err := pipeline.Run(ctx, client, pipeline.StaticEngine{}, server.URL, pipeline.Options{
		Security: pipeline.SecurityOptions{AllowPrivate: true},
	})
	if err != nil {
		t.Fatalf("pipeline.Run: %v", err)
	}

	fetchFrontmatter := render.GenerateFrontmatter(pipelineResult.Meta, pipelineResult.Doc)

	// 2. Run through daemon + static engine + domkit (symbrowse read path)
	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{})
	if _, err := registry.Ensure("s"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, "", daemon.NavigationRuntimeOptions{
		Engine:       "static",
		AllowPrivate: true,
	})
	defer func() { _ = runtime.Close() }()

	readArgs, _ := json.Marshal(map[string]any{"url": server.URL})
	readResponse, _, err := runtime.Handle(ctx, daemon.Frame{
		Cmd:     "read",
		Args:    readArgs,
		Session: "s",
	})
	if err != nil {
		t.Fatalf("daemon read: %v", err)
	}

	var material struct {
		HTML  string `json:"html"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	materialRaw, _ := json.Marshal(readResponse)
	if err := json.Unmarshal(materialRaw, &material); err != nil {
		t.Fatalf("unmarshal read material: %v", err)
	}

	domkitDoc, err := domkit.Render(material.HTML, material.Title, material.URL, domkit.Options{})
	if err != nil {
		t.Fatalf("domkit.Render: %v", err)
	}
	browseFrontmatter := domkit.Frontmatter(domkitDoc)

	// 3. Conformance checks: Frontmatter YAML keys and values
	var fetchFMMap map[string]any
	if err := yaml.Unmarshal([]byte(strings.Trim(fetchFrontmatter, "-\n")), &fetchFMMap); err != nil {
		t.Fatalf("parse fetch frontmatter YAML: %v", err)
	}

	var browseFMMap map[string]any
	if err := yaml.Unmarshal([]byte(strings.Trim(browseFrontmatter, "-\n")), &browseFMMap); err != nil {
		t.Fatalf("parse browse frontmatter YAML: %v", err)
	}

	// Schema keys check
	for _, requiredKey := range []string{"title", "url", "fetched_at", "lang", "tokens_est", "schema_type"} {
		if _, ok := browseFMMap[requiredKey]; !ok {
			t.Errorf("browse frontmatter missing required key %q: %v", requiredKey, browseFMMap)
		}
		if _, ok := fetchFMMap[requiredKey]; !ok {
			t.Errorf("fetch frontmatter missing required key %q: %v", requiredKey, fetchFMMap)
		}
	}

	if browseFMMap["title"] != "Mars Water Discovery" {
		t.Errorf("browse title = %v, want 'Mars Water Discovery'", browseFMMap["title"])
	}
	if fetchFMMap["title"] != "Mars Water Discovery" {
		t.Errorf("fetch title = %v, want 'Mars Water Discovery'", fetchFMMap["title"])
	}
	if browseFMMap["lang"] != "en" || fetchFMMap["lang"] != "en" {
		t.Errorf("lang mismatch: browse=%v fetch=%v", browseFMMap["lang"], fetchFMMap["lang"])
	}
	if browseFMMap["schema_type"] != "NewsArticle" || fetchFMMap["schema_type"] != "NewsArticle" {
		t.Errorf("schema_type mismatch: browse=%v fetch=%v", browseFMMap["schema_type"], fetchFMMap["schema_type"])
	}

	// FetchedAt must be valid RFC3339 in both
	if browseTime, err := time.Parse(time.RFC3339, browseFMMap["fetched_at"].(string)); err != nil || browseTime.IsZero() {
		t.Errorf("invalid browse fetched_at timestamp: %v", browseFMMap["fetched_at"])
	}
	if fetchTime, err := time.Parse(time.RFC3339, fetchFMMap["fetched_at"].(string)); err != nil || fetchTime.IsZero() {
		t.Errorf("invalid fetch fetched_at timestamp: %v", fetchFMMap["fetched_at"])
	}

	// 4. Conformance checks: JSON field names in envelope
	docJSON, err := json.Marshal(domkitDoc)
	if err != nil {
		t.Fatalf("marshal domkit doc: %v", err)
	}
	var docMap map[string]any
	if err := json.Unmarshal(docJSON, &docMap); err != nil {
		t.Fatalf("unmarshal doc json: %v", err)
	}

	for _, field := range []string{"url", "title", "lang", "fetched_at", "tokens_est", "schema_type", "markdown", "truncated", "char_count"} {
		if _, exists := docMap[field]; !exists {
			t.Errorf("JSON output missing schema field %q: %v", field, docMap)
		}
	}

	// 5. Conformance checks: Markdown content
	browseMD := domkitDoc.Markdown
	if !strings.Contains(browseMD, "Scientists Discover Water on Mars") {
		t.Errorf("browse markdown missing heading: %s", browseMD)
	}
	if !strings.Contains(browseMD, "ground-penetrating radar") {
		t.Errorf("browse markdown missing content: %s", browseMD)
	}
	if strings.Contains(browseMD, "<script") || strings.Contains(browseMD, "<nav") {
		t.Errorf("browse markdown leaked script or nav markup: %s", browseMD)
	}
}

func TestHermeticReadJSONModeConformance(t *testing.T) {
	fixtureHTML := `<!DOCTYPE html>
<html lang="de">
<head>
  <title>Deutsche Nachrichten</title>
</head>
<body>
  <h1>Wichtige Entdeckung</h1>
  <p>Hier ist der deutsche Inhalt.</p>
</body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fixtureHTML))
	}))
	t.Cleanup(server.Close)

	doc, err := domkit.Render(fixtureHTML, "Deutsche Nachrichten", server.URL, domkit.Options{})
	if err != nil {
		t.Fatalf("domkit.Render: %v", err)
	}

	data := withEngineHint(doc, false, "static content: the page renders identically with JavaScript disabled")
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Schema fields verification
	requiredFields := []string{"url", "title", "lang", "fetched_at", "tokens_est", "markdown", "truncated", "char_count", "js_required", "js_required_reason"}
	for _, field := range requiredFields {
		if _, exists := payload[field]; !exists {
			t.Errorf("missing expected field %q in JSON output: %v", field, payload)
		}
	}

	if payload["lang"] != "de" {
		t.Errorf("lang = %v, want 'de'", payload["lang"])
	}
	if payload["js_required"] != false {
		t.Errorf("js_required = %v, want false", payload["js_required"])
	}
}

func TestHermeticReadOutlineAndRawConformance(t *testing.T) {
	fixtureHTML := `<!DOCTYPE html>
<html>
<head><title>Outline Test</title></head>
<body>
  <h1>Level 1 Heading</h1>
  <p>Some text</p>
  <h2>Level 2 Subheading</h2>
  <p>More text</p>
  <h3>Level 3 Detail</h3>
</body>
</html>`

	// 1. Outline mode
	docOutline, err := domkit.Render(fixtureHTML, "Outline Test", "https://example.com/outline", domkit.Options{
		Outline: true,
	})
	if err != nil {
		t.Fatalf("domkit.Render outline: %v", err)
	}
	if len(docOutline.Outline) != 3 {
		t.Fatalf("expected 3 headings in outline, got %d", len(docOutline.Outline))
	}
	if docOutline.Outline[0].Text != "Level 1 Heading" || docOutline.Outline[0].Level != 1 {
		t.Errorf("unexpected first heading: %+v", docOutline.Outline[0])
	}
	if docOutline.Outline[1].Text != "Level 2 Subheading" || docOutline.Outline[1].Level != 2 {
		t.Errorf("unexpected second heading: %+v", docOutline.Outline[1])
	}

	// 2. Raw mode
	docRaw, err := domkit.Render(fixtureHTML, "Outline Test", "https://example.com/raw", domkit.Options{
		Raw: true,
	})
	if err != nil {
		t.Fatalf("domkit.Render raw: %v", err)
	}
	if !strings.Contains(docRaw.Raw, "<h1>Level 1 Heading</h1>") {
		t.Errorf("raw output missing raw HTML: %s", docRaw.Raw)
	}
}
