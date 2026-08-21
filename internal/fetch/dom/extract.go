package dom

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// Tree is a parsed, filtered HTML document tree.
type Tree struct {
	Root  *html.Node
	Title string
	Lang  string
}

// Parse parses the UTF-8 normalised HTML body into a Tree.
func Parse(body []byte) (*Tree, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	t := &Tree{Root: root}
	t.Title = extractTitle(root)
	t.Lang = extractLang(root)
	return t, nil
}

func extractTitle(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "title" && n.FirstChild != nil {
		return strings.TrimSpace(n.FirstChild.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := extractTitle(c); t != "" {
			return t
		}
	}
	return ""
}

func extractLang(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "html" {
		for _, a := range n.Attr {
			if a.Key == "lang" {
				return a.Val
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if l := extractLang(c); l != "" {
			return l
		}
	}
	return ""
}
