package mcp

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
)

// TestFetchToolsListed verifies the three SymFetch compatibility tools are
// registered in the core profile with their contract schemas (issue #258).
func TestFetchToolsListed(t *testing.T) {
	names, err := SelectTools(string(ProfileCore))
	if err != nil {
		t.Fatalf("SelectTools(core): %v", err)
	}
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	for _, want := range []string{"fetch_url", "fetch_batch", "wayback_snapshots"} {
		if !seen[want] {
			t.Errorf("core profile is missing tool %q", want)
		}
	}
	// Contract: the tool schemas carry the SymFetch argument names.
	byName := map[string]ProxyTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	fetchURL := byName["fetch_url"]
	if fetchURL.Name == "" {
		t.Fatal("fetch_url not in tool table")
	}
	if fetchURL.Cmd != "fetch.url" {
		t.Errorf("fetch_url cmd = %q, want fetch.url", fetchURL.Cmd)
	}
	props, _ := fetchURL.Schema["properties"].(map[string]any)
	for _, want := range []string{"url", "format", "max_chars", "css_selector", "frontmatter", "include_links", "query", "raw", "schema_path"} {
		if _, ok := props[want]; !ok {
			t.Errorf("fetch_url schema missing property %q", want)
		}
	}
	batch := byName["fetch_batch"]
	props, _ = batch.Schema["properties"].(map[string]any)
	if _, ok := props["urls"]; !ok {
		t.Errorf("fetch_batch schema missing property urls")
	}
	wayback := byName["wayback_snapshots"]
	props, _ = wayback.Schema["properties"].(map[string]any)
	for _, want := range []string{"url", "from", "to", "limit", "match_type"} {
		if _, ok := props[want]; !ok {
			t.Errorf("wayback_snapshots schema missing property %q", want)
		}
	}
}

// TestFetchURLArgsCarryURL verifies the fetch_url tool maps its url input
// onto the daemon frame (regression: buildFetchArgs must forward url).
func TestFetchURLArgsCarryURL(t *testing.T) {
	byName := map[string]ProxyTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	args, err := byName["fetch_url"].Args(map[string]any{
		"url":       "https://example.com",
		"format":    "json",
		"max_chars": float64(3000),
	})
	if err != nil {
		t.Fatalf("fetch_url Args: %v", err)
	}
	m, ok := args.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", args)
	}
	if m["url"] != "https://example.com" {
		t.Errorf("url = %v, want https://example.com", m["url"])
	}
	if m["format"] != "json" {
		t.Errorf("format = %v, want json", m["format"])
	}
	if m["max_chars"] != 3000 {
		t.Errorf("max_chars = %v, want 3000", m["max_chars"])
	}
}

// TestFetchBatchArgsCarryURLs verifies fetch_batch maps its urls array onto
// the daemon frame.
func TestFetchBatchArgsCarryURLs(t *testing.T) {
	byName := map[string]ProxyTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	args, err := byName["fetch_batch"].Args(map[string]any{
		"urls": []any{"https://a.example", "https://b.example"},
	})
	if err != nil {
		t.Fatalf("fetch_batch Args: %v", err)
	}
	m, ok := args.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", args)
	}
	urls, ok := m["urls"].([]string)
	if !ok || len(urls) != 2 || urls[0] != "https://a.example" || urls[1] != "https://b.example" {
		t.Errorf("urls = %#v, want the two input URLs", m["urls"])
	}
}

// startFetchFakeDaemon runs a daemon whose handler answers the fetch frames
// with the contract-shaped payloads (no real network).
func startFetchFakeDaemon(t *testing.T, base, session string) {
	t.Helper()
	path, err := daemon.SocketPathIn(base, session)
	if err != nil {
		t.Fatal(err)
	}
	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{})
	cacheDir := filepath.Join(base, session, "cache-out")
	server := daemon.NewServer(daemon.Options{
		SocketPath:    path,
		Session:       session,
		Registry:      registry,
		CacheDir:      cacheDir,
		CacheTTL:      time.Hour,
		PeerValidator: func(net.Conn) error { return nil },
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			switch frame.Cmd {
			case "fetch.url":
				return map[string]any{
					"url":       "https://example.com",
					"final_url": "https://example.com",
					"title":     "Example Domain",
					"lang":      "en",
					"content":   []any{map[string]any{"category": "text", "tag": "h1", "text": "Example Domain"}},
				}, nil, nil
			case "fetch.batch":
				return []any{
					map[string]any{"url": "https://a.example", "ok": true, "content": "# A"},
					map[string]any{"url": "https://b.example", "ok": true, "content": "# B"},
				}, nil, nil
			case "wayback.snapshots":
				return []any{
					map[string]any{"timestamp": "20020120142510", "url": "http://example.com:80/", "status": "200", "mime_type": "text/html", "digest": "ABC"},
				}, nil, nil
			default:
				return nil, nil, daemon.NewError(daemon.ErrorUnknownCommand, "not implemented in fake fetch daemon: "+frame.Cmd)
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("fake fetch daemon did not shut down")
		}
	})
	t.Cleanup(cancel)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake fetch daemon socket did not appear")
}

