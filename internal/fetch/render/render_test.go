package render

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
)

// helper: parse an HTML fragment into a *html.Node for Markdown tests.
func parseHTML(t *testing.T, fragment string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	// html.Parse wraps in <html><head><body>; return the <body> node.
	var body *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if body == nil {
		t.Fatal("no <body> found in parsed HTML")
	}
	return body
}

func emptyDoc() *agentdom.Document {
	return &agentdom.Document{
		URL:         "https://example.com",
		Content:     []agentdom.Element{},
		Interactive: []agentdom.Element{},
	}
}

func fullDoc() *agentdom.Document {
	return &agentdom.Document{
		URL:      "https://example.com/page",
		FinalURL: "https://example.com/page",
		Title:    "Example Page",
		Lang:     "en",
		Content: []agentdom.Element{
			{
				Category: semantic.Category("heading"),
				Tag:      "h1",
				Text:     "Welcome",
			},
			{
				Category: semantic.Category("paragraph"),
				Tag:      "p",
				Text:     "This is a test page.",
				Children: []agentdom.Element{
					{
						Category: semantic.Category("link"),
						Tag:      "a",
						Text:     "Click here",
						Attrs:    map[string]string{"href": "https://example.com/target"},
					},
				},
			},
		},
		Interactive: []agentdom.Element{
			{
				AgentID:  "@e1",
				Category: semantic.Category("button"),
				Text:     "Submit",
				Attrs:    map[string]string{"type": "submit"},
			},
			{
				AgentID:  "@e2",
				Category: semantic.Category("link"),
				Text:     "Home",
				Attrs:    map[string]string{"href": "/"},
			},
			{
				AgentID:  "@e3",
				Category: semantic.Category("input"),
				Attrs:    map[string]string{"placeholder": "Search...", "type": "text"},
			},
		},
		Islands: []agentdom.DataIsland{
			{
				Source: "__NEXT_DATA__",
				JSON:   json.RawMessage(`{"props":{"pageProps":{"title":"Hello"}}}`),
			},
		},
	}
}

func TestMarkdown_BasicContent(t *testing.T) {
	doc := emptyDoc()
	node := parseHTML(t, "<p>Hello <strong>world</strong></p>")
	out, err := Markdown(doc, node, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Hello") {
		t.Errorf("expected output to contain 'Hello', got:\n%s", out)
	}
	if !strings.Contains(out, "**world**") {
		t.Errorf("expected bold markdown for 'world', got:\n%s", out)
	}
}

func TestMarkdown_NilContentNode(t *testing.T) {
	doc := emptyDoc()
	out, err := Markdown(doc, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output for nil contentNode with no interactive/islands, got: %q", out)
	}
}

func TestMarkdown_EmptyDocument(t *testing.T) {
	doc := emptyDoc()
	node := parseHTML(t, "<p></p>")
	out, err := Markdown(doc, node, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty/whitespace output for empty doc, got: %q", out)
	}
}

func TestMarkdown_InteractiveElements(t *testing.T) {
	doc := fullDoc()
	out, err := Markdown(doc, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "## Interactive Elements") {
		t.Error("expected '## Interactive Elements' header")
	}
	if !strings.Contains(out, "@e1") {
		t.Error("expected @e1 in output")
	}
	if !strings.Contains(out, "Submit") {
		t.Error("expected 'Submit' text in output")
	}
	if !strings.Contains(out, "(submit)") {
		t.Error("expected '(submit)' type attribute in output")
	}
	if !strings.Contains(out, "@e2") {
		t.Error("expected @e2 in output")
	}
	if !strings.Contains(out, "→ /") {
		t.Error("expected '→ /' href for @e2")
	}
	if !strings.Contains(out, "@e3") {
		t.Error("expected @e3 in output")
	}
	if !strings.Contains(out, "[Search...]") {
		t.Error("expected '[Search...]' placeholder for @e3")
	}
}

func TestMarkdown_LinksSection(t *testing.T) {
	doc := fullDoc()
	out, err := Markdown(doc, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "## Links") {
		t.Error("expected '## Links' header when includeLinks=true")
	}
	if !strings.Contains(out, "[Click here](https://example.com/target)") {
		t.Errorf("expected link markdown, got:\n%s", out)
	}
}

func TestMarkdown_LinksSectionDisabled(t *testing.T) {
	doc := fullDoc()
	out, err := Markdown(doc, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "## Links") {
		t.Error("did not expect '## Links' header when includeLinks=false")
	}
}

func TestMarkdown_LinksSkipHashOnly(t *testing.T) {
	doc := &agentdom.Document{
		URL: "https://example.com",
		Content: []agentdom.Element{
			{
				Category: semantic.Category("link"),
				Text:     "Anchor",
				Attrs:    map[string]string{"href": "#section"},
			},
			{
				Category: semantic.Category("link"),
				Text:     "Real",
				Attrs:    map[string]string{"href": "https://example.com/real"},
			},
		},
		Interactive: []agentdom.Element{},
	}
	out, err := Markdown(doc, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "#section") {
		t.Error("hash-only links should be skipped")
	}
	if !strings.Contains(out, "https://example.com/real") {
		t.Error("real links should be included")
	}
}

