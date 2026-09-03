package daemon

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
	"github.com/danieljustus/symaira-browse/internal/injection"
)

// scanFetchInjectionHTML applies the same bounded scanner and warning shape as
// browser snapshots. The HTML is untrusted input, so the scan is capped and a
// cap hit is reported as a warning rather than treated as a clean result.
func scanFetchInjectionHTML(pageHTML, patternsFile string) []Warning {
	limited := len(pageHTML) > maxInjectionScanHTMLBytes
	if limited {
		pageHTML = pageHTML[:maxInjectionScanHTMLBytes]
		for len(pageHTML) > 0 && !utf8.ValidString(pageHTML) {
			pageHTML = pageHTML[:len(pageHTML)-1]
		}
	}

	scanWarnings, err := injection.Scan(pageHTML, injection.ScanOptions{PatternsFile: patternsFile})
	if err != nil {
		return []Warning{{
			Kind:     "injection_scan",
			Severity: "warning",
			Message:  "injection scan failed: " + err.Error(),
		}}
	}
	warnings := make([]Warning, 0, len(scanWarnings)+1)
	for _, warning := range scanWarnings {
		warnings = append(warnings, Warning{
			Kind:     warning.Kind,
			Severity: warning.Severity,
			Message:  injectionMessage(warning),
			Ref:      warning.Ref,
			Excerpt:  warning.Excerpt,
		})
	}
	if limited {
		warnings = append(warnings, Warning{
			Kind:     "injection_scan",
			Severity: "warning",
			Message:  fmt.Sprintf("injection scan limited to %d bytes; content beyond the cap was not scanned", maxInjectionScanHTMLBytes),
		})
	}
	return warnings
}

func warningCacheKey(pageHTML, patternsFile string) string {
	return fmt.Sprintf("%x\x00%s", sha256Bytes(pageHTML), patternsFile)
}

func sha256Bytes(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

func wrapFetchMarkdown(markdown string, boundary injection.Boundary) string {
	prefix, body := splitFetchMarkdownPrefix(markdown)
	return prefix + boundary.WrapText(body)
}

func splitFetchMarkdownPrefix(markdown string) (prefix, body string) {
	body = markdown
	if strings.HasPrefix(body, "---\n") {
		const close = "\n---\n\n"
		if end := strings.Index(body[len("---\n"):], close); end >= 0 {
			end += len("---\n") + len(close)
			prefix += body[:end]
			body = body[end:]
		}
	}
	if strings.HasPrefix(body, "> **") {
		if end := strings.Index(body, "\n\n"); end >= 0 {
			end += len("\n\n")
			prefix += body[:end]
			body = body[end:]
		}
	}
	return prefix, body
}

func addFetchBoundary(response map[string]any, format pipeline.Format, origin string, raw bool) error {
	if origin == "" {
		origin, _ = response["url"].(string)
	}
	boundary, err := injection.New(origin)
	if err != nil {
		return fmt.Errorf("create fetch content boundary: %w", err)
	}
	response["content_boundaries"] = boundary
	if raw {
		if content, ok := response["content"].(string); ok {
			response["content"] = boundary.WrapText(content)
		}
		return nil
	}
	switch format {
	case pipeline.FormatMarkdown:
		field := "markdown"
		if _, ok := response[field]; !ok {
			field = "content"
		}
		if content, ok := response[field].(string); ok {
			response[field] = wrapFetchMarkdown(content, boundary)
		}
	case pipeline.FormatText:
		if content, ok := response["content"].(string); ok {
			response["content"] = boundary.WrapText(content)
		}
	}
	return nil
}

func addFetchCacheHint(response map[string]any, output, surface string) {
	cacheID := pipeline.CacheIDFromOutput(output)
	if cacheID == "" {
		return
	}
	response["cache_id"] = cacheID
	if surface == "mcp" {
		response["cache_hint"] = fmt.Sprintf("Call the cache_get MCP tool with cache_id=%s", cacheID)
		return
	}
	response["cache_hint"] = fmt.Sprintf("symbrowse cache get %s", cacheID)
}
