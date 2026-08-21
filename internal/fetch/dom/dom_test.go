package dom

import (
	"testing"

	"golang.org/x/net/html"
)

func findNode(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findNode(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func hasTag(n *html.Node, tag string) bool { return findNode(n, tag) != nil }

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func parseAndFilter(t *testing.T, body string) *html.Node {
	t.Helper()
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return Filter(tree.Root)
}

func countTags(n *html.Node, tag string) int {
	count := 0
	if n.Type == html.ElementNode && n.Data == tag {
		count++
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		count += countTags(c, tag)
	}
	return count
}

func hasComment(n *html.Node) bool {
	if n.Type == html.CommentNode {
		return true
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasComment(c) {
			return true
		}
	}
	return false
}

func TestParse_BasicHTML(t *testing.T) {
	body := `<!DOCTYPE html><html lang="en"><head><title>Hello</title></head><body><p>World</p></body></html>`
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if tree.Root == nil {
		t.Fatal("Root is nil")
	}
	if tree.Title != "Hello" {
		t.Errorf("Title = %q, want %q", tree.Title, "Hello")
	}
	if tree.Lang != "en" {
		t.Errorf("Lang = %q, want %q", tree.Lang, "en")
	}
}

func TestParse_MissingTitle(t *testing.T) {
	body := `<html><head></head><body><p>No title here</p></body></html>`
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if tree.Title != "" {
		t.Errorf("Title = %q, want empty", tree.Title)
	}
}

func TestParse_MissingLang(t *testing.T) {
	body := `<html><head><title>X</title></head><body></body></html>`
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if tree.Lang != "" {
		t.Errorf("Lang = %q, want empty", tree.Lang)
	}
}

func TestParse_TitleWhitespace(t *testing.T) {
	body := `<html><head><title>  Spaced Out  </title></head><body></body></html>`
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if tree.Title != "Spaced Out" {
		t.Errorf("Title = %q, want %q", tree.Title, "Spaced Out")
	}
}

func TestParse_MalformedHTML(t *testing.T) {
	body := `<html><head><title>Broken</title></head><body><p>Still works<p>Another`
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if tree.Title != "Broken" {
		t.Errorf("Title = %q, want %q", tree.Title, "Broken")
	}
	if tree.Root == nil {
		t.Fatal("Root is nil after malformed parse")
	}
}

func TestParse_EmptyBody(t *testing.T) {
	tree, err := Parse([]byte(""))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if tree.Root == nil {
		t.Fatal("Root is nil for empty body")
	}
	if tree.Title != "" {
		t.Errorf("Title = %q, want empty", tree.Title)
	}
}

func TestParse_LangSubtag(t *testing.T) {
	body := `<html lang="pt-BR"><head><title>Oi</title></head><body></body></html>`
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if tree.Lang != "pt-BR" {
		t.Errorf("Lang = %q, want %q", tree.Lang, "pt-BR")
	}
}

func TestFilter_DropsScript(t *testing.T) {
	root := parseAndFilter(t, `<html><body><script>alert(1)</script><p>Keep</p></body></html>`)
	if hasTag(root, "script") {
		t.Error("script tag should be dropped")
	}
	if !hasTag(root, "p") {
		t.Error("p tag should be preserved")
	}
}

func TestFilter_DropsStyle(t *testing.T) {
	root := parseAndFilter(t, `<html><head><style>body{color:red}</style></head><body><p>OK</p></body></html>`)
	if hasTag(root, "style") {
		t.Error("style tag should be dropped")
	}
}

func TestFilter_DropsNoscript(t *testing.T) {
	root := parseAndFilter(t, `<html><body><noscript><p>No JS</p></noscript><p>Yes</p></body></html>`)
	if hasTag(root, "noscript") {
		t.Error("noscript tag should be dropped")
	}
}

func TestFilter_DropsSVG(t *testing.T) {
	root := parseAndFilter(t, `<html><body><svg><circle r="5"/></svg><p>Text</p></body></html>`)
	if hasTag(root, "svg") {
		t.Error("svg tag should be dropped")
	}
}

func TestFilter_DropsIframe(t *testing.T) {
	root := parseAndFilter(t, `<html><body><iframe src="x"></iframe><p>OK</p></body></html>`)
	if hasTag(root, "iframe") {
		t.Error("iframe tag should be dropped")
	}
}

func TestFilter_DropsAllNonSemanticTags(t *testing.T) {
	tags := []string{"object", "embed", "canvas", "audio", "video", "template", "picture"}
	for _, tag := range tags {
		t.Run(tag, func(t *testing.T) {
			body := `<html><body><` + tag + `>x</` + tag + `><p>Keep</p></body></html>`
			root := parseAndFilter(t, body)
			if hasTag(root, tag) {
				t.Errorf("%s tag should be dropped", tag)
			}
		})
	}
}

func TestFilter_DropsHiddenAttr(t *testing.T) {
	root := parseAndFilter(t, `<html><body><div hidden><p>Secret</p></div><p>Visible</p></body></html>`)
	if hasTag(root, "div") {
		t.Error("div[hidden] should be dropped")
	}
	if !hasTag(root, "p") {
		t.Error("visible p should remain")
	}
}

func TestFilter_DropsAriaHiddenTrue(t *testing.T) {
	body := `<html><body><span aria-hidden="true">X</span><span>Y</span></body></html>`
	tree, _ := Parse([]byte(body))
	Filter(tree.Root)
	count := countTags(tree.Root, "span")
	if count != 1 {
		t.Errorf("expected 1 span after filter, got %d", count)
	}
}

func TestFilter_DropsDisplayNone(t *testing.T) {
	root := parseAndFilter(t, `<html><body><div style="display:none">Gone</div><p>Here</p></body></html>`)
	if hasTag(root, "div") {
		t.Error("div with display:none should be dropped")
	}
}

func TestFilter_DropsDisplayNoneWithSpace(t *testing.T) {
	root := parseAndFilter(t, `<html><body><div style="display: none">Gone</div></body></html>`)
	if hasTag(root, "div") {
		t.Error("div with 'display: none' should be dropped")
	}
}

func TestFilter_DropsVisibilityHidden(t *testing.T) {
	root := parseAndFilter(t, `<html><body><div style="visibility:hidden">Gone</div></body></html>`)
	if hasTag(root, "div") {
		t.Error("div with visibility:hidden should be dropped")
	}
}

func TestFilter_DropsHiddenClasses(t *testing.T) {
	classes := []string{"hidden", "sr-only", "visually-hidden", "invisible", "d-none", "hide"}
	for _, cls := range classes {
		t.Run(cls, func(t *testing.T) {
			body := `<html><body><div class="` + cls + `">X</div><p>OK</p></body></html>`
			root := parseAndFilter(t, body)
			if hasTag(root, "div") {
				t.Errorf("div with class=%q should be dropped", cls)
			}
		})
	}
}

func TestFilter_DropsComments(t *testing.T) {
	body := `<html><body><!-- secret comment --><p>Visible</p></body></html>`
	tree, _ := Parse([]byte(body))
	Filter(tree.Root)
	if hasComment(tree.Root) {
		t.Error("comments should be dropped")
	}
}

func TestFilter_StripsNonSemanticAttrs(t *testing.T) {
	body := `<html><body><a href="/x" class="link" data-track="123" onclick="foo()">Link</a></body></html>`
	root := parseAndFilter(t, body)
	a := findNode(root, "a")
	if a == nil {
		t.Fatal("a tag not found")
	}
	if hasAttr(a, "class") {
		t.Error("class should be stripped")
	}
	if hasAttr(a, "data-track") {
		t.Error("data-track should be stripped")
	}
	if hasAttr(a, "onclick") {
		t.Error("onclick should be stripped")
	}
}

func TestFilter_PreservesSemanticAttrs(t *testing.T) {
	attrs := map[string]string{
		"href":             "/page",
		"src":              "/img.png",
		"alt":              "photo",
		"title":            "tip",
		"aria-label":       "Close",
		"aria-describedby": "desc1",
		"placeholder":      "Enter…",
		"name":             "q",
		"type":             "text",
		"value":            "42",
		"checked":          "checked",
		"selected":         "selected",
		"disabled":         "disabled",
		"readonly":         "readonly",
		"for":              "input1",
		"id":               "main",
		"role":             "button",
		"action":           "/submit",
		"method":           "POST",
		"enctype":          "multipart/form-data",
	}
	for k, v := range attrs {
		t.Run(k, func(t *testing.T) {
			body := `<html><body><div ` + k + `="` + v + `">X</div></body></html>`
			root := parseAndFilter(t, body)
			div := findNode(root, "div")
			if div == nil {
				t.Fatal("div not found")
			}
			if got := attrVal(div, k); got != v {
				t.Errorf("attr %s = %q, want %q", k, got, v)
			}
		})
	}
}

func TestFilter_Integration(t *testing.T) {
	body := `<!DOCTYPE html>
<html lang="en">
<head><title>Test</title><style>body{}</style></head>
<body>
  <script>var x=1;</script>
  <nav class="main-nav" role="navigation">
    <a href="/home" onclick="track()">Home</a>
  </nav>
  <main id="content">
    <h1 title="Heading">Hello</h1>
    <div hidden>Secret</div>
    <img src="/pic.jpg" alt="Photo" class="responsive" data-lazy="1">
    <!-- ad slot -->
    <div aria-hidden="true" class="ad-banner">Ad</div>
    <p>Keep this</p>
  </main>
</body></html>`

	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tree.Title != "Test" {
		t.Errorf("Title = %q, want %q", tree.Title, "Test")
	}
	if tree.Lang != "en" {
		t.Errorf("Lang = %q, want %q", tree.Lang, "en")
	}

	root := Filter(tree.Root)

	for _, tag := range []string{"script", "style"} {
		if hasTag(root, tag) {
			t.Errorf("%s should be dropped", tag)
		}
	}

	if countTags(root, "div") != 0 {
		t.Errorf("all divs should be dropped (hidden + aria-hidden), got %d", countTags(root, "div"))
	}

	if hasComment(root) {
		t.Error("comments should be dropped")
	}

	a := findNode(root, "a")
	if a == nil {
		t.Fatal("a tag not found")
	}
	if attrVal(a, "href") != "/home" {
		t.Errorf("a href = %q, want /home", attrVal(a, "href"))
	}
	if hasAttr(a, "onclick") {
		t.Error("onclick should be stripped from a")
	}

	img := findNode(root, "img")
	if img == nil {
		t.Fatal("img not found")
	}
	if attrVal(img, "src") != "/pic.jpg" {
		t.Errorf("img src = %q", attrVal(img, "src"))
	}
	if attrVal(img, "alt") != "Photo" {
		t.Errorf("img alt = %q", attrVal(img, "alt"))
	}
	if hasAttr(img, "class") {
		t.Error("img class should be stripped")
	}
	if hasAttr(img, "data-lazy") {
		t.Error("img data-lazy should be stripped")
	}

	h1 := findNode(root, "h1")
	if h1 == nil {
		t.Fatal("h1 not found")
	}
	if attrVal(h1, "title") != "Heading" {
		t.Errorf("h1 title = %q", attrVal(h1, "title"))
	}

	nav := findNode(root, "nav")
	if nav == nil {
		t.Fatal("nav not found")
	}
	if attrVal(nav, "role") != "navigation" {
		t.Errorf("nav role = %q", attrVal(nav, "role"))
	}
	if hasAttr(nav, "class") {
		t.Error("nav class should be stripped")
	}

	main := findNode(root, "main")
	if main == nil {
		t.Fatal("main not found")
	}
	if attrVal(main, "id") != "content" {
		t.Errorf("main id = %q", attrVal(main, "id"))
	}
}

func TestFilter_EmptyTree(t *testing.T) {
	tree, err := Parse([]byte(""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	root := Filter(tree.Root)
	if root == nil {
		t.Fatal("Filter returned nil for empty tree")
	}
}

func TestFilter_NestedDropTags(t *testing.T) {
	body := `<html><body><div><script>x</script><p>OK</p></div></body></html>`
	root := parseAndFilter(t, body)
	if !hasTag(root, "div") {
		t.Error("div should remain")
	}
	if hasTag(root, "script") {
		t.Error("script should be dropped")
	}
	if !hasTag(root, "p") {
		t.Error("p should remain")
	}
}

func TestFilter_HiddenClassAmongOthers(t *testing.T) {
	body := `<html><body><div class="foo hidden bar">X</div><p>OK</p></body></html>`
	root := parseAndFilter(t, body)
	if hasTag(root, "div") {
		t.Error("div with 'hidden' among other classes should be dropped")
	}
}

func TestFilter_AriaHiddenFalseNotDropped(t *testing.T) {
	body := `<html><body><div aria-hidden="false">Visible</div></body></html>`
	root := parseAndFilter(t, body)
	if !hasTag(root, "div") {
		t.Error("div with aria-hidden=false should NOT be dropped")
	}
}

func TestParse_FragmentNoHTMLTag(t *testing.T) {
	body := `<p>Just a fragment</p>`
	tree, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tree.Lang != "" {
		t.Errorf("Lang = %q, want empty for fragment", tree.Lang)
	}
}