func TestMarkdown_LinksUseHrefAsTextWhenEmpty(t *testing.T) {
	doc := &agentdom.Document{
		URL: "https://example.com",
		Content: []agentdom.Element{
			{
				Category: semantic.Category("link"),
				Text:     "",
				Attrs:    map[string]string{"href": "https://example.com/no-text"},
			},
		},
		Interactive: []agentdom.Element{},
	}
	out, err := Markdown(doc, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[https://example.com/no-text](https://example.com/no-text)") {
		t.Errorf("expected href used as text when text is empty, got:\n%s", out)
	}
}

func TestMarkdown_DataIslands(t *testing.T) {
	doc := fullDoc()
	out, err := Markdown(doc, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "## Data") {
		t.Error("expected '## Data' header")
	}
	if !strings.Contains(out, "```json") {
		t.Error("expected json code fence")
	}
	if !strings.Contains(out, "// Source: __NEXT_DATA__") {
		t.Error("expected source comment in data island")
	}
	if !strings.Contains(out, `"props"`) {
		t.Error("expected island JSON content in output")
	}
}

func TestMarkdown_NoInteractiveElements(t *testing.T) {
	doc := &agentdom.Document{
		URL:         "https://example.com",
		Content:     []agentdom.Element{},
		Interactive: []agentdom.Element{},
	}
	out, err := Markdown(doc, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "## Interactive Elements") {
		t.Error("should not emit interactive header when list is empty")
	}
}

func TestMarkdown_NoIslands(t *testing.T) {
	doc := &agentdom.Document{
		URL:         "https://example.com",
		Content:     []agentdom.Element{},
		Interactive: []agentdom.Element{},
		Islands:     []agentdom.DataIsland{},
	}
	out, err := Markdown(doc, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "## Data") {
		t.Error("should not emit data header when islands list is empty")
	}
}

func TestMarkdown_CollectLinksFromChildren(t *testing.T) {
	doc := &agentdom.Document{
		URL: "https://example.com",
		Content: []agentdom.Element{
			{
				Category: semantic.Category("paragraph"),
				Text:     "Parent",
				Children: []agentdom.Element{
					{
						Category: semantic.Category("link"),
						Text:     "Child Link",
						Attrs:    map[string]string{"href": "https://example.com/child"},
					},
				},
			},
		},
		Interactive: []agentdom.Element{},
	}
	out, err := Markdown(doc, nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[Child Link](https://example.com/child)") {
		t.Errorf("expected nested child link in output, got:\n%s", out)
	}
}

func TestJSON_ValidOutput(t *testing.T) {
	doc := fullDoc()
	out, err := JSON(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\nOutput:\n%s", err, out)
	}
}

func TestJSON_FieldPresence(t *testing.T) {
	doc := fullDoc()
	out, err := JSON(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	requiredFields := []string{"url", "title", "lang", "content", "interactive", "islands"}
	for _, f := range requiredFields {
		if _, ok := parsed[f]; !ok {
			t.Errorf("expected field %q in JSON output", f)
		}
	}

	if parsed["url"] != "https://example.com/page" {
		t.Errorf("expected url 'https://example.com/page', got %v", parsed["url"])
	}
	if parsed["title"] != "Example Page" {
		t.Errorf("expected title 'Example Page', got %v", parsed["title"])
	}
}

func TestJSON_EmptyDocument(t *testing.T) {
	doc := emptyDoc()
	out, err := JSON(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["url"] != "https://example.com" {
		t.Errorf("expected url 'https://example.com', got %v", parsed["url"])
	}
	// Content and Interactive should be empty arrays, not null.
	content, ok := parsed["content"]
	if !ok {
		t.Fatal("expected 'content' field")
	}
	arr, ok := content.([]any)
	if !ok {
		t.Fatalf("expected content to be an array, got %T", content)
	}
	if len(arr) != 0 {
		t.Errorf("expected empty content array, got %d items", len(arr))
	}
}

func TestJSON_PrettyPrinted(t *testing.T) {
	doc := emptyDoc()
	out, err := JSON(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "\n") {
		t.Error("expected pretty-printed JSON with newlines")
	}
	if !strings.Contains(out, "  ") {
		t.Error("expected 2-space indentation in JSON output")
	}
}

func TestJSON_IslandsPresent(t *testing.T) {
	doc := fullDoc()
	out, err := JSON(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	islands, ok := parsed["islands"].([]any)
	if !ok {
		t.Fatalf("expected islands to be an array, got %T", parsed["islands"])
	}
	if len(islands) != 1 {
		t.Fatalf("expected 1 island, got %d", len(islands))
	}
	island := islands[0].(map[string]any)
	if island["source"] != "__NEXT_DATA__" {
		t.Errorf("expected island source '__NEXT_DATA__', got %v", island["source"])
	}
}

func TestJSON_InteractiveElements(t *testing.T) {
	doc := fullDoc()
	out, err := JSON(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	interactive, ok := parsed["interactive"].([]any)
	if !ok {
		t.Fatalf("expected interactive to be an array, got %T", parsed["interactive"])
	}
	if len(interactive) != 3 {
		t.Fatalf("expected 3 interactive elements, got %d", len(interactive))
	}
}

func TestText_BasicContent(t *testing.T) {
	doc := fullDoc()
	out := Text(doc)
	if !strings.Contains(out, "Welcome") {
		t.Error("expected 'Welcome' in text output")
	}
	if !strings.Contains(out, "This is a test page.") {
		t.Error("expected paragraph text in output")
	}
}

func TestText_ChildrenIndented(t *testing.T) {
	doc := fullDoc()
	out := Text(doc)
	if !strings.Contains(out, "  Click here") {
		t.Errorf("expected indented child text, got:\n%s", out)
	}
}

func TestText_EmptyDocument(t *testing.T) {
	doc := emptyDoc()
	out := Text(doc)
	if out != "" {
		t.Errorf("expected empty string for empty doc, got: %q", out)
	}
}

func TestText_NoTextElements(t *testing.T) {
	doc := &agentdom.Document{
		URL: "https://example.com",
		Content: []agentdom.Element{
			{
				Category: semantic.Category("image"),
				Tag:      "img",
			},
		},
		Interactive: []agentdom.Element{},
	}
	out := Text(doc)
	if out != "" {
		t.Errorf("expected empty string when elements have no text, got: %q", out)
	}
}

func TestText_ExcludesInteractive(t *testing.T) {
	doc := &agentdom.Document{
		URL:     "https://example.com",
		Content: []agentdom.Element{},
		Interactive: []agentdom.Element{
			{
				AgentID:  "@e1",
				Category: semantic.Category("button"),
				Text:     "Submit",
			},
		},
	}
	out := Text(doc)
	if strings.Contains(out, "Submit") {
		t.Error("Text renderer should not include interactive element text")
	}
}

func TestText_ExcludesIslands(t *testing.T) {
	doc := &agentdom.Document{
		URL:         "https://example.com",
		Content:     []agentdom.Element{},
		Interactive: []agentdom.Element{},
		Islands: []agentdom.DataIsland{
			{
				Source: "test",
				JSON:   json.RawMessage(`{"key":"value"}`),
			},
		},
	}
	out := Text(doc)
	if strings.Contains(out, "key") {
		t.Error("Text renderer should not include island data")
	}
}

func TestText_MultipleElements(t *testing.T) {
	doc := &agentdom.Document{
		URL: "https://example.com",
		Content: []agentdom.Element{
			{Category: semantic.Category("heading"), Text: "Title"},
			{Category: semantic.Category("paragraph"), Text: "Paragraph one."},
			{Category: semantic.Category("paragraph"), Text: "Paragraph two."},
		},
		Interactive: []agentdom.Element{},
	}
	out := Text(doc)
	if !strings.Contains(out, "Title") {
		t.Error("expected 'Title'")
	}
	if !strings.Contains(out, "Paragraph one.") {
		t.Error("expected 'Paragraph one.'")
	}
	if !strings.Contains(out, "Paragraph two.") {
		t.Error("expected 'Paragraph two.'")
	}
}

func TestText_ChildWithNoText(t *testing.T) {
	doc := &agentdom.Document{
		URL: "https://example.com",
		Content: []agentdom.Element{
			{
				Category: semantic.Category("paragraph"),
				Text:     "Parent",
				Children: []agentdom.Element{
					{Category: semantic.Category("image"), Tag: "img"},
				},
			},
		},
		Interactive: []agentdom.Element{},
	}
	out := Text(doc)
	if !strings.Contains(out, "Parent") {
		t.Error("expected parent text")
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d: %v", len(lines), lines)
	}
}

func TestGenerateFrontmatter_Basic(t *testing.T) {
	meta := agentdom.Meta{
		Title:     "Test Title",
		FinalURL:  "https://example.com",
		Lang:      "en",
		EstTokens: 100,
	}
	doc := &agentdom.Document{
		URL:     "https://example.com",
		Islands: []agentdom.DataIsland{},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.HasPrefix(fm, "---\n") {
		t.Errorf("expected frontmatter to start with ---, got: %s", fm)
	}
	if !strings.HasSuffix(fm, "---\n\n") {
		t.Errorf("expected frontmatter to end with ---\\n\\n, got: %q", fm)
	}
	if !strings.Contains(fm, "title: Test Title") {
		t.Errorf("expected title in frontmatter, got: %s", fm)
	}
	if !strings.Contains(fm, "url: https://example.com") {
		t.Errorf("expected url in frontmatter, got: %s", fm)
	}
	if !strings.Contains(fm, "lang: en") {
		t.Errorf("expected lang in frontmatter, got: %s", fm)
	}
	if !strings.Contains(fm, "tokens_est: 100") {
		t.Errorf("expected tokens_est in frontmatter, got: %s", fm)
	}
	if !strings.Contains(fm, "fetched_at:") {
		t.Errorf("expected fetched_at in frontmatter, got: %s", fm)
	}
	lines := strings.Split(fm, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "  fetched_at:") {
			ts := strings.TrimPrefix(strings.TrimSpace(line), "fetched_at: ")
			if _, err := time.Parse(time.RFC3339, ts); err != nil {
				t.Errorf("expected valid RFC3339 timestamp, got: %s (err: %v)", ts, err)
			}
		}
	}
}

func TestGenerateFrontmatter_FinalURLDifferent(t *testing.T) {
	meta := agentdom.Meta{
		Title:     "Page",
		FinalURL:  "https://example.com/redirected",
		Lang:      "en",
		EstTokens: 50,
	}
	doc := &agentdom.Document{
		URL:     "https://example.com/original",
		Islands: []agentdom.DataIsland{},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "url: https://example.com/original") {
		t.Errorf("expected original URL, got: %s", fm)
	}
	if !strings.Contains(fm, "final_url: https://example.com/redirected") {
		t.Errorf("expected final_url when different from URL, got: %s", fm)
	}
}

func TestGenerateFrontmatter_FinalURLSame(t *testing.T) {
	meta := agentdom.Meta{
		Title:     "Page",
		FinalURL:  "https://example.com",
		Lang:      "en",
		EstTokens: 50,
	}
	doc := &agentdom.Document{
		URL:     "https://example.com",
		Islands: []agentdom.DataIsland{},
	}

	fm := GenerateFrontmatter(meta, doc)

	if strings.Contains(fm, "final_url:") {
		t.Errorf("should not contain final_url when same as URL, got: %s", fm)
	}
}

func TestGenerateFrontmatter_NilIslands(t *testing.T) {
	meta := agentdom.Meta{Title: "Page", Lang: "en", EstTokens: 10}
	doc := &agentdom.Document{URL: "https://example.com"}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "title: Page") {
		t.Errorf("expected title, got: %s", fm)
	}
	if strings.Contains(fm, "schema_type:") {
		t.Errorf("should not contain schema_type with nil islands, got: %s", fm)
	}
}

func TestGenerateFrontmatter_EmptyIslands(t *testing.T) {
	meta := agentdom.Meta{Title: "Page", Lang: "en", EstTokens: 10}
	doc := &agentdom.Document{
		URL:     "https://example.com",
		Islands: []agentdom.DataIsland{},
	}

	fm := GenerateFrontmatter(meta, doc)

	if strings.Contains(fm, "schema_type:") {
		t.Errorf("should not contain schema_type with empty islands, got: %s", fm)
	}
}

func TestGenerateFrontmatter_WithLDJSONIsland(t *testing.T) {
	meta := agentdom.Meta{Title: "Page", Lang: "en", EstTokens: 10}
	doc := &agentdom.Document{
		URL: "https://example.com",
		Islands: []agentdom.DataIsland{
			{
				Source: "ld+json",
				JSON:   json.RawMessage(`{"@type": "Article", "headline": "Hello"}`),
			},
		},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "schema_type: Article") {
		t.Errorf("expected schema_type Article, got: %s", fm)
	}
}

func TestGenerateFrontmatter_IslandTypeField(t *testing.T) {
	meta := agentdom.Meta{Title: "Page", Lang: "en", EstTokens: 10}
	doc := &agentdom.Document{
		URL: "https://example.com",
		Islands: []agentdom.DataIsland{
			{
				Source: "ld+json",
				JSON:   json.RawMessage(`{"type": "Product", "name": "Widget"}`),
			},
		},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "schema_type: Product") {
		t.Errorf("expected schema_type Product, got: %s", fm)
	}
}

func TestGenerateFrontmatter_IslandGraphType(t *testing.T) {
	meta := agentdom.Meta{Title: "Page", Lang: "en", EstTokens: 10}
	doc := &agentdom.Document{
		URL: "https://example.com",
		Islands: []agentdom.DataIsland{
			{
				Source: "ld+json",
				JSON:   json.RawMessage(`{"@graph": [{"@type": "BreadcrumbList"}]}`),
			},
		},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "schema_type: BreadcrumbList") {
		t.Errorf("expected schema_type BreadcrumbList, got: %s", fm)
	}
}

func TestGenerateFrontmatter_NonLDJSONIsland(t *testing.T) {
	meta := agentdom.Meta{Title: "Page", Lang: "en", EstTokens: 10}
	doc := &agentdom.Document{
		URL: "https://example.com",
		Islands: []agentdom.DataIsland{
			{
				Source: "__NEXT_DATA__",
				JSON:   json.RawMessage(`{"@type": "Article"}`),
			},
		},
	}

	fm := GenerateFrontmatter(meta, doc)

	if strings.Contains(fm, "schema_type:") {
		t.Errorf("should not extract schema_type from non-ld+json island, got: %s", fm)
	}
}

func TestGenerateFrontmatter_LDJSONIslandWithAtType(t *testing.T) {
	meta := agentdom.Meta{Title: "Article Page", Lang: "en", EstTokens: 200}
	doc := &agentdom.Document{
		URL: "https://example.com/article",
		Islands: []agentdom.DataIsland{
			{
				Source: "ld+json",
				JSON:   json.RawMessage(`{"@type": "NewsArticle", "headline": "Breaking"}`),
			},
		},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "schema_type: NewsArticle") {
		t.Errorf("expected schema_type NewsArticle, got:\n%s", fm)
	}
	if !strings.Contains(fm, "title: Article Page") {
		t.Errorf("expected title in frontmatter, got:\n%s", fm)
	}
}

func TestGenerateFrontmatter_MultipleIslandsLDJSONWithGraphType(t *testing.T) {
	meta := agentdom.Meta{Title: "Multi Island Page", Lang: "ja", EstTokens: 300}
	doc := &agentdom.Document{
		URL: "https://example.com/multi",
		Islands: []agentdom.DataIsland{
			{Source: "__NEXT_DATA__", JSON: json.RawMessage(`{"page":"home"}`)},
			{Source: "ld+json", JSON: json.RawMessage(`{"@graph": [{"@type": "WebSite", "name": "ACME"}]}`)},
			{Source: "custom", JSON: json.RawMessage(`{"foo":"bar"}`)},
		},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "schema_type: WebSite") {
		t.Errorf("expected schema_type WebSite from @graph, got:\n%s", fm)
	}
	if !strings.Contains(fm, "lang: ja") {
		t.Errorf("expected lang ja, got:\n%s", fm)
	}
}

func TestGenerateFrontmatter_MultipleIslandsNoLDJSON(t *testing.T) {
	meta := agentdom.Meta{Title: "No Schema Page", Lang: "de", EstTokens: 50}
	doc := &agentdom.Document{
		URL: "https://example.com/noschema",
		Islands: []agentdom.DataIsland{
			{Source: "__NEXT_DATA__", JSON: json.RawMessage(`{"props":{}}`)},
			{Source: "__PRELOADED_STATE__", JSON: json.RawMessage(`{"state":true}`)},
		},
	}

	fm := GenerateFrontmatter(meta, doc)

	if strings.Contains(fm, "schema_type:") {
		t.Errorf("should not contain schema_type when no ld+json islands, got:\n%s", fm)
	}
	if !strings.Contains(fm, "title: No Schema Page") {
		t.Errorf("expected title, got:\n%s", fm)
	}
}

func TestGenerateFrontmatter_FinalURLDiffersFromDocURL(t *testing.T) {
	meta := agentdom.Meta{
		Title:     "Redirected",
		FinalURL:  "https://example.com/dest",
		Lang:      "en",
		EstTokens: 80,
	}
	doc := &agentdom.Document{
		URL:     "https://example.com/src",
		Islands: []agentdom.DataIsland{},
	}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.Contains(fm, "url: https://example.com/src") {
		t.Errorf("expected original URL, got:\n%s", fm)
	}
	if !strings.Contains(fm, "final_url: https://example.com/dest") {
		t.Errorf("expected final_url when meta.FinalURL differs from doc.URL, got:\n%s", fm)
	}
}

func TestGenerateFrontmatter_FinalURLMatchesDocURL(t *testing.T) {
	meta := agentdom.Meta{
		Title:     "Same URL Page",
		FinalURL:  "https://example.com/page",
		Lang:      "en",
		EstTokens: 60,
	}
	doc := &agentdom.Document{
		URL:     "https://example.com/page",
		Islands: []agentdom.DataIsland{},
	}

	fm := GenerateFrontmatter(meta, doc)

	if strings.Contains(fm, "final_url:") {
		t.Errorf("should omit final_url when meta.FinalURL equals doc.URL, got:\n%s", fm)
	}
}

func TestGenerateFrontmatter_NilIslandsNoPanic(t *testing.T) {
	meta := agentdom.Meta{Title: "Nil Island Test", Lang: "fr", EstTokens: 10}
	doc := &agentdom.Document{URL: "https://example.com/nil-islands"}

	fm := GenerateFrontmatter(meta, doc)

	if !strings.HasPrefix(fm, "---\n") {
		t.Errorf("expected frontmatter to start with ---, got:\n%s", fm)
	}
	if !strings.HasSuffix(fm, "---\n\n") {
		t.Errorf("expected frontmatter to end with ---\\n\\n, got: %q", fm)
	}
	if !strings.Contains(fm, "title: Nil Island Test") {
		t.Errorf("expected title, got:\n%s", fm)
	}
	if strings.Contains(fm, "schema_type:") {
		t.Errorf("should not contain schema_type with nil islands, got:\n%s", fm)
	}
	inner := strings.TrimPrefix(fm, "---\n")
	inner = strings.TrimSuffix(inner, "---\n\n")
	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(inner), &parsed); err != nil {
		t.Fatalf("frontmatter is not valid YAML: %v\n%s", err, fm)
	}
}

func TestGenerateFrontmatter_EmptyTitle(t *testing.T) {
	meta := agentdom.Meta{Title: "", Lang: "en", EstTokens: 10}
	doc := &agentdom.Document{
		URL:     "https://example.com",
		Islands: []agentdom.DataIsland{},
	}

	fm := GenerateFrontmatter(meta, doc)

	if strings.Contains(fm, "title:") {
		t.Errorf("empty title should be omitted (omitempty), got:\n%s", fm)
	}
	if !strings.HasPrefix(fm, "---\n") {
		t.Errorf("expected valid frontmatter start, got:\n%s", fm)
	}
	if !strings.Contains(fm, "url: https://example.com") {
		t.Errorf("expected url field, got:\n%s", fm)
	}
	if !strings.Contains(fm, "fetched_at:") {
		t.Errorf("expected fetched_at field, got:\n%s", fm)
	}
}

func TestExtractSchemaType_AtType(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{"@type": "Recipe", "name": "Cake"}`))
	if got != "Recipe" {
		t.Errorf("expected 'Recipe', got %q", got)
	}
}

func TestExtractSchemaType_TypeField(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{"type": "Product", "name": "Widget"}`))
	if got != "Product" {
		t.Errorf("expected 'Product', got %q", got)
	}
}

func TestExtractSchemaType_GraphType(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{"@graph": [{"@type": "BreadcrumbList"}, {"@type": "Organization"}]}`))
	if got != "BreadcrumbList" {
		t.Errorf("expected 'BreadcrumbList', got %q", got)
	}
}

func TestExtractSchemaType_GraphEmpty(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{"@graph": []}`))
	if got != "" {
		t.Errorf("expected empty for empty graph, got %q", got)
	}
}

