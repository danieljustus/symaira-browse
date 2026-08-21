package render

import (
	"fmt"
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
)

// Markdown renders a Document as LLM-optimized Markdown.
// It uses html-to-markdown for the content HTML subtree and emits
// interactive elements in a structured block.
func Markdown(doc *agentdom.Document, contentNode *html.Node, includeLinks bool) (string, error) {
	var sb strings.Builder

	// Render main content via html-to-markdown
	if contentNode != nil {
		converted, err := md.ConvertNode(contentNode)
		if err == nil && len(strings.TrimSpace(string(converted))) > 0 {
			sb.WriteString(strings.TrimSpace(string(converted)))
			sb.WriteString("\n\n")
		}
	}

	// Emit interactive elements
	if len(doc.Interactive) > 0 {
		sb.WriteString("## Interactive Elements\n\n")
		for _, el := range doc.Interactive {
			sb.WriteString(renderInteractiveElement(el))
		}
		sb.WriteString("\n")
	}

	// Emit links section
	if includeLinks {
		links := collectLinks(doc.Content)
		if len(links) > 0 {
			sb.WriteString("## Links\n\n")
			for _, l := range links {
				sb.WriteString(fmt.Sprintf("- [%s](%s)\n", l.text, l.href))
			}
			sb.WriteString("\n")
		}
	}

	// Emit data islands
	if len(doc.Islands) > 0 {
		sb.WriteString("## Data\n\n")
		for _, island := range doc.Islands {
			sb.WriteString(fmt.Sprintf("```json\n// Source: %s\n%s\n```\n\n",
				island.Source, string(island.JSON)))
		}
	}

	return sb.String(), nil
}

func renderInteractiveElement(el agentdom.Element) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("- **%s** `%s`", el.AgentID, el.Category))
	if el.Text != "" {
		sb.WriteString(fmt.Sprintf(": %s", el.Text))
	}
	for k, v := range el.Attrs {
		switch k {
		case "href":
			sb.WriteString(fmt.Sprintf(" → %s", v))
		case "placeholder":
			sb.WriteString(fmt.Sprintf(" [%s]", v))
		case "type":
			sb.WriteString(fmt.Sprintf(" (%s)", v))
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

type linkItem struct {
	text string
	href string
}

func collectLinks(elements []agentdom.Element) []linkItem {
	var links []linkItem
	for _, el := range elements {
		if el.Category == "link" {
			href := el.Attrs["href"]
			if href != "" && !strings.HasPrefix(href, "#") {
				text := el.Text
				if text == "" {
					text = href
				}
				links = append(links, linkItem{text: text, href: href})
			}
		}
		if len(el.Children) > 0 {
			links = append(links, collectLinks(el.Children)...)
		}
	}
	return links
}

// FormatMarkdownWithMeta prepends a metadata header (title, status, tokens,
// truncation warning, final URL) to the markdown output. This is the single
// source of truth for the metadata format used by both CLI and MCP.
// When meta.Escalate is set (SPA skeleton / thin content / JS challenge),
// a non-blocking hint line is appended inside the blockquote suggesting the
// symbrowse command for a JS-rendered re-fetch. Output for results without
// an escalate hint is byte-identical to previous versions.
func FormatMarkdownWithMeta(meta agentdom.Meta, output string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("> **%s** · %d · ~%d tokens",
		meta.Title, meta.StatusCode, meta.EstTokens))
	if meta.Truncated {
		sb.WriteString(" · ⚠ truncated")
	}
	if meta.LikelyClientRendered {
		sb.WriteString(" · ⚠ likely client-rendered")
	}
	sb.WriteString("\n> ")
	sb.WriteString(meta.FinalURL)
	if meta.Escalate != nil {
		sb.WriteString("\n> ⚠ ")
		sb.WriteString(meta.Escalate.Reason)
		sb.WriteString(" — use symbrowse for JS-rendered pages: ")
		sb.WriteString(meta.Escalate.Command)
	}
	sb.WriteString("\n\n")
	sb.WriteString(output)
	return sb.String()
}
