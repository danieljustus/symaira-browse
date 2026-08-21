package semantic

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

var (
	positivePattern = regexp.MustCompile(`(?i)(article|main|content|post|body|entry|text|story|blog|news)`)
	negativePattern = regexp.MustCompile(`(?i)(sidebar|footer|nav|menu|banner|ad-|advert|cookie|consent|share|related|comment|promo|widget|header-nav)`)
)

// BlockScore holds the content score for a DOM subtree.
type BlockScore struct {
	Node      *html.Node
	TextLen   int
	LinkLen   int
	Score     float64
	ClassBias float64 // +1 positive, -1 negative, 0 neutral
}

// ScoreBlocks scores all block-level containers and returns them sorted by score desc.
func ScoreBlocks(root *html.Node) []BlockScore {
	var blocks []BlockScore
	walkBlocks(root, &blocks)
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Score > blocks[j].Score
	})
	return blocks
}

// BestBlock returns the highest-scoring block, applying adaptive retry
// if extracted text is below charThreshold.
func BestBlock(root *html.Node, charThreshold int) *html.Node {
	blocks := ScoreBlocks(root)
	if len(blocks) == 0 {
		return root
	}

	// Check if best result has enough text
	if len(blocks) > 0 && blocks[0].TextLen >= charThreshold {
		return blocks[0].Node
	}

	// Adaptive retry: loosen constraints — rerun with relaxed scoring
	for i, b := range blocks {
		if b.TextLen >= charThreshold/2 {
			return blocks[i].Node
		}
	}

	// Full-body fallback
	return root
}

func walkBlocks(n *html.Node, out *[]BlockScore) {
	if n.Type != html.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkBlocks(c, out)
		}
		return
	}

	tag := strings.ToLower(n.Data)
	isBlock := tag == "div" || tag == "article" || tag == "section" ||
		tag == "main" || tag == "aside" || tag == "p" || tag == "table" ||
		tag == "ul" || tag == "ol" || tag == "dl" || tag == "blockquote"

	if isBlock {
		score := scoreBlock(n)
		if score.TextLen > 20 {
			*out = append(*out, score)
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkBlocks(c, out)
	}
}

func scoreBlock(n *html.Node) BlockScore {
	textLen := countText(n)
	linkLen := countLinkText(n)

	var linkDensity float64
	if textLen > 0 {
		linkDensity = float64(linkLen) / float64(textLen)
	}

	classBias := classWeight(n)

	// Base score: text × (1 - linkDensity) × classWeight
	var weight float64
	switch {
	case classBias > 0:
		weight = 1.5
	case classBias < 0:
		weight = 0.3
	default:
		weight = 1.0
	}

	score := float64(textLen) * (1.0 - linkDensity) * weight

	return BlockScore{
		Node:      n,
		TextLen:   textLen,
		LinkLen:   linkLen,
		Score:     score,
		ClassBias: classBias,
	}
}

func classWeight(n *html.Node) float64 {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "id") || strings.EqualFold(a.Key, "class") {
			v := strings.ToLower(a.Val)
			if positivePattern.MatchString(v) {
				return 1.0
			}
			if negativePattern.MatchString(v) {
				return -1.0
			}
		}
	}
	return 0
}

func countText(n *html.Node) int {
	if n.Type == html.TextNode {
		return utf8.RuneCountInString(strings.TrimSpace(n.Data))
	}
	total := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		total += countText(c)
	}
	return total
}

func countLinkText(n *html.Node) int {
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "a" {
		return countText(n)
	}
	total := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		total += countLinkText(c)
	}
	return total
}