func TestExtractSchemaType_GraphNonMapItem(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{"@graph": ["string", 42]}`))
	if got != "" {
		t.Errorf("expected empty for non-map graph items, got %q", got)
	}
}

func TestExtractSchemaType_GraphNoTypeInFirst(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{"@graph": [{"name": "no type"}]}`))
	if got != "" {
		t.Errorf("expected empty when first graph item has no @type, got %q", got)
	}
}

func TestExtractSchemaType_NoType(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{"name": "just a name", "value": 42}`))
	if got != "" {
		t.Errorf("expected empty for no type, got %q", got)
	}
}

func TestExtractSchemaType_InvalidJSON(t *testing.T) {
	got := extractSchemaType(json.RawMessage(`{invalid json`))
	if got != "" {
		t.Errorf("expected empty for invalid JSON, got %q", got)
	}
}

func TestQuerySchema_AtTypeMatch(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Recipe", "name": "Chocolate Cake", "recipeYield": "8"}`),
		},
	}

	got, err := QuerySchema(islands, "@Recipe:name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Chocolate Cake"` {
		t.Errorf("expected %q, got %q", `"Chocolate Cake"`, got)
	}
}

func TestQuerySchema_TypeFieldMatch(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"type": "Product", "name": "Widget"}`),
		},
	}

	got, err := QuerySchema(islands, "@Product:name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Widget"` {
		t.Errorf("expected %q, got %q", `"Widget"`, got)
	}
}

func TestQuerySchema_GraphMatch(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON: json.RawMessage(`{
				"@graph": [
					{"@type": "Organization", "name": "ACME Corp"},
					{"@type": "WebPage", "name": "Home"}
				]
			}`),
		},
	}

	data, err := unmarshalIsland(islands[0].JSON)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matchesType(data, "Organization") {
		t.Error("expected matchesType to find Organization in @graph")
	}
	if !matchesType(data, "WebPage") {
		t.Error("expected matchesType to find WebPage in @graph")
	}
	if matchesType(data, "Article") {
		t.Error("expected matchesType to return false for non-existent type")
	}
}

func TestQuerySchema_NestedField(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON: json.RawMessage(`{
				"@type": "Product",
				"name": "Widget",
				"aggregateRating": {"ratingValue": 4.5, "reviewCount": 100}
			}`),
		},
	}

	got, err := QuerySchema(islands, "@Product:aggregateRating.ratingValue")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "4.5" {
		t.Errorf("expected 4.5, got %q", got)
	}
}

func TestQuerySchema_CaseInsensitiveType(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "recipe", "name": "Cake"}`),
		},
	}

	got, err := QuerySchema(islands, "@Recipe:name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Cake"` {
		t.Errorf("expected %q, got %q", `"Cake"`, got)
	}
}

func TestQuerySchema_NoMatch(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Product", "name": "Widget"}`),
		},
	}

	_, err := QuerySchema(islands, "@Recipe:name")
	if err == nil {
		t.Fatal("expected error for non-matching type")
	}
	if !strings.Contains(err.Error(), "no JSON-LD") {
		t.Errorf("expected 'no JSON-LD' error, got: %v", err)
	}
}

