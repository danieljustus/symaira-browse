package agentdom_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
)

func parseHTML(t *testing.T, src string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestBuilderAssignsAgentIDs(t *testing.T) {
	src := `<html><body>
<form action="/submit">
  <input type="text" name="q"/>
  <button type="submit">Go</button>
</form>
</body></html>`
	doc := parseHTML(t, src)
	agDoc := &agentdom.Document{}
	builder := agentdom.NewBuilder(10000)
	builder.Build(doc, agDoc)

	if len(agDoc.Interactive) == 0 {
		t.Fatal("expected interactive elements")
	}
	for _, el := range agDoc.Interactive {
		if el.AgentID == "" {
			t.Errorf("interactive element %q has no AgentID", el.Category)
		}
		if !strings.HasPrefix(el.AgentID, "@e") {
			t.Errorf("expected AgentID prefix @e, got %q", el.AgentID)
		}
	}
}

func TestBuilderAgentIDsAreUnique(t *testing.T) {
	src := `<html><body>
<input type="text" name="a"/>
<input type="text" name="b"/>
<button>X</button>
</body></html>`
	doc := parseHTML(t, src)
	agDoc := &agentdom.Document{}
	agentdom.NewBuilder(10000).Build(doc, agDoc)

	seen := map[string]bool{}
	for _, el := range agDoc.Interactive {
		if seen[el.AgentID] {
			t.Errorf("duplicate AgentID %q", el.AgentID)
		}
		seen[el.AgentID] = true
	}
}

func TestBuilderTruncatesAtRuneBoundary(t *testing.T) {
	// Japanese characters — 3 bytes each in UTF-8
	content := strings.Repeat("あ", 100)
	src := "<html><body><p>" + content + "</p></body></html>"
	doc := parseHTML(t, src)

	maxChars := 30
	agDoc := &agentdom.Document{}
	agentdom.NewBuilder(maxChars).Build(doc, agDoc)

	total := 0
	for _, el := range agDoc.Content {
		total += utf8.RuneCountInString(el.Text)
	}

	// Should not exceed maxChars (plus 1 for the ellipsis)
	if total > maxChars+1 {
		t.Errorf("expected at most %d runes, got %d", maxChars+1, total)
	}
	// Verify the result is valid UTF-8
	for _, el := range agDoc.Content {
		if !utf8.ValidString(el.Text) {
			t.Errorf("element text is invalid UTF-8: %q", el.Text)
		}
	}
}

func TestBuilderTruncatedFlagSet(t *testing.T) {
	content := strings.Repeat("x", 1000)
	src := "<html><body><p>" + content + "</p></body></html>"
	doc := parseHTML(t, src)

	agDoc := &agentdom.Document{}
	agentdom.NewBuilder(50).Build(doc, agDoc)

	// At least one element should have truncated text (ending with ellipsis)
	hasTruncation := false
	for _, el := range agDoc.Content {
		if strings.HasSuffix(el.Text, "…") {
			hasTruncation = true
			break
		}
	}
	if !hasTruncation {
		t.Error("expected truncated text to end with ellipsis")
	}
}

func TestBuilderCharacterBudgetBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		maxChars int
		src      string
		wantText string
	}{
		{
			name:     "unlimited budget",
			maxChars: 0,
			src:      `<html><body><p>first</p><p>second</p></body></html>`,
			wantText: "first",
		},
		{
			name:     "truncated budget stops later siblings",
			maxChars: 4,
			src:      `<html><body><p>first</p><p>second</p></body></html>`,
			wantText: "firs…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := &agentdom.Document{}
			agentdom.NewBuilder(tt.maxChars).Build(parseHTML(t, tt.src), doc)

			if len(doc.Content) == 0 || doc.Content[0].Text != tt.wantText {
				t.Fatalf("expected first element text %q, got %+v", tt.wantText, doc.Content)
			}
			if tt.maxChars == 4 && len(doc.Content) != 1 {
				t.Fatalf("expected later sibling to be skipped after budget exhaustion, got %d elements", len(doc.Content))
			}
		})
	}
}

func TestBuilderCompressesWhitespaceAndPreservesNestedInteractiveContent(t *testing.T) {
	src := `<html><body><main>
  <p>  spaced   text
 across lines </p>
  <a href="/next">  Read	more </a>
</main></body></html>`
	doc := &agentdom.Document{}
	agentdom.NewBuilder(1000).Build(parseHTML(t, src), doc)

	if len(doc.Interactive) != 1 {
		t.Fatalf("expected one interactive element, got %d", len(doc.Interactive))
	}
	if got := doc.Interactive[0].Text; got != "Read more" {
		t.Errorf("interactive text = %q, want %q", got, "Read more")
	}
	if len(doc.Content) < 2 {
		t.Fatalf("expected text and link content, got %d elements", len(doc.Content))
	}
}

func TestBuilderLimitsTextExtractionPerElement(t *testing.T) {
	content := strings.Repeat("x", 2500)
	doc := &agentdom.Document{}
	agentdom.NewBuilder(3000).Build(parseHTML(t, "<html><body><p>"+content+"</p></body></html>"), doc)

	if len(doc.Content) != 1 {
		t.Fatalf("expected one content element, got %d", len(doc.Content))
	}
	if got := utf8.RuneCountInString(doc.Content[0].Text); got != 2000 {
		t.Errorf("extracted %d runes, want per-element limit of 2000", got)
	}
}
