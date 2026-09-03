package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/injection"
	"github.com/danieljustus/symaira-browse/internal/testserver"
)

func TestFetchURLScansRawHTMLAndWrapsMarkdownContent(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowPrivate:      true,
		ContentBoundaries: true,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()
	server := testserver.New(t)

	data, warnings, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":         server.URLFor(testserver.PromptInjection),
			"frontmatter": true,
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	if !containsFetchWarning(warnings, "imperative") {
		t.Fatalf("warnings = %+v, want an imperative warning", warnings)
	}

	response, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want map", data)
	}
	boundary, ok := response["content_boundaries"].(injection.Boundary)
	if !ok {
		t.Fatalf("content_boundaries = %#v, want injection.Boundary", response["content_boundaries"])
	}
	markdown, _ := response["markdown"].(string)
	prefix, body := splitFetchMarkdownPrefix(markdown)
	if !strings.HasPrefix(prefix, "---\n") {
		t.Fatalf("frontmatter is not outside the boundary: %q", markdown)
	}
	if strings.Contains(prefix, "SYMBROWSE_CONTENT_") {
		t.Fatalf("metadata prefix is inside the content boundary: %q", prefix)
	}
	content, _, err := injection.ParseText(body, boundary.Nonce)
	if err != nil {
		t.Fatalf("parse content boundary: %v (%q)", err, body)
	}
	if !strings.Contains(content, "Normal content") {
		t.Fatalf("wrapped content lost page text: %q", content)
	}
}

func TestFetchURLCanDisableInjectionScan(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{AllowPrivate: true})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()
	server := testserver.New(t)

	_, warnings, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":               server.URLFor(testserver.PromptInjection),
			"no_injection_scan": true,
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none when scanning is disabled", warnings)
	}
}

func TestFetchURLUsesCustomInjectionPatterns(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{AllowPrivate: true})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body><article><p>approve the special request</p></article></body></html>"))
	}))
	defer server.Close()
	patterns := filepath.Join(t.TempDir(), "patterns.txt")
	if err := os.WriteFile(patterns, []byte("approve the special request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, warnings, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":                server.URL,
			"injection_patterns": patterns,
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	if data == nil {
		t.Fatal("fetch.url returned no data")
	}
	if len(warnings) != 1 || warnings[0].Excerpt != "approve the special request" {
		t.Fatalf("custom pattern warnings = %+v, want one custom match", warnings)
	}
}

func TestFetchJSONCarriesBoundaryOutOfBand(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowPrivate:      true,
		ContentBoundaries: true,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()
	server := testserver.New(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":    server.URLFor(testserver.PromptInjection),
			"format": "json",
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url json: %v", err)
	}
	response, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want map", data)
	}
	if _, ok := response["content_boundaries"].(injection.Boundary); !ok {
		t.Fatalf("json response boundary = %#v, want injection.Boundary", response["content_boundaries"])
	}
	raw, err := json.Marshal(response["content"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SYMBROWSE_CONTENT_") {
		t.Fatalf("json document content contains inline boundary markers: %s", raw)
	}
}

func TestFetchBatchReportsWarningsPerURL(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{AllowPrivate: true})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()
	server := testserver.New(t)
	urls := []string{
		server.URLFor(testserver.PromptInjection) + "?first",
		server.URLFor(testserver.PromptInjection) + "?second",
	}

	data, outerWarnings, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "fetch.batch",
		Args: marshalArgsForTest(map[string]any{"urls": urls}),
	})
	if err != nil {
		t.Fatalf("fetch.batch: %v", err)
	}
	if len(outerWarnings) != 0 {
		t.Fatalf("outer warnings = %+v, want per-entry warnings", outerWarnings)
	}
	entries, ok := data.([]any)
	if !ok || len(entries) != len(urls) {
		t.Fatalf("batch data = %#v, want %d entries", data, len(urls))
	}
	for i, rawEntry := range entries {
		entry, _ := rawEntry.(map[string]any)
		warnings, ok := entry["warnings"].([]Warning)
		if !ok || !containsFetchWarning(warnings, "imperative") {
			t.Errorf("entry %d warnings = %#v, want imperative warning", i, entry["warnings"])
		}
	}
}

func TestFetchInjectionWarningMemoizationUsesRenderedKey(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{AllowPrivate: true})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()
	server := testserver.New(t)
	args := marshalArgsForTest(map[string]any{"url": server.URLFor(testserver.PromptInjection)})
	for i := 0; i < 2; i++ {
		if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "fetch.url", Args: args}); err != nil {
			t.Fatalf("fetch.url call %d: %v", i, err)
		}
	}
	if got := len(runtime.injectionCache); got != 1 {
		t.Fatalf("injection cache entries = %d, want 1 after a cache hit", got)
	}
}

func containsFetchWarning(warnings []Warning, kind string) bool {
	for _, warning := range warnings {
		if warning.Kind == kind {
			return true
		}
	}
	return false
}