func TestQuerySchema_EmptyIslands(t *testing.T) {
	_, err := QuerySchema([]agentdom.DataIsland{}, "@Recipe:name")
	if err == nil {
		t.Fatal("expected error for empty islands")
	}
}

func TestQuerySchema_NonLDIslandSkipped(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "__NEXT_DATA__", JSON: json.RawMessage(`{"@type": "Recipe"}`)},
	}

	_, err := QuerySchema(islands, "@Recipe:name")
	if err == nil {
		t.Fatal("expected error when no ld+json islands")
	}
}

func TestQuerySchema_InvalidJSONSkipped(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "ld+json", JSON: json.RawMessage(`{invalid`)},
	}

	_, err := QuerySchema(islands, "@Recipe:name")
	if err == nil {
		t.Fatal("expected error when all islands have invalid JSON")
	}
}

func TestQuerySchema_InvalidPathNoAt(t *testing.T) {
	_, err := QuerySchema([]agentdom.DataIsland{}, "Recipe:name")
	if err == nil {
		t.Fatal("expected error for path without @")
	}
	if !strings.Contains(err.Error(), "must start with @") {
		t.Errorf("expected 'must start with @' error, got: %v", err)
	}
}

func TestQuerySchema_InvalidPathNoColon(t *testing.T) {
	_, err := QuerySchema([]agentdom.DataIsland{}, "@Recipe")
	if err == nil {
		t.Fatal("expected error for path without colon")
	}
	if !strings.Contains(err.Error(), "must have format @Type:path") {
		t.Errorf("expected format error, got: %v", err)
	}
}

