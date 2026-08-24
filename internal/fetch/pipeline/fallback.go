package pipeline

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/archive"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
)

// isThinContent returns true when the extracted main-content node has text
// below charThreshold AND a high link-to-text ratio (>0.5), or when a
// structural SPA skeleton signal is present, indicating a client-rendered
// SPA shell whose real content lives in an LLM-friendly twin.
//
// spaSkeleton is the output of DetectSPASkeleton, computed on the raw
// unfiltered HTML before dom.Filter. When true it acts as an additional
// trigger for thin content even when the link density alone is below 0.5.
func isThinContent(node *html.Node, charThreshold int, spaSkeleton bool) bool {
	textLen := countTextContent(node)
	if textLen >= charThreshold {
		return false // substantial text — not thin
	}
	if textLen == 0 {
		return true // no text at all
	}
	linkLen := countLinkContent(node)
	linkDensity := float64(linkLen) / float64(textLen)
	return linkDensity > 0.5 || spaSkeleton
}

// deriveMDTwinURL appends .md to the URL path, preserving query and fragment.
// Returns "" on parse failure.
func deriveMDTwinURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	u.Path = u.Path + ".md"
	return u.String()
}

// deriveLLMsTxtURL returns the site-level llms.txt URL for the given page URL.
// Returns "" on parse failure.
func deriveLLMsTxtURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	u.Path = "/llms.txt"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// tryFallback attempts to fetch the .md twin (primary), site-level llms.txt
// (secondary), or Wayback Machine snapshot (tertiary) and run the result
// through the pipeline. It never checks for thin content itself, preventing
// recursion.
//
// On success it returns the processed result, the fetch response (for cache
// metadata), and true. On any failure it returns nil, nil, false.
func tryFallback(ctx context.Context, c fetch.Client, eng Engine, rawURL string, o Options) (*Result, *fetch.Response, bool) {
	// Primary: .md twin
	mdURL := deriveMDTwinURL(rawURL)
	if mdURL != "" {
		if result, resp, ok := fetchAndProcess(ctx, c, eng, mdURL, rawURL, o); ok {
			slog.Debug("thin-content fallback succeeded (md twin)", "original", rawURL, "twin", mdURL, "chars", result.Meta.CharCount)
			return result, resp, true
		}
	}

	// Secondary: site-level llms.txt
	llmsURL := deriveLLMsTxtURL(rawURL)
	if llmsURL != "" {
		if result, resp, ok := fetchAndProcess(ctx, c, eng, llmsURL, rawURL, o); ok {
			slog.Debug("thin-content fallback succeeded (llms.txt)", "original", rawURL, "llms", llmsURL, "chars", result.Meta.CharCount)
			return result, resp, true
		}
	}

	// Tertiary: Wayback Machine snapshot (opt-in via WaybackFallback option)
	if o.WaybackFallback && !isWaybackURL(rawURL) {
		waybackURL := archive.RewriteURL(rawURL, o.WaybackTimestamp)
		if result, resp, ok := fetchAndProcess(ctx, c, eng, waybackURL, rawURL, o); ok {
			slog.Debug("thin-content fallback succeeded (wayback)", "original", rawURL, "wayback", waybackURL, "chars", result.Meta.CharCount)
			return result, resp, true
		}
	}

	slog.Debug("thin-content fallback exhausted", "url", rawURL)
	return nil, nil, false
}

// fetchAndProcess fetches a single URL and runs it through the full pipeline
// without fallback recursion. originURL is the original request URL used as the
// Doc.URL in the result.
func fetchAndProcess(ctx context.Context, c fetch.Client, eng Engine, fetchURL, originURL string, o Options) (*Result, *fetch.Response, bool) {
	fbOpts := o
	fbOpts.DisableFallback = true

	result, err := Run(ctx, c, eng, fetchURL, fbOpts)
	if err != nil {
		slog.Debug("fallback pipeline failed", "url", fetchURL, "error", err)
		return nil, nil, false
	}

	// Override Doc.URL with the original request URL
	if result.Doc != nil {
		result.Doc.URL = originURL
	}

	return result, &fetch.Response{
		FinalURL:   result.Meta.FinalURL,
		StatusCode: result.Meta.StatusCode,
		Protocol:   result.Meta.Protocol,
	}, true
}

// tryFetchAndProcess attempts to fetch a URL and run it through the pipeline.
// Returns the result and true on success, nil and false on failure.
func tryFetchAndProcess(ctx context.Context, c fetch.Client, eng Engine, fetchURL, originURL string, o Options) (*Result, *fetch.Response, bool) {
	return fetchAndProcess(ctx, c, eng, fetchURL, originURL, o)
}

// countTextContent returns the total visible text character count of a node
// subtree (excluding tags, attributes, scripts).
func countTextContent(n *html.Node) int {
	if n.Type == html.TextNode {
		return utf8.RuneCountInString(strings.TrimSpace(n.Data))
	}
	total := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		total += countTextContent(c)
	}
	return total
}

// countLinkContent returns the text character count inside <a> elements.
func countLinkContent(n *html.Node) int {
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "a" {
		return countTextContent(n)
	}
	total := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		total += countLinkContent(c)
	}
	return total
}
