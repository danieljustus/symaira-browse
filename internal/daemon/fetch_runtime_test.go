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

// spaSkeletonServer serves a client-rendered shell: a large hydration payload,
// an empty framework root, and no visible text. A plain HTTP fetch cannot
// retrieve the real content, so the pipeline must emit an escalation hint.
func spaSkeletonServer(t *testing.T) *httptest.Server {
	t.Helper()
	filler := strings.Repeat(`{"k":"vvvvvvvvvvvvvvvvvvvvvvvvvvvvvv"},`, 400)
	body := `<!doctype html><html lang="en"><head><title>Delayed hydration SPA</title></head><body>` +
		`<div id="__next"></div>` +
		`<script id="__NEXT_DATA__" type="application/json">{"props":[` + filler + `{"z":1}]}</script>` +
		`</body></html>`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
}

// TestFetchURLEscalationHintJSON verifies the tier-0 -> tier-1 escalation
// contract (docs/tiers.md): a client-rendered shell reports escalate so an
// agent knows a plain fetch missed the content.
func TestFetchURLEscalationHintJSON(t *testing.T) {
	server := spaSkeletonServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "fetch.url",
		Args: marshalArgsForTest(map[string]any{"url": server.URL, "format": "json"}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	raw, _ := json.Marshal(data)
	var doc struct {
		Escalate *struct {
			Tool    string `json:"tool"`
			MCPTool string `json:"mcp_tool"`
			Reason  string `json:"reason"`
			Command string `json:"command"`
		} `json:"escalate"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal doc: %v (%s)", err, raw)
	}
	if doc.Escalate == nil {
		t.Fatalf("json response carries no escalate hint: %s", raw)
	}
	if doc.Escalate.Reason != "spa_skeleton" {
		t.Errorf("reason = %q, want spa_skeleton", doc.Escalate.Reason)
	}
	if doc.Escalate.MCPTool != "read" {
		t.Errorf("mcp_tool = %q, want read", doc.Escalate.MCPTool)
	}
	if want := "symbrowse read " + server.URL; doc.Escalate.Command != want {
		t.Errorf("command = %q, want %q", doc.Escalate.Command, want)
	}
}

// TestFetchURLEscalationHintMarkdown verifies the same hint reaches the
// markdown path, both as a structured sibling and inside the rendered header.
func TestFetchURLEscalationHintMarkdown(t *testing.T) {
	server := spaSkeletonServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "fetch.url",
		Args: marshalArgsForTest(map[string]any{"url": server.URL, "format": "markdown"}),
	})
	if err != nil {
		t.Fatalf("fetch.url: %v", err)
	}
	// Round-trip through JSON: that is what the daemon socket does, so the
	// test sees the same shape a client does.
	raw, _ := json.Marshal(data)
	var response struct {
		Markdown string `json:"markdown"`
		Escalate *struct {
			Reason  string `json:"reason"`
			MCPTool string `json:"mcp_tool"`
			Command string `json:"command"`
		} `json:"escalate"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, raw)
	}
	if response.Escalate == nil {
		t.Fatalf("markdown response carries no escalate sibling: %s", raw)
	}
	if response.Escalate.Reason != "spa_skeleton" {
		t.Errorf("reason = %q, want spa_skeleton", response.Escalate.Reason)
	}
	if response.Escalate.MCPTool != "read" {
		t.Errorf("mcp_tool = %q, want read", response.Escalate.MCPTool)
	}
	if !strings.Contains(response.Markdown, "spa_skeleton") {
		t.Errorf("markdown header lacks the escalation reason: %q", response.Markdown)
	}
	if want := "symbrowse read " + server.URL; !strings.Contains(response.Markdown, want) {
		t.Errorf("markdown header lacks the escalation command %q: %q", want, response.Markdown)
	}
}

// TestFetchURLNoEscalationOnContentPage verifies the hint stays absent for an
// ordinary server-rendered page: escalate is a signal, not decoration.
func TestFetchURLNoEscalationOnContentPage(t *testing.T) {
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
	raw, _ := json.Marshal(data)
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, raw)
	}
	if _, ok := response["escalate"]; ok {
		t.Errorf("content page reported an escalation hint: %s", raw)
	}
}

// TestFetchURLMarkdownUnchangedWithoutSignals verifies an ordinary page still
// renders without a metadata header: the header is a signal for escalation,
// truncation or a client-rendered page, not boilerplate on every response.
func TestFetchURLMarkdownUnchangedWithoutSignals(t *testing.T) {
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
	if strings.HasPrefix(md, "> ") {
		t.Errorf("content page rendered a metadata header: %q", md)
	}
}
