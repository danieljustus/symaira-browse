package archive

import (
	"fmt"
	"strings"
)

// WaybackBaseURL is the base URL for fetching archived pages.
const WaybackBaseURL = "https://web.archive.org/web"

// RewriteURL rewrites a normal URL into a Wayback Machine archive URL.
// If timestamp is empty, it uses "*" to let the Wayback Machine pick the latest snapshot.
// The format is: https://web.archive.org/web/<timestamp>/<url>
func RewriteURL(rawURL, timestamp string) string {
	if timestamp == "" {
		timestamp = "*"
	}
	return fmt.Sprintf("%s/%s/%s", WaybackBaseURL, timestamp, rawURL)
}

// ParseWaybackURL extracts the original URL from a Wayback Machine archive URL.
// It handles URLs like:
//   - https://web.archive.org/web/20260101120000/https://example.com
//   - https://web.archive.org/web/20260101120000id_/https://example.com
//   - https://web.archive.org/web/20260101120000if_/https://example.com
//
// Returns ("", false) if the URL is not a Wayback archive URL.
func ParseWaybackURL(rawURL string) (original string, ok bool) {
	const prefix = WaybackBaseURL + "/"
	if !strings.HasPrefix(rawURL, prefix) {
		return "", false
	}

	remainder := rawURL[len(prefix):]

	// Skip the timestamp (or *) and optional modifier (id_, if_, etc.).
	// The timestamp is 14 digits or "*", followed by optional id_/if_ modifier, then "/".
	parts := strings.SplitN(remainder, "/", 2)
	if len(parts) < 2 {
		return "", false
	}

	// parts[0] is timestamp (or timestamp+modifier like "20260101id_")
	// parts[1] is the original URL.
	return parts[1], true
}

// StripWaybackToolbar removes Wayback Machine toolbar and banner elements from HTML.
// The Wayback Machine injects toolbar markup that pollutes the page content.
// This function removes common Wayback elements and their content.
func StripWaybackToolbar(html string) string {
	// Remove the Wayback Machine toolbar div (id="wm-ipp-base" or id="wm-ipp")
	html = removeElementByID(html, "wm-ipp-base")
	html = removeElementByID(html, "wm-ipp")

	// Remove the Wayback Machine banner/notice
	html = removeElementByClass(html, "wb-autocomplete-suggestions")

	// Remove Wayback-injected script blocks that reference archive.org
	html = removeWaybackScripts(html)

	return html
}

// removeElementByID removes an HTML element with the given ID.
// This is a simple string-based removal — not a full HTML parser.
func removeElementByID(html, id string) string {
	// Try to find and remove <div id="ID">...</div> blocks.
	lower := strings.ToLower(html)
	idAttr := fmt.Sprintf(`id="%s"`, id)
	idx := strings.Index(lower, strings.ToLower(idAttr))
	if idx < 0 {
		// Try single-quoted attribute.
		idAttr = fmt.Sprintf(`id='%s'`, id)
		idx = strings.Index(lower, strings.ToLower(idAttr))
		if idx < 0 {
			return html
		}
	}

	// Walk backward to find the opening < of the element.
	startTag := idx
	for startTag > 0 && html[startTag-1] != '<' {
		startTag--
	}
	if startTag > 0 {
		startTag-- // include the '<'
	}

	// Find the matching closing tag.
	// Look for </div> after the opening tag.
	closeTag := strings.ToLower(html[idx:])
	closeIdx := strings.Index(closeTag, "</div>")
	if closeIdx < 0 {
		// Try </DIV>.
		closeIdx = strings.Index(closeTag, "</DIV>")
	}
	if closeIdx < 0 {
		return html
	}

	endPos := idx + closeIdx + len("</div>")
	if endPos > len(html) {
		endPos = len(html)
	}

	// Include any trailing newline.
	if endPos < len(html) && html[endPos] == '\n' {
		endPos++
	}

	return html[:startTag] + html[endPos:]
}

// removeElementByClass removes elements with the given class attribute.
func removeElementByClass(html, class string) string {
	classAttr := fmt.Sprintf(`class="%s"`, class)
	idx := strings.Index(strings.ToLower(html), strings.ToLower(classAttr))
	if idx < 0 {
		return html
	}

	startTag := idx
	for startTag > 0 && html[startTag-1] != '<' {
		startTag--
	}
	if startTag > 0 {
		startTag--
	}

	// Find the next closing tag of the same element type.
	closeIdx := strings.Index(html[idx:], "</")
	if closeIdx < 0 {
		return html
	}
	endPos := idx + closeIdx + strings.Index(html[idx+closeIdx:], ">") + 1
	if endPos > len(html) {
		endPos = len(html)
	}

	if endPos < len(html) && html[endPos] == '\n' {
		endPos++
	}

	return html[:startTag] + html[endPos:]
}

// removeWaybackScripts removes <script> blocks that reference archive.org.
func removeWaybackScripts(html string) string {
	result := html

	for {
		scriptStart := strings.Index(result, "<script")
		if scriptStart < 0 {
			break
		}
		scriptEnd := strings.Index(result[scriptStart:], "</script>")
		if scriptEnd < 0 {
			break
		}
		scriptEnd += scriptStart

		scriptContent := strings.ToLower(result[scriptStart : scriptEnd+len("</script>")])
		if strings.Contains(scriptContent, "archive.org") ||
			strings.Contains(scriptContent, "wombat.js") ||
			strings.Contains(scriptContent, "wm-ipp") {
			// Remove this script block.
			// Include any trailing newline.
			endPos := scriptEnd + len("</script>")
			if endPos < len(result) && result[endPos] == '\n' {
				endPos++
			}
			result = result[:scriptStart] + result[endPos:]
			continue
		}
		break
	}
	return result
}
