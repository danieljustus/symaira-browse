package agentdom

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
)

const (
	defaultMaxListItems = 20
	defaultMaxTextRunes = 2000
)

// Builder converts a filtered DOM subtree into an agentdom.Document.
type Builder struct {
	counter     int
	elements    []Element
	interactive []Element
	maxChars    int
	charsSeen   int
	truncated   bool
}

// NewBuilder creates a Builder with the given character budget.
func NewBuilder(maxChars int) *Builder {
	return &Builder{maxChars: maxChars}
}

// Build walks node and populates the Document's Content and Interactive slices.
func (b *Builder) Build(n *html.Node, doc *Document) {
	b.walk(n)
	doc.Content = b.elements
	doc.Interactive = b.interactive
}

func (b *Builder) walk(n *html.Node) {
	if b.truncated {
		return
	}

	cat := semantic.ClassifyNode(n)
	if cat != "" {
		// Container tags (main, article, section, etc.) classified as CatText
		// must recurse so nested interactive elements are not missed.
		if cat == semantic.CatText && isContainerTag(n.Data) {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				b.walk(c)
			}
			return
		}

		el := b.buildElement(n, cat)
		b.elements = append(b.elements, el)
		if semantic.IsInteractive(cat) {
			b.interactive = append(b.interactive, el)
		}
		return
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.walk(c)
	}
}

func isContainerTag(tag string) bool {
	switch strings.ToLower(tag) {
	case "article", "section", "main", "aside", "header", "footer", "nav", "div", "span":
		return true
	}
	return false
}

func (b *Builder) buildElement(n *html.Node, cat semantic.Category) Element {
	el := Element{
		Category: cat,
		Tag:      strings.ToLower(n.Data),
		Attrs:    extractAttrs(n),
	}

	if semantic.IsInteractive(cat) {
		b.counter++
		el.AgentID = fmt.Sprintf("@e%d", b.counter)
	}

	// Extract text content
	text := extractText(n, defaultMaxTextRunes)
	text = compressWhitespace(text)
	if text != "" {
		el.Text = b.consumeChars(text)
	}

	// For forms, recurse into children as sub-elements
	if cat == semantic.CatForm {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			childCat := semantic.ClassifyNode(c)
			if childCat != "" && childCat != semantic.CatForm {
				child := b.buildElement(c, childCat)
				el.Children = append(el.Children, child)
				if semantic.IsInteractive(childCat) {
					b.interactive = append(b.interactive, child)
				}
			}
		}
	}

	return el
}

func (b *Builder) consumeChars(s string) string {
	if b.maxChars <= 0 {
		return s
	}
	runes := utf8.RuneCountInString(s)
	remaining := b.maxChars - b.charsSeen
	if remaining <= 0 {
		b.truncated = true
		return ""
	}
	if runes > remaining {
		b.truncated = true
		// Truncate at rune boundary
		i := 0
		for r := 0; r < remaining; r++ {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
		b.charsSeen += remaining
		return s[:i] + "…"
	}
	b.charsSeen += runes
	return s
}

func extractAttrs(n *html.Node) map[string]string {
	if len(n.Attr) == 0 {
		return nil
	}
	m := make(map[string]string, len(n.Attr))
	for _, a := range n.Attr {
		m[a.Key] = a.Val
	}
	return m
}

func extractText(n *html.Node, maxRunes int) string {
	var sb strings.Builder
	collectText(n, &sb, &maxRunes)
	return sb.String()
}

func collectText(n *html.Node, sb *strings.Builder, remaining *int) {
	if *remaining <= 0 {
		return
	}
	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text == "" {
			return
		}
		runes := utf8.RuneCountInString(text)
		if runes > *remaining {
			// Truncate at rune boundary
			i := 0
			for r := 0; r < *remaining; r++ {
				_, size := utf8.DecodeRuneInString(text[i:])
				i += size
			}
			sb.WriteString(text[:i])
			*remaining = 0
			return
		}
		sb.WriteString(text)
		sb.WriteRune(' ')
		*remaining -= runes
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectText(c, sb, remaining)
	}
}

func compressWhitespace(s string) string {
	s = strings.TrimSpace(s)
	var sb strings.Builder
	lastSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			if !lastSpace {
				sb.WriteRune(' ')
			}
			lastSpace = true
		} else {
			sb.WriteRune(r)
			lastSpace = false
		}
	}
	return sb.String()
}
