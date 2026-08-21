package relevance

import (
	"strings"
	"testing"
)

func TestTokenize_SimpleWords(t *testing.T) {
	tokens := Tokenize("Hello World")
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d: %v", len(tokens), tokens)
	}
	if tokens[0] != "hello" || tokens[1] != "world" {
		t.Errorf("expected [hello world], got %v", tokens)
	}
}

func TestTokenize_Duplicates(t *testing.T) {
	tokens := Tokenize("price pricing price")
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d: %v", len(tokens), tokens)
	}
}

func TestTokenize_Empty(t *testing.T) {
	tokens := Tokenize("")
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens, got %d", len(tokens))
	}
}

func TestTokenize_Punctuation(t *testing.T) {
	tokens := Tokenize("hello, world! how's it?")
	if len(tokens) < 4 {
		t.Errorf("expected at least 4 tokens, got %d: %v", len(tokens), tokens)
	}
}

func TestBM25_ExactMatch(t *testing.T) {
	docs := []string{
		"pricing information for enterprise plans",
		"contact us for support",
		"enterprise pricing tiers and billing",
	}
	query := "pricing"
	scores := BM25(query, docs)
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	// Docs containing "pricing" should score higher
	if scores[0] <= scores[1] {
		t.Errorf("doc 0 (has pricing) should score higher than doc 1 (no pricing)")
	}
	if scores[2] <= scores[1] {
		t.Errorf("doc 2 (has pricing) should score higher than doc 1 (no pricing)")
	}
}

func TestBM25_MultipleTerms(t *testing.T) {
	docs := []string{
		"enterprise pricing plans",
		"enterprise features overview",
		"pricing and billing",
	}
	query := "enterprise pricing"
	scores := BM25(query, docs)
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	// Doc 0 has both terms, should rank highest
	if scores[0] <= scores[1] {
		t.Errorf("doc 0 (both terms) should score higher than doc 1 (one term)")
	}
	if scores[0] <= scores[2] {
		t.Errorf("doc 0 (both terms) should score higher than doc 2 (one term)")
	}
}

func TestBM25_EmptyQuery(t *testing.T) {
	docs := []string{"hello", "world"}
	scores := BM25("", docs)
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}
	for _, s := range scores {
		if s != 0 {
			t.Errorf("expected 0 score for empty query, got %f", s)
		}
	}
}

func TestRankSections_BasicOrdering(t *testing.T) {
	sections := []Section{
		{Heading: "Introduction", Text: "Welcome to our platform"},
		{Heading: "Pricing", Text: "Our pricing starts at $99 per month"},
		{Heading: "Contact", Text: "Email us at support@example.com"},
	}
	ranked := RankSections("pricing", sections, 0)
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked sections, got %d", len(ranked))
	}
	// "Pricing" section should rank first
	if ranked[0].Heading != "Pricing" {
		t.Errorf("expected 'Pricing' to rank first, got %q", ranked[0].Heading)
	}
}

func TestRankSections_TopK(t *testing.T) {
	sections := []Section{
		{Heading: "Introduction", Text: "Welcome to our platform"},
		{Heading: "Pricing", Text: "Our pricing starts at $99 per month"},
		{Heading: "Enterprise Pricing", Text: "Enterprise pricing tiers and custom plans"},
		{Heading: "Contact", Text: "Email us at support@example.com"},
		{Heading: "FAQ", Text: "Frequently asked questions about pricing"},
	}
	ranked := RankSections("pricing", sections, 2)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked sections (top-k=2), got %d", len(ranked))
	}
	// Both top sections should contain "pricing"
	for _, s := range ranked {
		if !strings.Contains(strings.ToLower(s.Text), "pricing") && !strings.Contains(strings.ToLower(s.Heading), "pricing") {
			t.Errorf("expected top-k sections to contain 'pricing', got %q: %s", s.Heading, s.Text)
		}
	}
}

func TestRankSections_TopKZero(t *testing.T) {
	sections := []Section{
		{Heading: "Pricing", Text: "Our pricing starts at $99 per month"},
	}
	ranked := RankSections("pricing", sections, 0)
	if len(ranked) != 1 {
		t.Fatalf("expected all sections when top-k=0, got %d", len(ranked))
	}
}

