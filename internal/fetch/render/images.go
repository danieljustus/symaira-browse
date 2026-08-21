package render

import (
	"regexp"
	"strings"
)

// base64ImgPattern matches Markdown image syntax with data URI src:
// ![alt](data:image/...;base64,...)
var base64ImgPattern = regexp.MustCompile(`!\[([^\]]*)\]\(data:image/[^)]+\)`)

// htmlBase64ImgPattern matches HTML <img> tags with data URI src:
// <img src="data:image/...;base64,..." alt="..." />
var htmlBase64ImgPattern = regexp.MustCompile(`<img[^>]*\ssrc="data:image/[^"]*"[^>]*/?>`)

// htmlBase64ImgAltPattern extracts the alt attribute from an <img> tag.
var htmlBase64ImgAltPattern = regexp.MustCompile(`alt="([^"]*)"`)

// StripBase64Images replaces inline base64-encoded image data in Markdown
// and HTML content with lightweight [IMAGE: alt] placeholders.
// Real image URLs (http/https) are preserved unchanged.
// The function is safe for content of any size (runs in linear time).
func StripBase64Images(content string) string {
	if !strings.Contains(content, "data:image") {
		return content
	}

	// Replace Markdown-style base64 images: ![alt](data:image/...)
	content = base64ImgPattern.ReplaceAllStringFunc(content, func(match string) string {
		submatch := base64ImgPattern.FindStringSubmatch(match)
		alt := strings.TrimSpace(submatch[1])
		if alt == "" {
			return "[IMAGE]"
		}
		return "[IMAGE: " + alt + "]"
	})

	// Replace HTML <img> tags with base64 data URI src
	content = htmlBase64ImgPattern.ReplaceAllStringFunc(content, func(match string) string {
		altMatch := htmlBase64ImgAltPattern.FindStringSubmatch(match)
		alt := ""
		if len(altMatch) > 1 {
			alt = strings.TrimSpace(altMatch[1])
		}
		if alt == "" {
			return "[IMAGE]"
		}
		return "[IMAGE: " + alt + "]"
	})

	return content
}
