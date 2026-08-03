package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// Options controls one read render.
type Options struct {
	// Selector restricts the render to the first matching subtree.
	Selector string
	// Filter removes every matching subtree before rendering.
	Filter string
	// Outline returns only the heading structure.
	Outline bool
	// Raw returns the (possibly filtered/selected) HTML instead of Markdown.
	Raw bool
	// MaxChars is the truncation budget; zero means DefaultMaxChars.
	MaxChars int
}

// Document is the fetch-compatible read output. The metadata field names
// (url, final_url, title, lang, fetched_at, tokens_est, schema_type) and the
// frontmatter keys are identical to symfetch; the content fields (markdown,
// outline, raw) are the browse-specific body carriers (see
// testdata/fetch-schema.json and docs/output-schema.md).
type Document struct {
	URL        string       `json:"url"`
	FinalURL   string       `json:"final_url,omitempty"`
	Title      string       `json:"title,omitempty"`
	Lang       string       `json:"lang,omitempty"`
	FetchedAt  string       `json:"fetched_at"`
	TokensEst  int          `json:"tokens_est"`
	SchemaType string       `json:"schema_type,omitempty"`
	Markdown   string       `json:"markdown,omitempty"`
	Raw        string       `json:"raw,omitempty"`
	Outline    []Heading    `json:"outline,omitempty"`
	Truncated  bool         `json:"truncated"`
	CharCount  int          `json:"char_count"`
	Meta       DocumentMeta `json:"meta,omitempty"`
}

// DocumentMeta carries the fetch-style response metadata (subset of
// symfetch's Meta with the same field names).
type DocumentMeta struct {
	StatusCode int  `json:"status_code"`
	Truncated  bool `json:"truncated"`
}

// Render builds the read document from raw page material.
func Render(pageHTML, pageTitle, pageURL string, options Options) (Document, error) {
	if options.MaxChars <= 0 {
		options.MaxChars = DefaultMaxChars
	}
	source := pageHTML
	selected, err := selectSource(source, options.Selector, options.Filter)
	if err != nil {
		return Document{}, err
	}
	source = selected

	document := Document{
		URL:       pageURL,
		Title:     strings.TrimSpace(pageTitle),
		Lang:      Lang(source),
		FetchedAt: FetchedAtNow(),
		CharCount: utf8RuneCount(source),
	}
	document.TokensEst = document.CharCount / 4
	document.SchemaType = schemaTypeFromHTML(source)

	switch {
	case options.Raw:
		raw, truncated := Truncate(source, options.MaxChars)
		document.Raw = raw
		document.Truncated = truncated
		document.Meta = DocumentMeta{StatusCode: 200, Truncated: truncated}
	case options.Outline:
		document.Outline = Outline(source)
		document.Meta = DocumentMeta{StatusCode: 200}
	default:
		markdown, truncated := Truncate(Markdown(source), options.MaxChars)
		document.Markdown = markdown
		document.Truncated = truncated
		document.Meta = DocumentMeta{StatusCode: 200, Truncated: truncated}
	}
	return document, nil
}

// selectSource narrows the HTML to the requested subtree and removes filtered
// subtrees.
func selectSource(source, selector, filter string) (string, error) {
	if selector == "" && filter == "" {
		return source, nil
	}
	document, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "", fmt.Errorf("parse page: %w", err)
	}
	if selector != "" {
		subtree := findSubtree(document, selector)
		if subtree == nil {
			return "", fmt.Errorf("selector %q did not match an element", selector)
		}
		var builder strings.Builder
		if err := html.Render(&builder, subtree); err != nil {
			return "", fmt.Errorf("render selected subtree: %w", err)
		}
		return builder.String(), nil
	}
	removeMatching(document, filter)
	var builder strings.Builder
	if err := html.Render(&builder, document); err != nil {
		return "", fmt.Errorf("render filtered document: %w", err)
	}
	return builder.String(), nil
}

func findSubtree(root *html.Node, selector string) *html.Node {
	var match func(*html.Node) *html.Node
	match = func(node *html.Node) *html.Node {
		if node.Type == html.ElementNode && matchesSelector(node, selector) {
			return node
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if found := match(child); found != nil {
				return found
			}
		}
		return nil
	}
	return match(root)
}

func removeMatching(root *html.Node, selector string) {
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		child := node.FirstChild
		for child != nil {
			next := child.NextSibling
			if child.Type == html.ElementNode && matchesSelector(child, selector) {
				node.RemoveChild(child)
			} else {
				walk(child)
			}
			child = next
		}
	}
	walk(root)
}

// matchesSelector implements the subset of CSS selectors browse needs: tag
// names, .class, #id, [attribute], [attribute=value] and combinations.
func matchesSelector(node *html.Node, selector string) bool {
	parts := strings.Fields(selector)
	for _, part := range parts {
		if !matchesSimpleSelector(node, part) {
			return false
		}
	}
	return true
}

func matchesSimpleSelector(node *html.Node, part string) bool {
	if part == "" {
		return false
	}
	switch part[0] {
	case '.':
		class := attributeOf(node, "class")
		for _, token := range strings.Fields(class) {
			if token == part[1:] {
				return true
			}
		}
		return false
	case '#':
		return attributeOf(node, "id") == part[1:]
	case '[':
		return matchesAttributeSelector(node, part)
	default:
		return node.Data == part
	}
}

func matchesAttributeSelector(node *html.Node, selector string) bool {
	inner := strings.TrimSuffix(strings.TrimPrefix(selector, "["), "]")
	key := inner
	want := ""
	exact := false
	if idx := strings.Index(inner, "="); idx >= 0 {
		key = inner[:idx]
		want = strings.Trim(inner[idx+1:], `"'`)
		exact = true
	}
	value := attributeOf(node, key)
	if value == "" {
		return false
	}
	if !exact {
		return true
	}
	return value == want
}

func attributeOf(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

// schemaTypeFromHTML extracts a JSON-LD @type from the first ld+json island,
// mirroring symfetch's schema_type behaviour.
func schemaTypeFromHTML(source string) string {
	document, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return ""
	}
	var raw []byte
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if len(raw) > 0 {
			return
		}
		if node.Type == html.ElementNode && node.Data == "script" {
			isJSONLD := false
			for _, attribute := range node.Attr {
				if attribute.Key == "type" && attribute.Val == "application/ld+json" {
					isJSONLD = true
				}
			}
			if isJSONLD && node.FirstChild != nil && node.FirstChild.Type == html.TextNode {
				raw = []byte(node.FirstChild.Data)
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	if len(raw) == 0 {
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}
	if value, ok := data["@type"].(string); ok {
		return value
	}
	if value, ok := data["type"].(string); ok {
		return value
	}
	return ""
}

func utf8RuneCount(text string) int {
	count := 0
	for range text {
		count++
	}
	return count
}