func TestRankSections_EmptySections(t *testing.T) {
	ranked := RankSections("pricing", []Section{}, 0)
	if len(ranked) != 0 {
		t.Errorf("expected 0 ranked sections, got %d", len(ranked))
	}
}

func TestSplitMarkdownSections_Basic(t *testing.T) {
	md := `# Welcome

This is the introduction.

## Pricing

Our pricing starts at $99 per month.

## Contact

Email us at support@example.com.`
	sections := SplitMarkdownSections(md)
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}
	if sections[0].Heading != "Welcome" {
		t.Errorf("expected first heading 'Welcome', got %q", sections[0].Heading)
	}
	if sections[1].Heading != "Pricing" {
		t.Errorf("expected second heading 'Pricing', got %q", sections[1].Heading)
	}
	if sections[2].Heading != "Contact" {
		t.Errorf("expected third heading 'Contact', got %q", sections[2].Heading)
	}
}

func TestSplitMarkdownSections_NoHeadings(t *testing.T) {
	md := "Just some text without headings.\nMore text here."
	sections := SplitMarkdownSections(md)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section (no headings), got %d", len(sections))
	}
	if sections[0].Heading != "" {
		t.Errorf("expected empty heading for implicit section, got %q", sections[0].Heading)
	}
}

func TestSplitMarkdownSections_Empty(t *testing.T) {
	sections := SplitMarkdownSections("")
	if len(sections) != 1 {
		t.Fatalf("expected 1 section for empty input, got %d", len(sections))
	}
}

func TestReassembleMarkdown_WithOmissions(t *testing.T) {
	sections := []Section{
		{Heading: "Pricing", Text: "Our pricing starts at $99."},
		{Heading: "FAQ", Text: "Frequently asked questions."},
	}
	result := ReassembleMarkdown(sections, 5, 2)
	if !strings.Contains(result, "Pricing") {
		t.Error("expected 'Pricing' in reassembled output")
	}
	if !strings.Contains(result, "FAQ") {
		t.Error("expected 'FAQ' in reassembled output")
	}
	if !strings.Contains(result, "<!--") {
		t.Error("expected omission marker in reassembled output")
	}
}

func TestReassembleMarkdown_NoOmissions(t *testing.T) {
	sections := []Section{
		{Heading: "Pricing", Text: "Our pricing starts at $99."},
	}
	result := ReassembleMarkdown(sections, 1, 1)
	if strings.Contains(result, "<!--") {
		t.Error("no omission markers expected when all sections are included")
	}
}

func TestFilterJSONContent_BasicOrdering(t *testing.T) {
	type contentItem struct {
		Heading string `json:"heading"`
		Text    string `json:"text"`
	}
	items := []contentItem{
		{Heading: "Introduction", Text: "Welcome to our platform"},
		{Heading: "Pricing", Text: "Our pricing starts at $99 per month"},
		{Heading: "Contact", Text: "Email us at support@example.com"},
	}
	ranked := FilterJSONContent("pricing", items, func(c contentItem) string {
		return c.Heading + " " + c.Text
	}, 0)
	if len(ranked) != 3 {
		t.Fatalf("expected 3 items, got %d", len(ranked))
	}
	for i, item := range ranked {
		if item.Heading != items[i].Heading {
			t.Errorf("item %d: expected heading %q, got %q", i, items[i].Heading, item.Heading)
		}
	}
}

func TestFilterJSONContent_TopK(t *testing.T) {
	type contentItem struct {
		Heading string `json:"heading"`
		Text    string `json:"text"`
	}
	items := []contentItem{
		{Heading: "Introduction", Text: "Welcome"},
		{Heading: "Pricing", Text: "Our pricing starts at $99"},
		{Heading: "Enterprise", Text: "Enterprise pricing tiers"},
		{Heading: "FAQ", Text: "Questions about pricing"},
	}
	ranked := FilterJSONContent("pricing", items, func(c contentItem) string {
		return c.Heading + " " + c.Text
	}, 2)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 items (top-k=2), got %d", len(ranked))
	}
}

func TestFilterJSONContent_EmptyQuery(t *testing.T) {
	type item struct{ Text string }
	items := []item{{Text: "hello"}, {Text: "world"}}
	ranked := FilterJSONContent("", items, func(i item) string { return i.Text }, 0)
	if len(ranked) != 2 {
		t.Errorf("expected all items for empty query, got %d", len(ranked))
	}
}
