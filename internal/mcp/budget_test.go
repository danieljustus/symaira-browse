package mcp

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// TestMCPBudgetDefaultAndOverride verifies the stricter MCP-mode token
// budget (issue #23): output-heavy tools send max_tokens to the daemon by
// default, callers can override it per call, and other tools stay unbudgeted.
func TestMCPBudgetDefaultAndOverride(t *testing.T) {
	base := socketBase(t)
	path, err := daemon.SocketPathIn(base, "test")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var gotMaxTokens *int
	daemonServer := daemon.NewServer(daemon.Options{
		SocketPath: path,
		Session:    "test",
		// In-process fixture: peer validation is unavailable on Windows.
		PeerValidator: func(net.Conn) error { return nil },
		CacheDir:      filepath.Join(base, "cache-out"),
		CacheTTL:      time.Hour,
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			mu.Lock()
			gotMaxTokens = frame.MaxTokens
			mu.Unlock()
			return map[string]any{"ok": true}, nil, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemonServer.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("capture daemon did not shut down")
		}
	})
	t.Cleanup(cancel)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.Dial("unix", path)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		select {
		case serveErr := <-done:
			t.Fatalf("capture daemon exited before ready: %v", serveErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	server := newTestServer(t, base)
	var snapshotTool, openTool ProxyTool
	for _, tool := range tools {
		switch tool.Name {
		case "snapshot":
			snapshotTool = tool
		case "open":
			openTool = tool
		}
	}
	if snapshotTool.Name == "" || openTool.Name == "" {
		t.Fatal("snapshot/open tools not found in the tool table")
	}

	// Output-heavy tool: default budget of 4000 tokens.
	if _, err := server.proxyTool(snapshotTool).Handler(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defaultBudget := gotMaxTokens
	mu.Unlock()
	if defaultBudget == nil || *defaultBudget != mcpDefaultMaxTokens {
		t.Fatalf("snapshot max_tokens = %v, want %d", defaultBudget, mcpDefaultMaxTokens)
	}

	// Per-call override wins.
	if _, err := server.proxyTool(snapshotTool).Handler(context.Background(), json.RawMessage(`{"max_tokens": 8000}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	override := gotMaxTokens
	mu.Unlock()
	if override == nil || *override != 8000 {
		t.Fatalf("overridden max_tokens = %v, want 8000", override)
	}

	// Non-output-heavy tools stay unbudgeted.
	if _, err := server.proxyTool(openTool).Handler(context.Background(), json.RawMessage(`{"url": "https://example.com/"}`)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	plain := gotMaxTokens
	mu.Unlock()
	if plain != nil {
		t.Fatalf("open max_tokens = %v, want nil", plain)
	}
}