// TestFetchToolsCallContract drives the MCP server end-to-end against the
// fake daemon and verifies each fetch tool resolves to the expected frame
// command and returns the contract-shaped payload.
func TestFetchToolsCallContract(t *testing.T) {
	base := socketBase(t)
	const session = "default"
	startFetchFakeDaemon(t, base, session)

	server, err := New(Options{
		Version:    "test",
		Session:    session,
		Profiles:   string(ProfileCore),
		SocketPath: func(s string) (string, error) { return daemon.SocketPathIn(base, s) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	send := serve(t, server)

	initialize := send(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})
	if initialize["error"] != nil {
		t.Fatalf("initialize error: %v", initialize["error"])
	}

	// tools/list includes the three fetch tools.
	toolsResponse := send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	result, _ := toolsResponse["result"].(map[string]any)
	toolList, _ := result["tools"].([]any)
	found := map[string]bool{}
	for _, item := range toolList {
		m, _ := item.(map[string]any)
		if name, ok := m["name"].(string); ok {
			found[name] = true
		}
	}
	for _, want := range []string{"fetch_url", "fetch_batch", "wayback_snapshots"} {
		if !found[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}

	// fetch_url call resolves to fetch.url and returns the JSON contract.
	fetchCall := send(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "fetch_url",
			"arguments": map[string]any{"url": "https://example.com", "format": "json"},
		},
	})
	if fetchCall["error"] != nil {
		t.Fatalf("fetch_url call error: %v", fetchCall["error"])
	}
	text := resultText(t, fetchCall)
	for _, want := range []string{"Example Domain", "example.com"} {
		if !strings.Contains(text, want) {
			t.Errorf("fetch_url result missing %q: %s", want, text)
		}
	}

	// fetch_batch call resolves to fetch.batch.
	batchCall := send(map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{
			"name":      "fetch_batch",
			"arguments": map[string]any{"urls": []string{"https://a.example", "https://b.example"}},
		},
	})
	if batchCall["error"] != nil {
		t.Fatalf("fetch_batch call error: %v", batchCall["error"])
	}
	text = resultText(t, batchCall)
	if !strings.Contains(text, "https://a.example") || !strings.Contains(text, "https://b.example") {
		t.Errorf("fetch_batch result missing urls: %s", text)
	}

	// wayback_snapshots call resolves to wayback.snapshots.
	waybackCall := send(map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{
			"name":      "wayback_snapshots",
			"arguments": map[string]any{"url": "http://example.com"},
		},
	})
	if waybackCall["error"] != nil {
		t.Fatalf("wayback_snapshots call error: %v", waybackCall["error"])
	}
	text = resultText(t, waybackCall)
	for _, want := range []string{"20020120142510", "text/html"} {
		if !strings.Contains(text, want) {
			t.Errorf("wayback_snapshots result missing %q: %s", want, text)
		}
	}
}

func resultText(t *testing.T, response map[string]any) string {
	t.Helper()
	result, _ := response["result"].(map[string]any)
	content, _ := result["content"].([]any)
	var builder strings.Builder
	for _, block := range content {
		if m, ok := block.(map[string]any); ok {
			if text, ok := m["text"].(string); ok {
				builder.WriteString(text)
			}
		}
	}
	// Fall back to the raw result when content is not text-based.
	if builder.Len() == 0 {
		raw, _ := json.Marshal(result)
		builder.Write(raw)
	}
	return builder.String()
}

// TestEscalationMCPToolIsRegistered verifies the MCP tool named by a tier-0
// escalation hint is actually exposed on the tool surface. The hint tells an
// agent which tool to call next; naming a tool that is not registered would
// leave an MCP client with no way to act on it (docs/tiers.md).
func TestEscalationMCPToolIsRegistered(t *testing.T) {
	names, err := SelectTools(string(ProfileCore))
	if err != nil {
		t.Fatalf("SelectTools(core): %v", err)
	}
	for _, name := range names {
		if name == pipeline.EscalationMCPTool {
			return
		}
	}
	t.Errorf("escalation hint names MCP tool %q, which the core profile does not expose: %v",
		pipeline.EscalationMCPTool, names)
}

// TestFetchToolsAreBudgeted verifies the plain-HTTP fetch tools get the
// MCP-mode token budget like the other output-heavy tools. Without it a
// single fetch_url call can return a whole page into the model context, and
// fetch_batch multiplies that by up to 20 URLs.
func TestFetchToolsAreBudgeted(t *testing.T) {
	for _, cmd := range []string{"fetch.url", "fetch.batch"} {
		if !mcpBudgetedCommands[cmd] {
			t.Errorf("daemon command %q is not token-budgeted in MCP mode", cmd)
		}
	}
}
