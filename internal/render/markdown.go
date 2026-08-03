// Package render implements symbrowse's own minimal HTML→Markdown path and
// the fetch-compatible output schema (ARCHITEKTUR.md §6.1). Until the
// semantic DOM pipeline moves to corekit (B-61) this package is the standalone
// implementation; field names and frontmatter keys match symfetch exactly
// (see testdata/fetch-schema.json for the documented source).
package render

import (
	"strings"

	"golang.org/x/net/html"
)

// blockElements render with a surrounding blank line and a block marker.
var blockElements = map[string]string{
	"h1": "# ", "h2": "## ", "h3": "### ", "h4": "#### ", "h5": "##### ", "h6": "###### ",
	"p":   "",
	"pre": "```\n",
	"hr":  "---",
}

// skipElements are never rendered into the markdown output.
var skipElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"head": true, "title": true, "meta": true, "link": true, "iframe": true,
}

var listItemMarker = map[string]string{"ul": "- ", "ol": "1. "}

// Heading is one outline entry.
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// Markdown converts an HTML document into LLM-optimized Markdown.
func Markdown(source string) string {
	document, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return ""
	}
	var builder strings.Builder
	renderNode(document, &builder, 0, false)
	return strings.TrimSpace(builder.String())
}

// Outline extracts the heading structure (h1–h6) of a document.
func Outline(source string) []Heading {
	document, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return nil
	}
	var headings []Heading
	collectHeadings(document, &headings)
	return headings
}

// Lang extracts the document language from the <html lang="…"> attribute.
func Lang(source string) string {
	document, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return ""
	}
	var lang string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if lang != "" {
			return
		}
		if node.Type == html.ElementNode && node.Data == "html" {
			for _, attribute := range node.Attr {
				if attribute.Key == "lang" {
					lang = attribute.Val
					return
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return lang
}

func renderNode(node *html.Node, builder *strings.Builder, listDepth int, inPre bool) {
	switch node.Type {
	case html.TextNode:
		text := node.Data
		if !inPre {
			text = compressWhitespace(text)
		}
		builder.WriteString(text)
		return
	case html.CommentNode:
		return
	case html.DocumentNode:
		renderChildren(node, builder, listDepth, inPre)
		return
	case html.ElementNode:
		renderElement(node, builder, listDepth, inPre)
	}
}

func renderElement(node *html.Node, builder *strings.Builder, listDepth int, inPre bool) {
	tag := strings.ToLower(node.Data)
	if skipElements[tag] {
		return
	}

	switch tag {
	case "br":
		builder.WriteString("\n")
		return
	case "img":
		renderImage(node, builder)
		return
	case "a":
		renderLink(node, builder, listDepth, inPre)
		return
	case "strong", "b":
		text := nodeText(node)
		if strings.TrimSpace(text) != "" {
			builder.WriteString("**")
			renderChildren(node, builder, listDepth, inPre)
			builder.WriteString("**")
		}
		return
	case "em", "i":
		text := nodeText(node)
		if strings.TrimSpace(text) != "" {
			builder.WriteString("*")
			renderChildren(node, builder, listDepth, inPre)
			builder.WriteString("*")
		}
		return
	case "code":
		if node.Parent != nil && strings.ToLower(node.Parent.Data) == "pre" {
			renderChildren(node, builder, listDepth, true)
			return
		}
		text := nodeText(node)
		if strings.TrimSpace(text) != "" {
			builder.WriteString("`")
			builder.WriteString(text)
			builder.WriteString("`")
		}
		return
	}

	if marker, ok := blockElements[tag]; ok {
		builder.WriteString("\n\n")
		builder.WriteString(marker)
		if tag == "pre" {
			renderChildren(node, builder, listDepth, true)
			builder.WriteString("\n```")
			return
		}
		renderChildren(node, builder, listDepth, inPre)
		builder.WriteString("\n\n")
		return
	}

	if marker, ok := listItemMarker[tag]; ok {
		builder.WriteString("\n\n")
		renderList(node, builder, listDepth+1, marker)
		return
	}

	// Unknown container (div, section, article, main, aside, header, footer,
	// nav, span, table rows, …): recurse without emitting markers.
	renderChildren(node, builder, listDepth, inPre)
}

func renderList(node *html.Node, builder *strings.Builder, depth int, marker string) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || strings.ToLower(child.Data) != "li" {
			continue
		}
		builder.WriteString(strings.Repeat("  ", depth-1))
		builder.WriteString(marker)
		renderChildren(child, builder, depth, false)
		builder.WriteString("\n")
	}
}

func renderChildren(node *html.Node, builder *strings.Builder, listDepth int, inPre bool) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		renderNode(child, builder, listDepth, inPre)
	}
}

func renderLink(node *html.Node, builder *strings.Builder, listDepth int, inPre bool) {
	href := attribute(node, "href")
	text := nodeText(node)
	if strings.TrimSpace(text) == "" {
		text = href
	}
	if href == "" || strings.HasPrefix(href, "#") {
		builder.WriteString(text)
		return
	}
	builder.WriteString("[")
	builder.WriteString(text)
	builder.WriteString("](")
	builder.WriteString(href)
	builder.WriteString(")")
}

func renderImage(node *html.Node, builder *strings.Builder) {
	alt := attribute(node, "alt")
	src := attribute(node, "src")
	if src == "" {
		return
	}
	builder.WriteString("![")
	builder.WriteString(alt)
	builder.WriteString("](")
	builder.WriteString(src)
	builder.WriteString(")")
}

func collectHeadings(node *html.Node, headings *[]Heading) {
	if node.Type == html.ElementNode && len(node.Data) == 2 && node.Data[0] == 'h' && node.Data[1] >= '1' && node.Data[1] <= '6' {
		level := int(node.Data[1] - '0')
		text := strings.TrimSpace(nodeText(node))
		if text != "" {
			*headings = append(*headings, Heading{Level: level, Text: text})
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectHeadings(child, headings)
	}
}

func nodeText(node *html.Node) string {
	var builder strings.Builder
	collectText(node, &builder)
	return compressWhitespace(builder.String())
}

func collectText(node *html.Node, builder *strings.Builder) {
	if node.Type == html.TextNode {
		builder.WriteString(node.Data)
		return
	}
	if node.Type == html.ElementNode && skipElements[node.Data] {
		return
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectText(child, builder)
	}
}

func attribute(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

func compressWhitespace(text string) string {
	var builder strings.Builder
	space := false
	for _, r := range text {
		switch r {
		case ' ', '\t', '\n', '\r', '\f':
			space = true
		default:
			if space && builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteRune(r)
			space = false
		}
	}
	return builder.String()
}