func TestQuerySchema_MissingField(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Recipe", "name": "Cake"}`),
		},
	}

	_, err := QuerySchema(islands, "@Recipe:nonexistent")
	if err == nil {
		t.Fatal("expected error for missing field")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestQuerySchema_ArrayTraversalError(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Recipe", "steps": ["mix", "bake"]}`),
		},
	}

	_, err := QuerySchema(islands, "@Recipe:steps.0")
	if err == nil {
		t.Fatal("expected error for array traversal")
	}
	if !strings.Contains(err.Error(), "cannot traverse into array") {
		t.Errorf("expected 'cannot traverse into array' error, got: %v", err)
	}
}

func TestQuerySchema_NilTypeField(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": null, "name": "Test"}`),
		},
	}

	_, err := QuerySchema(islands, "@Recipe:name")
	if err == nil {
		t.Fatal("expected error when @type is null")
	}
}

func TestQuerySchema_PlainPathType(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Recipe", "name": "Cake"}`),
		},
	}

	got, err := QuerySchema(islands, "@type")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Recipe"` {
		t.Errorf("expected %q, got %q", `"Recipe"`, got)
	}
}

func TestQuerySchema_PlainPathName(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Recipe", "name": "Chocolate Cake"}`),
		},
	}

	got, err := QuerySchema(islands, "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Chocolate Cake"` {
		t.Errorf("expected %q, got %q", `"Chocolate Cake"`, got)
	}
}

func TestQuerySchema_PlainPathHeadline(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Article", "headline": "Breaking News"}`),
		},
	}

	got, err := QuerySchema(islands, "headline")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Breaking News"` {
		t.Errorf("expected %q, got %q", `"Breaking News"`, got)
	}
}

