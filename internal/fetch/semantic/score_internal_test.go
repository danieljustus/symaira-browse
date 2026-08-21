package semantic

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestClassWeight_Positive(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"id-article", `<div id="article">Content</div>`},
		{"class-content", `<div class="content">Content</div>`},
		{"class-main", `<div class="main">Content</div>`},
		{"class-post", `<div class="post">Content</div>`},
		{"class-body-text", `<div class="body-text">Content</div>`},
		{"id-story", `<div id="story">Content</div>`},
		{"class-news", `<div class="news">Content</div>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, _ := html.Parse(strings.NewReader(tt.html))
			node := findFirstNode(doc, "div")
			if node == nil {
				t.Fatal("node not found")
			}
			got := classWeight(node)
			if got != 1.0 {
				t.Errorf("classWeight() = %v, want 1.0", got)
			}
		})
	}
}

func TestClassWeight_Negative(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"id-sidebar", `<div id="sidebar">Content</div>`},
		{"class-footer", `<div class="footer">Content</div>`},
		{"class-nav", `<div class="nav">Content</div>`},
		{"class-menu", `<div class="menu">Content</div>`},
		{"class-banner", `<div class="banner">Content</div>`},
		{"class-ad-slot", `<div class="ad-slot">Content</div>`},
		{"class-cookie", `<div class="cookie">Content</div>`},
		{"class-comment", `<div class="comment">Content</div>`},
		{"class-widget", `<div class="widget">Content</div>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, _ := html.Parse(strings.NewReader(tt.html))
			node := findFirstNode(doc, "div")
			if node == nil {
				t.Fatal("node not found")
			}
			got := classWeight(node)
			if got != -1.0 {
				t.Errorf("classWeight() = %v, want -1.0", got)
			}
		})
	}
}

func TestClassWeight_Neutral(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"no-attrs", `<div>Content</div>`},
		{"unrelated-class", `<div class="wrapper">Content</div>`},
		{"unrelated-id", `<div id="container">Content</div>`},
		{"empty-class", `<div class="">Content</div>`},
		{"empty-id", `<div id="">Content</div>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, _ := html.Parse(strings.NewReader(tt.html))
			node := findFirstNode(doc, "div")
			if node == nil {
				t.Fatal("node not found")
			}
			got := classWeight(node)
			if got != 0 {
				t.Errorf("classWeight() = %v, want 0", got)
			}
		})
	}
}

func findFirstNode(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && strings.EqualFold(n.Data, tag) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirstNode(c, tag); found != nil {
			return found
		}
	}
	return nil
}
