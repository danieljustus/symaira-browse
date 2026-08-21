package semantic

import (
	"encoding/json"
	"strings"

	"golang.org/x/net/html"
)

// DataIsland is a structured JSON payload extracted from the page.
type DataIsland struct {
	Source string          `json:"source"`
	JSON   json.RawMessage `json:"json"`
}

// ExtractIslands finds __NEXT_DATA__, ld+json and similar data islands
// embedded in <script> tags without executing JavaScript.
func ExtractIslands(root *html.Node, maxIslandBytes int) []DataIsland {
	var islands []DataIsland
	walkIslands(root, &islands, maxIslandBytes)
	return islands
}

func walkIslands(n *html.Node, out *[]DataIsland, maxBytes int) {
	if n.Type == html.ElementNode && strings.ToLower(n.Data) == "script" {
		src := attrVal(n, "type")
		id := attrVal(n, "id")

		isLdJSON := strings.EqualFold(src, "application/ld+json")
		isNextData := strings.EqualFold(id, "__NEXT_DATA__")
		isPreloaded := strings.Contains(strings.ToLower(id), "preloaded") ||
			strings.Contains(strings.ToLower(id), "initial-state")

		if isLdJSON || isNextData || isPreloaded {
			text := innerText(n)
			if text == "" {
				return
			}
			if len(text) > maxBytes {
				// Too large: emit a summary placeholder
				var topKeys []string
				var partial map[string]json.RawMessage
				if err := json.Unmarshal([]byte(text), &partial); err == nil {
					for k := range partial {
						topKeys = append(topKeys, k)
						if len(topKeys) >= 20 {
							break
						}
					}
				}
				summary := map[string]interface{}{
					"_truncated": true,
					"_size":      len(text),
					"_keys":      topKeys,
				}
				data, _ := json.Marshal(summary)
				source := sourceLabel(src, id)
				*out = append(*out, DataIsland{Source: source, JSON: data})
				return
			}

			var raw json.RawMessage
			if err := json.Unmarshal([]byte(text), &raw); err != nil {
				return // malformed JSON: skip
			}
			source := sourceLabel(src, id)
			*out = append(*out, DataIsland{Source: source, JSON: raw})
		}
		return // don't descend into scripts
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkIslands(c, out, maxBytes)
	}
}

func sourceLabel(scriptType, id string) string {
	if strings.EqualFold(scriptType, "application/ld+json") {
		return "ld+json"
	}
	if id != "" {
		return id
	}
	return "script"
}

func innerText(n *html.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return strings.TrimSpace(sb.String())
}
