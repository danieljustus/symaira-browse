package render

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
	"github.com/danieljustus/symaira-browse/internal/fetch/archive"
)

type frontmatterData struct {
	Title      string `yaml:"title,omitempty"`
	URL        string `yaml:"url"`
	FinalURL   string `yaml:"final_url,omitempty"`
	FetchedAt  string `yaml:"fetched_at"`
	Lang       string `yaml:"lang,omitempty"`
	TokensEst  int    `yaml:"tokens_est"`
	SchemaType string `yaml:"schema_type,omitempty"`
	Source     string `yaml:"source,omitempty"`
	SnapshotAt string `yaml:"snapshot_at,omitempty"`
}

func GenerateFrontmatter(meta agentdom.Meta, doc *agentdom.Document) string {
	if doc == nil {
		return ""
	}
	fm := frontmatterData{
		Title:     meta.Title,
		URL:       doc.URL,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Lang:      meta.Lang,
		TokensEst: meta.EstTokens,
	}

	if meta.FinalURL != "" && meta.FinalURL != doc.URL {
		fm.FinalURL = meta.FinalURL
	}

	if doc.Islands != nil {
		for _, island := range doc.Islands {
			if island.Source == "ld+json" {
				schemaType := extractSchemaType(island.JSON)
				if schemaType != "" {
					fm.SchemaType = schemaType
					break
				}
			}
		}
	}

	if archiveURL, ok := archive.ParseWaybackURL(doc.URL); ok {
		fm.Source = "wayback"
		fm.URL = archiveURL
		if ts := extractWaybackTimestamp(doc.URL); ts != "" {
			fm.SnapshotAt = ts
		}
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	encoder := yaml.NewEncoder(&sb)
	encoder.SetIndent(2)
	if err := encoder.Encode(fm); err != nil {
		return ""
	}
	encoder.Close()
	sb.WriteString("---\n\n")
	return sb.String()
}

func extractSchemaType(jsonData []byte) string {
	var raw map[string]interface{}
	if err := yaml.Unmarshal(jsonData, &raw); err != nil {
		return ""
	}

	if t, ok := raw["@type"].(string); ok {
		return t
	}
	if t, ok := raw["type"].(string); ok {
		return t
	}

	if itemList, ok := raw["@graph"].([]interface{}); ok {
		if len(itemList) > 0 {
			if first, ok := itemList[0].(map[string]interface{}); ok {
				if t, ok := first["@type"].(string); ok {
					return t
				}
			}
		}
	}

	return ""
}

func extractWaybackTimestamp(rawURL string) string {
	const prefix = "https://web.archive.org/web/"
	if !strings.HasPrefix(rawURL, prefix) {
		return ""
	}
	remainder := rawURL[len(prefix):]
	parts := strings.SplitN(remainder, "/", 2)
	if len(parts) < 1 {
		return ""
	}
	ts := parts[0]
	if ts == "*" {
		return ""
	}
	return ts
}
