package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchStoreFullTextUsesUnifiedCacheAndMCPHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body><article><h1>Long article</h1><p>" + strings.Repeat("full text ", 400) + "</p></article></body></html>"))
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowPrivate:   true,
		CacheTTL:       time.Hour,
		OutputCacheDir: cacheDir,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":               server.URL,
			"max_chars":         100000,
			"char_limit":        100,
			"store_full_text":   true,
			"retrieval_surface": "mcp",
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	response, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want map", data)
	}
	cacheID, _ := response["cache_id"].(string)
	if cacheID == "" {
		t.Fatalf("response = %#v, want cache_id", response)
	}
	hint, _ := response["cache_hint"].(string)
	if !strings.Contains(hint, "cache_get") || strings.Contains(hint, "symfetch") {
		t.Fatalf("cache hint = %q, want MCP cache_get route", hint)
	}
	markdown, _ := response["markdown"].(string)
	if strings.Contains(markdown, "symfetch") || strings.Contains(markdown, "/.cache/") {
		t.Fatalf("stored output leaked a legacy filesystem hint: %q", markdown)
	}

	cacheRuntime := NewCacheRuntime(cacheDir, time.Hour)
	full, _, err := cacheRuntime.Handle(context.Background(), Frame{
		Cmd:  "cache.get",
		Args: marshalArgsForTest(map[string]any{"cache_id": cacheID}),
	})
	if err != nil {
		t.Fatalf("cache.get: %v", err)
	}
	fullResponse := full.(map[string]any)
	if !strings.Contains(fullResponse["content"].(string), "full text") {
		t.Fatalf("full cache response = %#v", fullResponse)
	}
}