func TestQuerySchema_PlainPathNested(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON: json.RawMessage(`{
				"@type": "Product",
				"name": "Widget",
				"aggregateRating": {"ratingValue": 4.5, "reviewCount": 100}
			}`),
		},
	}

	got, err := QuerySchema(islands, "aggregateRating.ratingValue")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "4.5" {
		t.Errorf("expected 4.5, got %q", got)
	}
}

func TestQuerySchema_TypedSelectorStillWorks(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON:   json.RawMessage(`{"@type": "Article", "headline": "Test Headline"}`),
		},
	}

	got, err := QuerySchema(islands, "@Article:headline")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Test Headline"` {
		t.Errorf("expected %q, got %q", `"Test Headline"`, got)
	}
}

func TestQuerySchema_GraphTypedSelection(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON: json.RawMessage(`{
				"@graph": [
					{"@type": "Organization", "name": "ACME Corp"},
					{"@type": "WebPage", "name": "Home", "headline": "Welcome"}
				]
			}`),
		},
	}

	got, err := QuerySchema(islands, "@WebPage:headline")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Welcome"` {
		t.Errorf("expected %q, got %q", `"Welcome"`, got)
	}
}

func TestQuerySchema_GraphPlainPath(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON: json.RawMessage(`{
				"@graph": [
					{"@type": "Organization", "name": "ACME Corp"},
					{"@type": "WebPage", "name": "Home"}
				]
			}`),
		},
	}

	got, err := QuerySchema(islands, "name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "ACME Corp") || !strings.Contains(got, "Home") {
		t.Errorf("expected both ACME Corp and Home, got: %q", got)
	}
}

