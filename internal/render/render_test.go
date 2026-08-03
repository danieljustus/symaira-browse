package render

import (
	"strings"
	"testing"
)

const staticPage = `<html lang="de">
<head><title>Fixture</title>
<script type="application/ld+json">{"@type":"Article","headline":"Fixture"}</script>
</head>
<body>
<h1>Überschrift eins</h1>
<p>Ein Absatz mit <strong>fett</strong>, <em>kursiv</em> und einem
<a href="/ziel">Link</a>.</p>
<ul>
  <li>Punkt eins</li>
  <li>Punkt zwei</li>
</ul>
<pre><code>code block</code></pre>
<blockquote>Zitat</blockquote>
<script>window.evil = true;</script>
<style>.hidden{display:none}</style>
<h2>Unterpunkt</h2>
</body>
</html>`

func TestMarkdownRendersStaticPage(t *testing.T) {
	markdown := Markdown(staticPage)
	for _, expected := range []string{"# Überschrift eins", "**fett**", "*kursiv*", "[Link](/ziel)", "- Punkt eins", "```\ncode block", "## Unterpunkt"} {
		if !strings.Contains(markdown, expected) {
			t.Errorf("markdown is missing %q:\n%s", expected, markdown)
		}
	}
	for _, forbidden := range []string{"window.evil", ".hidden", "Überschrift eins</h1>"} {
		if strings.Contains(markdown, forbidden) {
			t.Errorf("markdown must not contain %q", forbidden)
		}
	}
}

func TestOutlineReturnsHeadingsOnly(t *testing.T) {
	outline := Outline(staticPage)
	if len(outline) != 2 {
		t.Fatalf("outline = %#v, want 2 headings", outline)
	}
	if outline[0].Level != 1 || outline[0].Text != "Überschrift eins" {
		t.Fatalf("outline[0] = %#v", outline[0])
	}
	if outline[1].Level != 2 || outline[1].Text != "Unterpunkt" {
		t.Fatalf("outline[1] = %#v", outline[1])
	}
}

func TestLangFromHTML(t *testing.T) {
	if got := Lang(staticPage); got != "de" {
		t.Fatalf("lang = %q, want de", got)
	}
}

func TestSchemaTypeFromJSONLD(t *testing.T) {
	if got := schemaTypeFromHTML(staticPage); got != "Article" {
		t.Fatalf("schema_type = %q, want Article", got)
	}
}

func TestRenderMarkdownDocument(t *testing.T) {
	document, err := Render(staticPage, "Fixture", "https://example.test/", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "Fixture" || document.Lang != "de" || document.SchemaType != "Article" {
		t.Fatalf("metadata = %#v", document)
	}
	if document.URL != "https://example.test/" {
		t.Fatalf("url = %q", document.URL)
	}
	if document.TokensEst != document.CharCount/4 {
		t.Fatalf("tokens_est = %d, char_count = %d", document.TokensEst, document.CharCount)
	}
	if document.Truncated {
		t.Fatal("small page must not be truncated")
	}
	if !strings.Contains(document.Markdown, "# Überschrift eins") {
		t.Fatalf("markdown = %q", document.Markdown)
	}
	if document.FetchedAt == "" {
		t.Fatal("fetched_at must be set")
	}
}

func TestRenderOutlineDocument(t *testing.T) {
	document, err := Render(staticPage, "Fixture", "https://example.test/", Options{Outline: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Outline) != 2 {
		t.Fatalf("outline = %#v", document.Outline)
	}
	if document.Markdown != "" {
		t.Fatalf("outline mode must not emit markdown, got %q", document.Markdown)
	}
}

func TestRenderRawDocument(t *testing.T) {
	document, err := Render(staticPage, "Fixture", "https://example.test/", Options{Raw: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document.Raw, "<h1>") {
		t.Fatalf("raw = %q", document.Raw)
	}
	if document.Markdown != "" {
		t.Fatalf("raw mode must not emit markdown, got %q", document.Markdown)
	}
}

func TestRenderSelector(t *testing.T) {
	document, err := Render(staticPage, "Fixture", "https://example.test/", Options{Selector: "ul"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(document.Markdown, "Überschrift") {
		t.Fatalf("selector render must exclude the h1: %q", document.Markdown)
	}
	if !strings.Contains(document.Markdown, "Punkt eins") {
		t.Fatalf("selector render must include the list: %q", document.Markdown)
	}
}

func TestRenderFilter(t *testing.T) {
	document, err := Render(staticPage, "Fixture", "https://example.test/", Options{Filter: "blockquote"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(document.Markdown, "Zitat") {
		t.Fatalf("filtered render must exclude the blockquote: %q", document.Markdown)
	}
	if !strings.Contains(document.Markdown, "Überschrift eins") {
		t.Fatalf("filtered render must keep the rest: %q", document.Markdown)
	}
}

func TestTruncateWindowSemantics(t *testing.T) {
	body := strings.Repeat("a", 100)
	truncated, isTruncated := Truncate(body, 40)
	if !isTruncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(truncated, "[truncated:") {
		t.Fatalf("missing truncation marker: %q", truncated)
	}
	// head and tail are preserved, the middle is gone
	if !strings.HasPrefix(truncated, "aaaaaaaaaa") {
		t.Fatalf("head not preserved: %q", truncated)
	}
	if !strings.HasSuffix(truncated, "aaaaaaaaaa") {
		t.Fatalf("tail not preserved: %q", truncated)
	}
}

func TestTruncateRuneSafe(t *testing.T) {
	body := strings.Repeat("あ", 100)
	truncated, _ := Truncate(body, 40)
	for _, r := range truncated {
		if r == 0xfffd {
			t.Fatal("truncation produced a replacement rune")
		}
	}
}

func TestTruncateBelowBudgetUntouched(t *testing.T) {
	body := "short"
	truncated, isTruncated := Truncate(body, 100)
	if isTruncated || truncated != body {
		t.Fatalf("truncated = %q, %v", truncated, isTruncated)
	}
}

func TestRenderTruncatesAtBudget(t *testing.T) {
	long := "<html><body><p>" + strings.Repeat("x", 500) + "</p></body></html>"
	document, err := Render(long, "Long", "https://example.test/", Options{MaxChars: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !document.Truncated {
		t.Fatal("expected truncation")
	}
	if !document.Meta.Truncated {
		t.Fatal("meta must report truncation")
	}
}
