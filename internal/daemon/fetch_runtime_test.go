package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fetchTestServer returns an httptest server serving a small fixture page.
func fetchTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/batch-a" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Batch A</title></head><body><h1>Alpha</h1><p>First page.</p></body></html>`))
			return
		}
		if r.URL.Path == "/batch-b" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Batch B</title></head><body><h1>Beta</h1><p>Second page.</p></body></html>`))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Mars Water Discovery</title></head><body><h1>Mars Water Discovery</h1><p>Scientists found water.</p></body></html>`))
	}))
}

func newTestFetchRuntime(t *testing.T) *FetchRuntime {
	t.Helper()
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowPrivate: true,
		Robots:       false,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// TestFetchURLJSONContract verifies fetch.url returns the SymFetch fetch_url
// JSON contract: url, final_url, title, lang, content with category/tag/text.
func TestFetchURLJSONContract(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":       server.URL,
			"format":    "json",
			"max_chars": 5000,
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	raw, _ := json.Marshal(data)
	var doc struct {
		URL      string `json:"url"`
		FinalURL string `json:"final_url"`
		Title    string `json:"title"`
		Lang     string `json:"lang"`
		Content  []struct {
			Category string `json:"category"`
			Tag      string `json:"tag"`
			Text     string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal doc: %v (%s)", err, raw)
	}
	if doc.URL != server.URL {
		t.Errorf("url = %q, want %q", doc.URL, server.URL)
	}
	if doc.Title != "Mars Water Discovery" {
		t.Errorf("title = %q, want Mars Water Discovery", doc.Title)
	}
	if doc.Lang != "en" {
		t.Errorf("lang = %q, want en", doc.Lang)
	}
	var found bool
	for _, el := range doc.Content {
		if strings.Contains(el.Text, "Scientists found water") {
			found = true
			if el.Category == "" {
				t.Errorf("content element missing category")
			}
		}
	}
	if !found {
		t.Errorf("content lacks the paragraph text: %s", raw)
	}
}

// TestFetchURLMarkdownContract verifies fetch.url markdown output shape.
func TestFetchURLMarkdownContract(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url":    server.URL,
			"format": "markdown",
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	m, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", data)
	}
	md, _ := m["markdown"].(string)
	if !strings.Contains(md, "Mars Water Discovery") {
		t.Errorf("markdown lacks title: %q", md)
	}
	if !strings.Contains(md, "Scientists found water") {
		t.Errorf("markdown lacks body: %q", md)
	}
}

// TestFetchURLRawContract verifies raw=true returns the decoded body.
func TestFetchURLRawContract(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.url",
		Args: marshalArgsForTest(map[string]any{
			"url": server.URL,
			"raw": true,
		}),
	})
	if err != nil {
		t.Fatalf("fetch.url raw: %v", err)
	}
	m, _ := data.(map[string]any)
	content, _ := m["content"].(string)
	if !strings.Contains(content, "Scientists found water") {
		t.Errorf("raw body lacks content: %q", content)
	}
}

// TestFetchBatchInputOrder verifies fetch.batch returns results in input
// order with ok=true per entry (SymFetch contract).
func TestFetchBatchInputOrder(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	urlA := server.URL + "/batch-b"
	urlB := server.URL + "/batch-a"
	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.batch",
		Args: marshalArgsForTest(map[string]any{
			"urls":   []string{urlA, urlB},
			"format": "markdown",
		}),
	})
	if err != nil {
		t.Fatalf("fetch.batch: %v", err)
	}
	raw, _ := json.Marshal(data)
	var entries []struct {
		URL     string `json:"url"`
		OK      bool   `json:"ok"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("unmarshal batch: %v (%s)", err, raw)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].URL != urlA || entries[1].URL != urlB {
		t.Errorf("input order broken: %+v", entries)
	}
	if !entries[0].OK || !entries[1].OK {
		t.Errorf("expected ok=true: %+v", entries)
	}
	if !strings.Contains(entries[0].Content, "Beta") || !strings.Contains(entries[1].Content, "Alpha") {
		t.Errorf("batch content mismatch: %+v", entries)
	}
}

// TestFetchBatchPartialFailure verifies one failing URL does not abort the
// batch and carries an error entry.
func TestFetchBatchPartialFailure(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.batch",
		Args: marshalArgsForTest(map[string]any{
			"urls": []string{server.URL, "http://127.0.0.1:1/does-not-exist"},
		}),
	})
	if err != nil {
		t.Fatalf("fetch.batch: %v", err)
	}
	raw, _ := json.Marshal(data)
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0]["ok"] != true {
		t.Errorf("first entry should succeed: %v", entries[0])
	}
	if entries[1]["ok"] == true {
		t.Errorf("second entry should fail: %v", entries[1])
	}
	if _, hasErr := entries[1]["error"]; !hasErr {
		t.Errorf("failed entry missing error: %v", entries[1])
	}
}

// TestFetchURLRejectsMissingURL verifies the frame validates its payload.
func TestFetchURLRejectsMissingURL(t *testing.T) {
	runtime := newTestFetchRuntime(t)
	_, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "fetch.url",
		Args: marshalArgsForTest(map[string]any{}),
	})
	if err == nil {
		t.Fatal("expected error for missing url")
	}
}