func TestQuerySchema_PlainPathTypeFromGraph(t *testing.T) {
	islands := []agentdom.DataIsland{
		{
			Source: "ld+json",
			JSON: json.RawMessage(`{
				"@graph": [
					{"@type": "Organization", "name": "ACME"},
					{"@type": "WebPage", "name": "Home"}
				]
			}`),
		},
	}

	got, err := QuerySchema(islands, "@type")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Organization") || !strings.Contains(got, "WebPage") {
		t.Errorf("expected both Organization and WebPage, got: %q", got)
	}
}

func TestQuerySchema_InvalidSyntaxNoPanic(t *testing.T) {
	badPaths := []string{
		"@:",
		"@:",
		":name",
		"@Recipe:",
	}
	for _, p := range badPaths {
		_, err := QuerySchema([]agentdom.DataIsland{}, p)
		if err == nil {
			t.Errorf("expected error for path %q", p)
		}
	}
}

func TestTraversePath_DeepNesting(t *testing.T) {
	data := json.RawMessage(`{"a": {"b": {"c": "deep"}}}`)
	got, err := traversePath(data, "a.b.c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"deep"` {
		t.Errorf("expected %q, got %q", `"deep"`, got)
	}
}

func TestTraversePath_NumericValue(t *testing.T) {
	data := json.RawMessage(`{"count": 42}`)
	got, err := traversePath(data, "count")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "42" {
		t.Errorf("expected 42, got %q", got)
	}
}

func TestTraversePath_BoolValue(t *testing.T) {
	data := json.RawMessage(`{"active": true}`)
	got, err := traversePath(data, "active")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "true" {
		t.Errorf("expected true, got %q", got)
	}
}

func TestTraversePath_NullValue(t *testing.T) {
	data := json.RawMessage(`{"val": null}`)
	got, err := traversePath(data, "val")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "null" {
		t.Errorf("expected null, got %q", got)
	}
}

func TestTraversePath_InvalidJSON(t *testing.T) {
	_, err := traversePath(json.RawMessage(`{bad`), "field")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

// --- FormatMarkdownWithMeta tests ---

func TestFormatMarkdownWithMeta_Basic(t *testing.T) {
	meta := agentdom.Meta{
		Title:      "Test Page",
		StatusCode: 200,
		EstTokens:  42,
		FinalURL:   "https://example.com",
	}
	got := FormatMarkdownWithMeta(meta, "# Hello")

	if !strings.Contains(got, "> **Test Page**") {
		t.Errorf("expected title in metadata, got: %s", got)
	}
	if !strings.Contains(got, "~42 tokens") {
		t.Errorf("expected token count, got: %s", got)
	}
	if !strings.Contains(got, "> https://example.com") {
		t.Errorf("expected final URL, got: %s", got)
	}
	if !strings.Contains(got, "# Hello") {
		t.Errorf("expected output content, got: %s", got)
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("should not contain truncated warning, got: %s", got)
	}
}

func TestFormatMarkdownWithMeta_Truncated(t *testing.T) {
	meta := agentdom.Meta{
		Title:      "Page",
		StatusCode: 200,
		EstTokens:  100,
		FinalURL:   "https://example.com",
		Truncated:  true,
	}
	got := FormatMarkdownWithMeta(meta, "content")

	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncated warning, got: %s", got)
	}
}

