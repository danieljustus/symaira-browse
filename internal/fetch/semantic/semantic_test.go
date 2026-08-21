package semantic_test

import (
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
)

func parseHTML(t *testing.T, src string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// findFirst returns the first node matching the given tag in a pre-order walk.
func findFirst(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, tag) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirst(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func TestClassifyNodeButton(t *testing.T) {
	doc := parseHTML(t, `<button type="submit">Click</button>`)
	btn := findFirst(doc, "button")
	if btn == nil {
		t.Fatal("button not found")
	}
	got := semantic.ClassifyNode(btn)
	if got != semantic.CatButton {
		t.Errorf("expected CatButton, got %q", got)
	}
}

func TestClassifyNodeInputSubmitIsButton(t *testing.T) {
	doc := parseHTML(t, `<input type="submit" value="Go"/>`)
	inp := findFirst(doc, "input")
	got := semantic.ClassifyNode(inp)
	if got != semantic.CatButton {
		t.Errorf("expected CatButton for input[type=submit], got %q", got)
	}
}

func TestClassifyNodeInputText(t *testing.T) {
	doc := parseHTML(t, `<input type="text" name="q"/>`)
	inp := findFirst(doc, "input")
	got := semantic.ClassifyNode(inp)
	if got != semantic.CatInput {
		t.Errorf("expected CatInput, got %q", got)
	}
}

func TestClassifyNodeLink(t *testing.T) {
	doc := parseHTML(t, `<a href="/foo">Link</a>`)
	a := findFirst(doc, "a")
	got := semantic.ClassifyNode(a)
	if got != semantic.CatLink {
		t.Errorf("expected CatLink, got %q", got)
	}
}

func TestClassifyNodeAnchorNoHrefIsText(t *testing.T) {
	doc := parseHTML(t, `<a name="anchor">Anchor</a>`)
	a := findFirst(doc, "a")
	got := semantic.ClassifyNode(a)
	if got != semantic.CatText {
		t.Errorf("expected CatText for anchor without href, got %q", got)
	}
}

func TestClassifyNodeForm(t *testing.T) {
	doc := parseHTML(t, `<form action="/submit"></form>`)
	f := findFirst(doc, "form")
	got := semantic.ClassifyNode(f)
	if got != semantic.CatForm {
		t.Errorf("expected CatForm, got %q", got)
	}
}

func TestIsInteractive(t *testing.T) {
	interactive := []semantic.Category{
		semantic.CatButton, semantic.CatLink, semantic.CatInput,
		semantic.CatSelect, semantic.CatTextarea, semantic.CatForm,
	}
	for _, cat := range interactive {
		if !semantic.IsInteractive(cat) {
			t.Errorf("expected IsInteractive(%q)=true", cat)
		}
	}
	nonInteractive := []semantic.Category{semantic.CatText, semantic.CatImage, ""}
	for _, cat := range nonInteractive {
		if semantic.IsInteractive(cat) {
			t.Errorf("expected IsInteractive(%q)=false", cat)
		}
	}
}

func TestExtractIslandsNextData(t *testing.T) {
	src := `<html><head></head><body>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"title":"Hi"}},"page":"/"}</script>
</body></html>`
	doc := parseHTML(t, src)
	islands := semantic.ExtractIslands(doc, 100000)
	if len(islands) == 0 {
		t.Fatal("expected at least one island")
	}
	found := false
	for _, island := range islands {
		if island.Source == "__NEXT_DATA__" {
			found = true
			var obj map[string]interface{}
			if err := json.Unmarshal(island.JSON, &obj); err != nil {
				t.Fatalf("island JSON invalid: %v", err)
			}
		}
	}
	if !found {
		t.Error("expected __NEXT_DATA__ island")
	}
}

func TestExtractIslandsMalformedJSON(t *testing.T) {
	src := `<html><body>
<script id="__NEXT_DATA__" type="application/json">{broken json</script>
</body></html>`
	doc := parseHTML(t, src)
	islands := semantic.ExtractIslands(doc, 100000)
	// Malformed JSON should be skipped silently
	for _, island := range islands {
		if island.Source == "__NEXT_DATA__" {
			t.Error("expected malformed __NEXT_DATA__ to be skipped")
		}
	}
}

func TestBestBlockPicksArticle(t *testing.T) {
	src := `<html><body>
<nav><a href="/a">A</a><a href="/b">B</a><a href="/c">C</a></nav>
<article><p>This is the real content with lots of text to ensure it scores well above the threshold.</p>
<p>More content here to push the character count up sufficiently for the test.</p></article>
<footer><a href="/privacy">Privacy</a><a href="/terms">Terms</a></footer>
</body></html>`
	doc := parseHTML(t, src)
	best := semantic.BestBlock(doc, 50)
	if best == nil {
		t.Fatal("BestBlock returned nil")
	}
	// Best block should be the article, not nav/footer
	if best.Type == html.ElementNode && best.Data == "nav" {
		t.Error("BestBlock picked nav instead of article")
	}
}

func TestBestBlockFallbackOnTinyPage(t *testing.T) {
	src := `<html><body><p>Hi.</p></body></html>`
	doc := parseHTML(t, src)
	best := semantic.BestBlock(doc, 500) // threshold way above content
	if best == nil {
		t.Fatal("BestBlock returned nil even for tiny page")
	}
}

func TestClassifyNodeNonElement(t *testing.T) {
	node := &html.Node{Type: html.TextNode, Data: "hello"}
	got := semantic.ClassifyNode(node)
	if got != "" {
		t.Errorf("expected empty for text node, got %q", got)
	}
}

func TestClassifyNodeComment(t *testing.T) {
	node := &html.Node{Type: html.CommentNode, Data: "comment"}
	got := semantic.ClassifyNode(node)
	if got != "" {
		t.Errorf("expected empty for comment node, got %q", got)
	}
}

func TestClassifyNodeUnknownTag(t *testing.T) {
	doc := parseHTML(t, `<custom-element foo="bar">content</custom-element>`)
	node := findFirst(doc, "custom-element")
	got := semantic.ClassifyNode(node)
	if got != "" {
		t.Errorf("expected empty for unknown tag, got %q", got)
	}
}

func TestClassifyNodeInputButton(t *testing.T) {
	doc := parseHTML(t, `<input type="button" value="Click"/>`)
	node := findFirst(doc, "input")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatButton {
		t.Errorf("expected CatButton for input[type=button], got %q", got)
	}
}

func TestClassifyNodeInputReset(t *testing.T) {
	doc := parseHTML(t, `<input type="reset" value="Reset"/>`)
	node := findFirst(doc, "input")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatButton {
		t.Errorf("expected CatButton for input[type=reset], got %q", got)
	}
}

func TestClassifyNodeInputImage(t *testing.T) {
	doc := parseHTML(t, `<input type="image" src="submit.png"/>`)
	node := findFirst(doc, "input")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatImage {
		t.Errorf("expected CatImage for input[type=image], got %q", got)
	}
}

func TestClassifyNodeInputDefault(t *testing.T) {
	doc := parseHTML(t, `<input type="email" name="email"/>`)
	node := findFirst(doc, "input")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatInput {
		t.Errorf("expected CatInput for input[type=email], got %q", got)
	}
}

func TestClassifyNodeSelect(t *testing.T) {
	doc := parseHTML(t, `<select><option>A</option></select>`)
	node := findFirst(doc, "select")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatSelect {
		t.Errorf("expected CatSelect, got %q", got)
	}
}

func TestClassifyNodeTextarea(t *testing.T) {
	doc := parseHTML(t, `<textarea></textarea>`)
	node := findFirst(doc, "textarea")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatTextarea {
		t.Errorf("expected CatTextarea, got %q", got)
	}
}

func TestClassifyNodeImgWithAlt(t *testing.T) {
	doc := parseHTML(t, `<img src="photo.jpg" alt="A photo"/>`)
	node := findFirst(doc, "img")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatImage {
		t.Errorf("expected CatImage for img with alt, got %q", got)
	}
}

func TestClassifyNodeImgWithoutAlt(t *testing.T) {
	doc := parseHTML(t, `<img src="photo.jpg"/>`)
	node := findFirst(doc, "img")
	got := semantic.ClassifyNode(node)
	if got != "" {
		t.Errorf("expected empty for img without alt, got %q", got)
	}
}

func TestClassifyNodeRoleButton(t *testing.T) {
	doc := parseHTML(t, `<div role="button">Click</div>`)
	node := findFirst(doc, "div")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatButton {
		t.Errorf("expected CatButton for role=button, got %q", got)
	}
}

func TestClassifyNodeRoleLink(t *testing.T) {
	doc := parseHTML(t, `<span role="link">Link</span>`)
	node := findFirst(doc, "span")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatLink {
		t.Errorf("expected CatLink for role=link, got %q", got)
	}
}

func TestClassifyNodeRoleTextbox(t *testing.T) {
	doc := parseHTML(t, `<div role="textbox">Input</div>`)
	node := findFirst(doc, "div")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatInput {
		t.Errorf("expected CatInput for role=textbox, got %q", got)
	}
}

func TestClassifyNodeRoleSearchbox(t *testing.T) {
	doc := parseHTML(t, `<div role="searchbox">Search</div>`)
	node := findFirst(doc, "div")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatInput {
		t.Errorf("expected CatInput for role=searchbox, got %q", got)
	}
}

func TestClassifyNodeRoleSpinbutton(t *testing.T) {
	doc := parseHTML(t, `<div role="spinbutton">123</div>`)
	node := findFirst(doc, "div")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatInput {
		t.Errorf("expected CatInput for role=spinbutton, got %q", got)
	}
}

func TestClassifyNodeRoleCombobox(t *testing.T) {
	doc := parseHTML(t, `<div role="combobox">Options</div>`)
	node := findFirst(doc, "div")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatSelect {
		t.Errorf("expected CatSelect for role=combobox, got %q", got)
	}
}

func TestClassifyNodeRoleListbox(t *testing.T) {
	doc := parseHTML(t, `<div role="listbox">Items</div>`)
	node := findFirst(doc, "div")
	got := semantic.ClassifyNode(node)
	if got != semantic.CatSelect {
		t.Errorf("expected CatSelect for role=listbox, got %q", got)
	}
}

func TestClassifyNodeTextElements(t *testing.T) {
	textTags := []string{"h1", "h2", "h3", "h4", "h5", "h6", "p", "li", "dt", "dd", "blockquote", "pre", "code", "figcaption", "article", "section", "main", "aside", "header", "footer", "nav"}
	for _, tag := range textTags {
		t.Run(tag, func(t *testing.T) {
			doc := parseHTML(t, "<"+tag+">Content</"+tag+">")
			node := findFirst(doc, tag)
			if node == nil {
				t.Fatalf("node not found for %s", tag)
			}
			got := semantic.ClassifyNode(node)
			if got != semantic.CatText {
				t.Errorf("expected CatText for %s, got %q", tag, got)
			}
		})
	}
}

func TestClassifyNodeTextElementsTable(t *testing.T) {
	tableTags := []string{"td", "th"}
	for _, tag := range tableTags {
		t.Run(tag, func(t *testing.T) {
			doc := parseHTML(t, "<table><tr><"+tag+">Content</"+tag+"></tr></table>")
			node := findFirst(doc, tag)
			if node == nil {
				t.Fatalf("node not found for %s", tag)
			}
			got := semantic.ClassifyNode(node)
			if got != semantic.CatText {
				t.Errorf("expected CatText for %s, got %q", tag, got)
			}
		})
	}
}
