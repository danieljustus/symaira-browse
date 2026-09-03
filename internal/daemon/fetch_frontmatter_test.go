package daemon

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestFetchURLFrontmatterEmitted verifies the frontmatter option produces the
// documented YAML block. It used to affect nothing but the cache key, which
// meant fetch_url could not emit the schema docs/output-schema.md promises
// both tiers share.
func TestFetchURLFrontmatterEmitted(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":         server.URL,
			"format":      "markdown",
			"frontmatter": true,
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	m, _ := data.(map[string]any)
	md, _ := m["markdown"].(string)

	keys, body := parseFrontmatter(t, md)
	for _, want := range []string{"title", "url", "fetched_at", "lang", "tokens_est"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("frontmatter is missing key %q: %q", want, md)
		}
	}
	if keys["title"] != "Mars Water Discovery" {
		t.Errorf("frontmatter title = %v, want Mars Water Discovery", keys["title"])
	}
	if keys["url"] != server.URL {
		t.Errorf("frontmatter url = %v, want %s", keys["url"], server.URL)
	}
	if !strings.Contains(body, "Scientists found water") {
		t.Errorf("body was lost behind the frontmatter: %q", md)
	}
}

// TestFetchURLWithoutFrontmatterUnchanged verifies the option stays opt-in.
func TestFetchURLWithoutFrontmatterUnchanged(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "fetch.url",
		Args: marshalArgsForTest(map[string]any{"url": server.URL, "format": "markdown"}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	m, _ := data.(map[string]any)
	md, _ := m["markdown"].(string)
	if strings.HasPrefix(md, "---\n") {
		t.Errorf("frontmatter was emitted without being asked for: %q", md)
	}
}

// TestFetchURLFrontmatterWithinBudget verifies the frontmatter counts against
// max_chars rather than being added on top of it.
func TestFetchURLFrontmatterWithinBudget(t *testing.T) {
	server := longArticleServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	const budget = 4000
	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":         server.URL,
			"format":      "markdown",
			"frontmatter": true,
			"max_chars":   budget,
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	m, _ := data.(map[string]any)
	md, _ := m["markdown"].(string)
	if got := len([]rune(md)); got > budget {
		t.Errorf("frontmatter response is %d runes, exceeds the budget of %d", got, budget)
	}
	if !strings.HasPrefix(md, "---\n") {
		t.Errorf("frontmatter missing from a budgeted response: %q", md[:min(200, len(md))])
	}
}

// parseFrontmatter splits a markdown document into its YAML frontmatter keys
// and the remaining body.
func parseFrontmatter(t *testing.T, document string) (map[string]any, string) {
	t.Helper()
	if !strings.HasPrefix(document, "---\n") {
		t.Fatalf("document has no frontmatter block: %q", document)
	}
	end := strings.Index(document[4:], "\n---\n")
	if end < 0 {
		t.Fatalf("frontmatter block is not closed: %q", document)
	}
	block := document[4 : 4+end]
	var keys map[string]any
	if err := yaml.Unmarshal([]byte(block), &keys); err != nil {
		t.Fatalf("frontmatter is not valid YAML: %v (%q)", err, block)
	}
	return keys, document[4+end+5:]
}