func TestFormatMarkdownWithMeta_LikelyClientRendered(t *testing.T) {
	meta := agentdom.Meta{
		Title:                "SPA Shell",
		StatusCode:           200,
		EstTokens:            5,
		FinalURL:             "https://example.com/app",
		LikelyClientRendered: true,
	}
	got := FormatMarkdownWithMeta(meta, "<nav>links only</nav>")

	if !strings.Contains(got, "⚠ likely client-rendered") {
		t.Errorf("expected likely client-rendered warning, got: %s", got)
	}
	if !strings.Contains(got, "> **SPA Shell**") {
		t.Errorf("expected title in metadata, got: %s", got)
	}
}

// --- Issue #188: coverage regression target tests ---

// schema.go:22 — SchemaMiss.Error() fallback when Msg is empty
func TestSchemaMiss_ErrorEmptyMsg(t *testing.T) {
	e := &SchemaMiss{Path: "@Recipe:name"}
	got := e.Error()
	if !strings.Contains(got, `"@Recipe:name"`) {
		t.Errorf("expected quoted path in error, got: %q", got)
	}
	if !strings.Contains(got, "no match") {
		t.Errorf("expected 'no match' in error, got: %q", got)
	}
}

// schema.go:36-38 — QuerySchema rejects empty path
func TestQuerySchema_EmptyPath(t *testing.T) {
	_, err := QuerySchema([]agentdom.DataIsland{}, "")
	if err == nil {
		t.Fatal("expected error for empty schema path")
	}
	if !strings.Contains(err.Error(), "must not be empty") {
		t.Errorf("expected 'must not be empty', got: %v", err)
	}
}

// schema.go:106-108 — queryTypeField: "type" field (not "@type")
func TestQueryTypeField_TypeField(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "ld+json", JSON: json.RawMessage(`{"type": "Product", "name": "Widget"}`)},
	}
	got, err := queryTypeField(islands)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `"Product"` {
		t.Errorf("expected %q, got %q", `"Product"`, got)
	}
}

// schema.go:122-124 — queryTypeField: no types found
func TestQueryTypeField_NoTypesFound(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "ld+json", JSON: json.RawMessage(`{"name": "Widget"}`)},
	}
	_, err := queryTypeField(islands)
	if err == nil {
		t.Fatal("expected error for no types found")
	}
	var miss *SchemaMiss
	if !errors.As(err, &miss) {
		t.Errorf("expected SchemaMiss, got: %v", err)
	}
	if !strings.Contains(err.Error(), "@type not found") {
		t.Errorf("expected '@type not found', got: %v", err)
	}
}

// schema.go:94-95 — queryTypeField skips non-ld+json islands
func TestQueryTypeField_SkipsNonLDJSON(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "__NEXT_DATA__", JSON: json.RawMessage(`{"@type": "Recipe"}`)},
	}
	_, err := queryTypeField(islands)
	if err == nil {
		t.Fatal("expected error when all islands are non-ld+json")
	}
}

// schema.go: unmarshal error in queryTypeField (island.Source == ld+json but bad JSON)
func TestQueryTypeField_InvalidJSON(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "ld+json", JSON: json.RawMessage(`{not json`)},
	}
	_, err := queryTypeField(islands)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// schema.go:142-143 — queryPlainPath skips non-ld+json islands
func TestQueryPlainPath_SkipsNonLDJSON(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "__NEXT_DATA__", JSON: json.RawMessage(`{"name": "Test"}`)},
	}
	_, err := queryPlainPath(islands, "name")
	if err == nil {
		t.Fatal("expected error when all islands are non-ld+json")
	}
}

// schema.go:146 — queryPlainPath: unmarshal error → continue
func TestQueryPlainPath_InvalidJSON(t *testing.T) {
	islands := []agentdom.DataIsland{
		{Source: "ld+json", JSON: json.RawMessage(`{not json`)},
	}
	_, err := queryPlainPath(islands, "name")
	if err == nil {
		t.Fatal("expected error for invalid JSON in ld+json island")
	}
}

// frontmatter.go:23-25 — GenerateFrontmatter with nil doc returns ""
func TestGenerateFrontmatter_NilDoc(t *testing.T) {
	meta := agentdom.Meta{Title: "Test", Lang: "en", EstTokens: 10}
	got := GenerateFrontmatter(meta, nil)
	if got != "" {
		t.Errorf("expected empty string for nil doc, got: %q", got)
	}
}

// traverseFieldMap: "cannot traverse into" default branch (non-map, non-array)
func TestTraverseFieldMap_CannotTraverseIntoPrimitive(t *testing.T) {
	data := map[string]interface{}{"val": "string_value"}
	_, err := traverseFieldMap(data, "val.subfield")
	if err == nil {
		t.Fatal("expected error when traversing into a string")
	}
	if !strings.Contains(err.Error(), "cannot traverse into") {
		t.Errorf("expected 'cannot traverse into', got: %v", err)
	}
}

// findTypedNode: returns nil for unmarshalable JSON
func TestFindTypedNode_InvalidJSON(t *testing.T) {
	got := findTypedNode(json.RawMessage(`{bad`), "Recipe")
	if got != nil {
		t.Errorf("expected nil for invalid JSON, got: %v", got)
	}
}

func TestFormatMarkdownWithMeta_ContentRich_NoClientRenderedWarning(t *testing.T) {
	meta := agentdom.Meta{
		Title:                "Article",
		StatusCode:           200,
		EstTokens:            500,
		FinalURL:             "https://example.com/article",
		LikelyClientRendered: false,
	}
	got := FormatMarkdownWithMeta(meta, "# Real content here")

	if strings.Contains(got, "likely client-rendered") {
		t.Errorf("should not contain likely client-rendered warning for content-rich page, got: %s", got)
	}
}
