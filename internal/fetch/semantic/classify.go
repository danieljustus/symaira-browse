package semantic

import (
	"strings"

	"golang.org/x/net/html"
)

// Category classifies a DOM element for the agent.
type Category string

const (
	CatButton   Category = "button"
	CatLink     Category = "link"
	CatInput    Category = "input"
	CatSelect   Category = "select"
	CatTextarea Category = "textarea"
	CatForm     Category = "form"
	CatText     Category = "text"
	CatImage    Category = "image"
)

// ClassifyNode returns the semantic category of an element node.
// Returns "" if the node is not a classifiable element.
func ClassifyNode(n *html.Node) Category {
	if n.Type != html.ElementNode {
		return ""
	}
	tag := strings.ToLower(n.Data)
	role := attrVal(n, "role")

	switch tag {
	case "button":
		return CatButton
	case "a":
		if attrVal(n, "href") != "" {
			return CatLink
		}
		return CatText
	case "input":
		inputType := strings.ToLower(attrVal(n, "type"))
		if inputType == "submit" || inputType == "button" || inputType == "reset" {
			return CatButton
		}
		if inputType == "image" {
			return CatImage
		}
		return CatInput
	case "select":
		return CatSelect
	case "textarea":
		return CatTextarea
	case "form":
		return CatForm
	case "img":
		if attrVal(n, "alt") != "" {
			return CatImage
		}
		return "" // no alt text → skip
	case "h1", "h2", "h3", "h4", "h5", "h6",
		"p", "li", "td", "th", "dt", "dd",
		"blockquote", "pre", "code", "figcaption",
		"article", "section", "main", "aside",
		"header", "footer", "nav":
		return CatText
	}

	// Role overrides
	switch role {
	case "button":
		return CatButton
	case "link":
		return CatLink
	case "textbox", "searchbox", "spinbutton":
		return CatInput
	case "combobox", "listbox":
		return CatSelect
	}

	return ""
}

// IsInteractive returns true for categories that get an @eN agent ID.
func IsInteractive(cat Category) bool {
	switch cat {
	case CatButton, CatLink, CatInput, CatSelect, CatTextarea, CatForm:
		return true
	}
	return false
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}
