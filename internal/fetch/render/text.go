package render

import (
	"strings"
	"unicode/utf8"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
)

// Text renders only the textual content of the document (no Markdown formatting).
func Text(doc *agentdom.Document) string {
	var sb strings.Builder
	for _, el := range doc.Content {
		if el.Text != "" {
			sb.WriteString(el.Text)
			sb.WriteRune('\n')
		}
		for _, child := range el.Children {
			if child.Text != "" {
				sb.WriteString("  ")
				sb.WriteString(child.Text)
				sb.WriteRune('\n')
			}
		}
	}
	result := strings.TrimSpace(sb.String())
	_ = utf8.RuneCountInString(result)
	return result
}
