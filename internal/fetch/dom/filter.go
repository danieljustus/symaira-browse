package dom

import (
	"strings"

	"golang.org/x/net/html"
)

// dropTags are elements whose entire subtree is discarded.
var dropTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "svg": true,
	"iframe": true, "object": true, "embed": true, "canvas": true,
	"audio": true, "video": true, "template": true, "picture": true,
}

// semanticAttrs are the only attributes preserved after filtering.
var semanticAttrs = map[string]bool{
	"href": true, "src": true, "alt": true, "title": true,
	"aria-label": true, "aria-describedby": true, "placeholder": true,
	"name": true, "type": true, "value": true, "checked": true,
	"selected": true, "disabled": true, "readonly": true,
	"for": true, "id": true, "role": true, "action": true,
	"method": true, "enctype": true,
}

// hiddenClasses marks an element as visually hidden.
var hiddenClasses = map[string]bool{
	"hidden": true, "sr-only": true, "visually-hidden": true,
	"invisible": true, "d-none": true, "hide": true,
}

// Filter removes non-semantic nodes from the tree in-place and returns
// the modified root. It drops scripts, styles, hidden elements, and strips
// non-semantic attributes.
func Filter(n *html.Node) *html.Node {
	filterNode(n)
	return n
}

func filterNode(n *html.Node) bool {
	// Process children first (depth-first, collect to avoid iterator invalidation)
	var toRemove []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if shouldDrop(c) {
			toRemove = append(toRemove, c)
		} else {
			filterNode(c)
		}
	}
	for _, c := range toRemove {
		n.RemoveChild(c)
	}

	// Strip non-semantic attributes from element nodes
	if n.Type == html.ElementNode {
		var kept []html.Attribute
		for _, a := range n.Attr {
			if semanticAttrs[strings.ToLower(a.Key)] {
				kept = append(kept, a)
			}
		}
		n.Attr = kept
	}

	return false
}

func shouldDrop(n *html.Node) bool {
	if n.Type == html.CommentNode {
		return true
	}
	if n.Type == html.ElementNode {
		tag := strings.ToLower(n.Data)
		if dropTags[tag] {
			return true
		}
		if isHidden(n) {
			return true
		}
	}
	return false
}

func isHidden(n *html.Node) bool {
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		val := strings.ToLower(strings.TrimSpace(a.Val))

		if key == "hidden" {
			return true
		}
		if key == "aria-hidden" && val == "true" {
			return true
		}
		if key == "style" {
			if strings.Contains(val, "display:none") ||
				strings.Contains(val, "display: none") ||
				strings.Contains(val, "visibility:hidden") ||
				strings.Contains(val, "visibility: hidden") {
				return true
			}
		}
		if key == "class" {
			for _, cls := range strings.Fields(val) {
				if hiddenClasses[cls] {
					return true
				}
			}
		}
	}
	return false
}
