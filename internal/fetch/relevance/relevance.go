// Package relevance provides BM25-based query relevance filtering for
// classified content blocks. It splits rendered Markdown into sections,
// scores each section against a query, and returns only the most relevant
// sections while preserving structure and headings.
package relevance

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Section represents a content section extracted from Markdown.
type Section struct {
	Heading string // heading text (empty for implicit first section)
	Text    string // body text content
	Raw     string // raw Markdown including heading prefix
	Score   float64
}

// Tokenize performs simple whitespace + lowercase tokenization.
// Punctuation is stripped from token boundaries.
func Tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	var current strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		} else {
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// BM25 computes BM25 scores for a query against a list of document strings.
// Returns a score per document. Higher scores indicate greater relevance.
//
// Uses standard BM25 parameters: k1=1.5, b=0.75.
func BM25(query string, docs []string) []float64 {
	if query == "" || len(docs) == 0 {
		scores := make([]float64, len(docs))
		return scores
	}

	queryTokens := Tokenize(query)
	if len(queryTokens) == 0 {
		return make([]float64, len(docs))
	}

	// Tokenize all documents.
	docTokens := make([][]string, len(docs))
	docLens := make([]float64, len(docs))
	var avgDocLen float64
	for i, doc := range docs {
		docTokens[i] = Tokenize(doc)
		docLens[i] = float64(len(docTokens[i]))
		avgDocLen += docLens[i]
	}
	avgDocLen /= float64(len(docs))
	if avgDocLen == 0 {
		avgDocLen = 1
	}

	// IDF: number of documents containing each query term.
	n := float64(len(docs))
	idf := make(map[string]float64)
	for _, qt := range queryTokens {
		df := 0.0
		for _, dt := range docTokens {
			for _, t := range dt {
				if t == qt {
					df++
					break
				}
			}
		}
		// BM25 IDF formula with +1 floor to avoid negative values.
		idf[qt] = math.Log((n-df+0.5)/(df+0.5) + 1.0)
	}

	// Score each document.
	scores := make([]float64, len(docs))
	const k1 = 1.5
	const b = 0.75

	for i, dt := range docTokens {
		tf := make(map[string]float64)
		for _, t := range dt {
			tf[t]++
		}
		var score float64
		for _, qt := range queryTokens {
			tfVal := tf[qt]
			if tfVal == 0 {
				continue
			}
			numerator := tfVal * (k1 + 1)
			denominator := tfVal + k1*(1-b+b*docLens[i]/avgDocLen)
			score += idf[qt] * numerator / denominator
		}
		scores[i] = score
	}

	return scores
}

// RankSections scores sections against a query and returns them sorted
// by relevance. If topK > 0, only the top K sections are returned.
func RankSections(query string, sections []Section, topK int) []Section {
	if len(sections) == 0 {
		return sections
	}

	// Build document texts for BM25 scoring.
	docs := make([]string, len(sections))
	for i, s := range sections {
		docs[i] = s.Heading + " " + s.Text
	}

	scores := BM25(query, docs)

	// Assign scores and sort.
	ranked := make([]Section, len(sections))
	copy(ranked, sections)
	for i := range ranked {
		ranked[i].Score = scores[i]
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	if topK > 0 && topK < len(ranked) {
		ranked = ranked[:topK]
	}

	return ranked
}

// SplitMarkdownSections splits Markdown content into sections by headings.
// Each section gets its heading text and body content.
func SplitMarkdownSections(md string) []Section {
	if md == "" {
		return []Section{{Text: ""}}
	}

	lines := strings.Split(md, "\n")
	var sections []Section
	var currentHeading string
	var currentBody strings.Builder
	var currentRaw strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// Save previous section.
			body := strings.TrimSpace(currentBody.String())
			raw := strings.TrimSpace(currentRaw.String())
			if raw != "" || currentHeading != "" {
				sections = append(sections, Section{
					Heading: currentHeading,
					Text:    body,
					Raw:     raw,
				})
			}
			// Parse heading level and text.
			level := 0
			for _, r := range trimmed {
				if r == '#' {
					level++
				} else {
					break
				}
			}
			currentHeading = strings.TrimSpace(trimmed[level:])
			currentBody.Reset()
			currentRaw.Reset()
			currentRaw.WriteString(line)
			currentRaw.WriteString("\n")
		} else {
			if currentBody.Len() > 0 && trimmed != "" {
				currentBody.WriteString("\n")
			}
			currentBody.WriteString(trimmed)
			if currentRaw.Len() > 0 || trimmed != "" {
				currentRaw.WriteString(line)
				currentRaw.WriteString("\n")
			}
		}
	}

	// Save last section.
	body := strings.TrimSpace(currentBody.String())
	raw := strings.TrimSpace(currentRaw.String())
	sections = append(sections, Section{
		Heading: currentHeading,
		Text:    body,
		Raw:     raw,
	})

	return sections
}

// ReassembleMarkdown joins sections back into Markdown, adding omission
// markers for sections that were filtered out. totalSections is the
// original count before filtering; includedSections is how many were kept.
func ReassembleMarkdown(sections []Section, totalSections, includedSections int) string {
	if len(sections) == 0 {
		return ""
	}

	var sb strings.Builder
	omitted := totalSections - includedSections
	omissionAdded := false

	for i, s := range sections {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		if s.Heading != "" {
			sb.WriteString("## ")
			sb.WriteString(s.Heading)
			sb.WriteString("\n\n")
		}
		sb.WriteString(s.Text)

		// Add omission marker after the last included section if there are omissions.
		if omitted > 0 && !omissionAdded && i == len(sections)-1 {
			sb.WriteString("\n\n<!-- ... ")
			sb.WriteString(strings.TrimSpace(
				formatOmissionNote(omitted, totalSections),
			))
			sb.WriteString(" -->")
			omissionAdded = true
		}
	}

	return sb.String()
}

func formatOmissionNote(omitted, _ int) string {
	return fmt.Sprintf("%d section%s omitted for relevance", omitted, pluralSuffix(omitted))
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// FilterJSONContent filters a slice of content items by BM25 relevance.
// The textFn extracts the searchable text from each item.
// If topK > 0, only the top K items are returned.
// Returns items in their original order after filtering.
func FilterJSONContent[T any](query string, items []T, textFn func(T) string, topK int) []T {
	if query == "" || len(items) == 0 {
		return items
	}

	docs := make([]string, len(items))
	for i, item := range items {
		docs[i] = textFn(item)
	}

	scores := BM25(query, docs)

	// Create index-score pairs and sort by score descending.
	type scored struct {
		index int
		score float64
	}
	scoredItems := make([]scored, len(items))
	for i := range items {
		scoredItems[i] = scored{index: i, score: scores[i]}
	}
	sort.SliceStable(scoredItems, func(i, j int) bool {
		return scoredItems[i].score > scoredItems[j].score
	})

	// Filter by top-k.
	limit := len(items)
	if topK > 0 && topK < limit {
		limit = topK
	}

	// Collect indices to keep, sorted by original order.
	keep := make([]int, 0, limit)
	for i := 0; i < limit; i++ {
		keep = append(keep, scoredItems[i].index)
	}

	// Sort kept indices by original position.
	sort.Ints(keep)

	result := make([]T, len(keep))
	for i, idx := range keep {
		result[i] = items[idx]
	}

	return result
}
