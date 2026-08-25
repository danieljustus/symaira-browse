package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func mustArgs(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestConsoleFramesWithoutSession verifies the console/errors buffers are
// empty (not an error) when no browser session has been started (issue #60).
func TestConsoleFramesWithoutSession(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{})
	if _, err := registry.Ensure("s"); err != nil {
		t.Fatal(err)
	}
	runtime := NewNavigationRuntime(registry, "", NavigationRuntimeOptions{})
	defer func() { _ = runtime.Close() }()
	ctx := context.Background()

	data, _, err := runtime.Handle(ctx, Frame{Cmd: "console.list", Args: nil, Session: "s"})
	if err != nil {
		t.Fatalf("console.list without session: %v", err)
	}
	payload, _ := data.(map[string]any)
	if payload["count"] != 0 {
		t.Fatalf("count = %v, want 0", payload["count"])
	}
	data, _, err = runtime.Handle(ctx, Frame{Cmd: "errors.list", Args: nil, Session: "s"})
	if err != nil {
		t.Fatalf("errors.list without session: %v", err)
	}
	payload, _ = data.(map[string]any)
	if payload["count"] != 0 {
		t.Fatalf("count = %v, want 0", payload["count"])
	}
	if _, _, err := runtime.Handle(ctx, Frame{Cmd: "console.clear", Args: nil, Session: "s"}); err != nil {
		t.Fatalf("console.clear without session: %v", err)
	}
}

// TestEvalRequiresRunningBrowser verifies eval fails cleanly when no browser
// is configured instead of fabricating a result (issue #60).
func TestEvalRequiresRunningBrowser(t *testing.T) {
	// Deterministic: stub discovery to find nothing, so the test does not
	// depend on whether a Chrome install exists on the host.
	orig := resolveBrowserExecutable
	resolveBrowserExecutable = stubResolver("", errors.New("no browser"))
	t.Cleanup(func() { resolveBrowserExecutable = orig })
	registry := NewSessionRegistry(SessionRegistryOptions{})
	if _, err := registry.Ensure("s"); err != nil {
		t.Fatal(err)
	}
	runtime := NewNavigationRuntime(registry, "", NavigationRuntimeOptions{})
	defer func() { _ = runtime.Close() }()
	ctx := context.Background()

	_, _, err := runtime.Handle(ctx, Frame{Cmd: "eval", Args: mustArgs(t, map[string]any{"expression": "1+1"}), Session: "s"})
	if err == nil {
		t.Fatal("eval succeeded without a browser")
	}
	// Empty expressions are rejected before any browser work.
	_, _, err = runtime.Handle(ctx, Frame{Cmd: "eval", Args: mustArgs(t, map[string]any{"expression": "  "}), Session: "s"})
	if err == nil {
		t.Fatal("empty eval expression was accepted")
	}
}
