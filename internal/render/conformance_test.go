package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fetchSchema is the machine-checked copy of the symfetch output schema.
type fetchSchema struct {
	FrontmatterKeys  []string `json:"frontmatter_keys"`
	JSONFields       []string `json:"json_fields"`
	BrowseExtensions struct {
		ContentFields []string `json:"content_fields"`
	} `json:"browse_extensions"`
}

func loadFetchSchema(t *testing.T) fetchSchema {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "fetch-schema.json"))
	if err != nil {
		t.Fatalf("read schema copy: %v", err)
	}
	var schema fetchSchema
	if err := json.Unmarshal(content, &schema); err != nil {
		t.Fatalf("decode schema copy: %v", err)
	}
	if len(schema.FrontmatterKeys) == 0 || len(schema.JSONFields) == 0 {
		t.Fatal("schema copy is empty")
	}
	return schema
}

// TestFrontmatterSchemaCopyComplete verifies that the schema copy itself
// contains the required frontmatter keys the issue mandates.
func TestFrontmatterSchemaCopyComplete(t *testing.T) {
	schema := loadFetchSchema(t)
	required := []string{"title", "url", "fetched_at", "lang", "schema_type"}
	for _, key := range required {
		if !contains(schema.FrontmatterKeys, key) {
			t.Fatalf("schema copy is missing required frontmatter key %q", key)
		}
	}
}

// TestEmittedFrontmatterKeysConform parses the generated frontmatter and
// checks every emitted key against the schema copy.
func TestEmittedFrontmatterKeysConform(t *testing.T) {
	schema := loadFetchSchema(t)
	allowed := make(map[string]bool, len(schema.FrontmatterKeys))
	for _, key := range schema.FrontmatterKeys {
		allowed[key] = true
	}
	document := Document{
		URL:        "https://example.com/",
		Title:      "Example",
		FetchedAt:  "2026-08-03T12:00:00Z",
		Lang:       "en",
		TokensEst:  42,
		SchemaType: "Article",
	}
	emitted := frontmatterKeys(t, Frontmatter(document))
	if len(emitted) == 0 {
		t.Fatal("frontmatter emitted no keys")
	}
	for _, key := range emitted {
		if !allowed[key] {
			t.Fatalf("frontmatter emits key %q which is not part of the symfetch schema", key)
		}
	}
}

// TestJSONFieldNamesConform verifies that the metadata field names of the
// read JSON document come from the symfetch schema (content carriers are the
// documented browse extensions).
func TestJSONFieldNamesConform(t *testing.T) {
	schema := loadFetchSchema(t)
	allowed := make(map[string]bool, len(schema.JSONFields))
	for _, field := range schema.JSONFields {
		allowed[field] = true
	}
	extensions := make(map[string]bool, len(schema.BrowseExtensions.ContentFields))
	for _, field := range schema.BrowseExtensions.ContentFields {
		extensions[field] = true
	}

	document := Document{
		URL:        "https://example.com/",
		Title:      "Example",
		Lang:       "en",
		FetchedAt:  "2026-08-03T12:00:00Z",
		TokensEst:  42,
		SchemaType: "Article",
		Markdown:   "# Example\n",
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		if allowed[name] || extensions[name] {
			continue
		}
		t.Fatalf("JSON document emits field %q which is neither in the symfetch schema nor a documented browse extension", name)
	}
}

func frontmatterKeys(t *testing.T, frontmatter string) []string {
	t.Helper()
	var keys []string
	for _, line := range splitLines(frontmatter) {
		line = trimSpace(line)
		if line == "---" || line == "" {
			continue
		}
		for idx, r := range line {
			if r == ':' {
				keys = append(keys, line[:idx])
				break
			}
		}
	}
	return keys
}

func splitLines(text string) []string {
	var lines []string
	start := 0
	for idx, r := range text {
		if r == '\n' {
			lines = append(lines, text[start:idx])
			start = idx + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

func trimSpace(text string) string {
	start, end := 0, len(text)
	for start < end && (text[start] == ' ' || text[start] == '\t') {
		start++
	}
	for end > start && (text[end-1] == ' ' || text[end-1] == '\t') {
		end--
	}
	return text[start:end]
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
