package pipeline

import (
	"strings"

	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
	"golang.org/x/net/html"
)

// frameworkRootIDs contains common SPA framework root element IDs whose
// presence as an empty node strongly suggests client-side rendering.
var frameworkRootIDs = map[string]bool{
	"app":    true,
	"root":   true,
	"__next": true,
	"_next":  true,
	"nuxt":   true,
	"svelte": true,
}

// DetectSPASkeleton inspects the raw HTML body bytes, parsed DOM tree, and
// extracted data islands for structural signs of a client-rendered SPA
// skeleton. It returns true when the combination of signals strongly
// suggests the page is a JavaScript-rendered shell with near-empty
// visible text:
//   - High ratio of raw HTML bytes to visible body text length
//   - Presence of hydration data islands (__NEXT_DATA__, preloaded state, etc.)
//   - Presence of an empty framework root node (e.g. <div id="app">)
//
// This function must be called on the unfiltered tree (before dom.Filter)
// because hydration markers live in <script> tags that Filter removes.
func DetectSPASkeleton(body []byte, root *html.Node, islands []semantic.DataIsland) bool {
	if len(body) == 0 || root == nil {
		return false
	}

	// Signal 1: large HTML with near-empty visible body text.
	// We count visible text (skipping script/style/noscript) to avoid
	// counting the hydration payload as "content".
	visibleLen := countVisibleText(root)
	if visibleLen == 0 {
		visibleLen = 1 // avoid division by zero; zero visible text is suspicious
	}
	ratio := float64(len(body)) / float64(visibleLen)
	largePayload := ratio > 50 && len(body) > 2048

	// Signal 2: hydration data islands (__NEXT_DATA__, preloaded state, etc.)
	hasHydration := hasHydrationIslands(islands)

	// Signal 3: empty framework root node (e.g. <div id="app"></div>)
	hasEmptyRoot := hasEmptyFrameworkRoot(root)

	// Require the high HTML-to-text ratio AND at least one structural signal.
	return largePayload && (hasHydration || hasEmptyRoot)
}

// countVisibleText returns the byte length of visible text content in the
// subtree, skipping script, style, noscript, and svg elements.
func countVisibleText(n *html.Node) int {
	if n.Type == html.ElementNode {
		tag := strings.ToLower(n.Data)
		if tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" {
			return 0
		}
	}
	if n.Type == html.TextNode {
		return len(strings.TrimSpace(n.Data))
	}
	total := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		total += countVisibleText(c)
	}
	return total
}

// hasHydrationIslands returns true when any island has a source indicating
// it contains hydration/initial-state data from a JS framework.
func hasHydrationIslands(islands []semantic.DataIsland) bool {
	for _, island := range islands {
		src := strings.ToLower(island.Source)
		if strings.Contains(src, "__next_data__") ||
			strings.Contains(src, "__nuxt__") ||
			strings.Contains(src, "__gatsby") ||
			strings.Contains(src, "preloaded") ||
			strings.Contains(src, "initial-state") ||
			strings.Contains(src, "__initial_state__") ||
			strings.Contains(src, "__app_state__") {
			return true
		}
	}
	return false
}

// hasEmptyFrameworkRoot walks the tree looking for a single element with a
// known SPA framework root ID (e.g. id="app", id="root", id="__next") that
// has no visible text or child elements — the classic SPA mount point.
func hasEmptyFrameworkRoot(n *html.Node) bool {
	if n.Type == html.ElementNode {
		id := getSPAAttr(n, "id")
		if frameworkRootIDs[id] && isEmptyOrMinimal(n) {
			return true
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hasEmptyFrameworkRoot(c) {
			return true
		}
	}
	return false
}

// isEmptyOrMinimal returns true if a node has no visible text content and
// no child elements — the typical SPA mount point pattern.
func isEmptyOrMinimal(n *html.Node) bool {
	textLen := 0
	elemCount := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			textLen += len(strings.TrimSpace(c.Data))
		} else if c.Type == html.ElementNode {
			elemCount++
		}
	}
	return textLen == 0 && elemCount == 0
}

// getSPAAttr returns the value of an attribute by key (case-insensitive).
func getSPAAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return strings.ToLower(a.Val)
		}
	}
	return ""
}
