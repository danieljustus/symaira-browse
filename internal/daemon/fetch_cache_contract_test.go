package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
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

func TestFetchRuntimeUsesConfiguredUserAgentForRobotsAndFetch(t *testing.T) {
	const userAgent = "daniel-fetch-test/1.0"
	var robotsUserAgent, pageUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			robotsUserAgent = r.Header.Get("User-Agent")
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n\nUser-agent: daniel-fetch-test\nAllow: /\n"))
		case "/page":
			pageUserAgent = r.Header.Get("User-Agent")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body><article><h1>Configured agent</h1><p>" + strings.Repeat("content ", 100) + "</p></article></body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowPrivate: true,
		Robots:       true,
		UserAgent:    userAgent,
		NoCache:      true,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	_, _, err = runtime.Handle(context.Background(), Frame{
		Cmd:  "fetch.url",
		Args: marshalArgsForTest(map[string]any{"url": server.URL + "/page"}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	if robotsUserAgent != userAgent || pageUserAgent != userAgent {
		t.Fatalf("user agents = robots %q, page %q; want %q for both", robotsUserAgent, pageUserAgent, userAgent)
	}
}

func TestFetchRuntimeNoCacheForcesFreshFetch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><article><h1>Fresh page</h1><p>" + strings.Repeat("fresh ", 120) + "</p></article></body></html>"))
	}))
	defer server.Close()

	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowPrivate: true,
		CacheDir:     t.TempDir(),
		CacheTTL:     time.Hour,
		NoCache:      true,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	for range 2 {
		if _, _, err := runtime.Handle(context.Background(), Frame{
			Cmd:  "fetch.url",
			Args: marshalArgsForTest(map[string]any{"url": server.URL}),
		}); err != nil {
			t.Fatalf("fetch.url: %v", err)
		}
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("network requests = %d, want 2 with no_cache", got)
	}
}

func TestFetchURLPipelineFieldsForwardTopKAndNoCache(t *testing.T) {
	noCache := true
	args := fetchURLArgs{Query: "pricing", TopK: 3, NoCache: &noCache}
	fields := args.pipelineFields()
	if fields.TopK != 3 || fields.NoCache == nil || !*fields.NoCache {
		t.Fatalf("pipeline fields = %+v, want top_k=3 and no_cache=true", fields)
	}

	runtime := &FetchRuntime{}
	options := runtime.pipelineOptions(fields, pipeline.FormatMarkdown)
	if options.TopK != 3 || !options.Cache.NoCache {
		t.Fatalf("pipeline options = %+v, want top_k=3 and no_cache=true", options)
	}
}
