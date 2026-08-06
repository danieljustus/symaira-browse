package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/budget"
)

// startBudgetServer starts a test server with a truncate-and-store cache.
func startBudgetServer(t *testing.T, cacheDir string) (string, context.CancelFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sb-budget-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "default.sock")
	ctx, cancel := context.WithCancel(context.Background())
	handler := func(ctx context.Context, frame Frame) (any, []Warning, error) {
		return map[string]any{"body": strings.Repeat("0123456789", 2000)}, nil, nil
	}
	server := NewServer(Options{
		SocketPath:       path,
		Handler:          handler,
		IdleTimeout:      -1,
		OperationTimeout: 2 * time.Second,
		PeerValidator:    func(net.Conn) error { return nil },
		CacheDir:         cacheDir,
		CacheTTL:         time.Hour,
	})
	ready := make(chan error, 1)
	go func() { ready <- server.ListenAndServe(ctx) }()
	waitForSocket(t, path, ready)
	t.Cleanup(func() { cancel(); _ = server.Close(); <-ready })
	return path, cancel
}

func waitForSocket(t *testing.T, path string, ready chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o600 == 0o600 {
			return
		}
		select {
		case err := <-ready:
			t.Fatalf("test daemon stopped before ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("test daemon did not create its socket")
		}
		time.Sleep(time.Millisecond)
	}
}

func requestBudget(t *testing.T, path string, maxTokens *int) (Response, error) {
	t.Helper()
	client := NewClient(ClientOptions{SocketPath: path, Session: "default"})
	return client.Request(context.Background(), Frame{Cmd: "snapshot", Session: "default", MaxTokens: maxTokens})
}

func TestServerTruncatesOversizedPayload(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "out")
	path, _ := startBudgetServer(t, cacheDir)

	// Without a budget the full payload is returned.
	response, err := requestBudget(t, path, nil)
	if err != nil || !response.Success {
		t.Fatalf("unbudgeted request: %v, %v", response, err)
	}
	if body, _ := response.Data.(map[string]any)["body"].(string); len(body) != 20000 {
		t.Fatalf("full payload length = %d, want 20000", len(body))
	}

	// With a small budget the response is the truncation marker.
	maxTokens := 100
	response, err = requestBudget(t, path, &maxTokens)
	if err != nil || !response.Success {
		t.Fatalf("budgeted request: %v, %v", response, err)
	}
	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want marker map", response.Data)
	}
	if data["truncated"] != true {
		t.Fatalf("truncated = %v, want true", data["truncated"])
	}
	cacheID, _ := data["cache_id"].(string)
	if cacheID == "" {
		t.Fatal("marker has no cache_id")
	}
	if data["tokens_total"].(float64) < 5000 {
		t.Fatalf("tokens_total = %v, want >= 5000 (payload is JSON-wrapped)", data["tokens_total"])
	}
	if _, ok := data["head"].(string); !ok {
		t.Fatal("marker has no head")
	}
	if _, ok := data["foot"].(string); !ok {
		t.Fatal("marker has no foot")
	}
	// The full payload is stored in the cache and loadable.
	cache := budget.NewCache(cacheDir, time.Hour)
	stored, err := cache.Load(cacheID)
	if err != nil {
		t.Fatalf("cache load: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(stored, &roundTrip); err != nil {
		t.Fatalf("stored payload is not valid JSON: %v", err)
	}
	if len(roundTrip["body"].(string)) != 20000 {
		t.Fatal("stored payload is not the full output")
	}
}

func TestServerFailsClosedWithoutCacheDir(t *testing.T) {
	path, _ := startBudgetServer(t, "") // no cache dir configured
	maxTokens := 100
	response, err := requestBudget(t, path, &maxTokens)
	if err != nil {
		t.Fatal(err)
	}
	if response.Success {
		t.Fatalf("budgeted request without cache dir must fail, got %+v", response)
	}
	if response.Error == nil || !strings.Contains(response.Error.Message, "no output cache directory") {
		t.Fatalf("error = %+v, want fail-closed message", response.Error)
	}
}
