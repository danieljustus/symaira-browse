package render

import (
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// frontmatter mirrors symfetch's frontmatter keys exactly
// (symaira-fetch internal/render/frontmatter.go). Fields that browse never
// sets (source, snapshot_at) stay omitted.
type frontmatter struct {
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

// Frontmatter renders the YAML metadata block with the exact key set and
// ordering symfetch uses, followed by a blank line.
func Frontmatter(document Document) string {
	fm := frontmatter{
		Title:      document.Title,
		URL:        document.URL,
		FinalURL:   document.FinalURL,
		FetchedAt:  document.FetchedAt,
		Lang:       document.Lang,
		TokensEst:  document.TokensEst,
		SchemaType: document.SchemaType,
	}
	var builder strings.Builder
	builder.WriteString("---\n")
	encoder := yaml.NewEncoder(&builder)
	encoder.SetIndent(2)
	if err := encoder.Encode(fm); err != nil {
		return ""
	}
	_ = encoder.Close()
	builder.WriteString("---\n\n")
	return builder.String()
}

// FetchedAtNow returns the current UTC timestamp in the RFC3339 format
// symfetch uses for fetched_at.
func FetchedAtNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}
