package flows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/testserver"
)

// TestTabsKeepRefsPerTabEndToEnd verifies that refs are per-tab and survive
// tab switches on the real engine path.
func TestTabsKeepRefsPerTabEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	fixture := testserver.NewServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e-tabs"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, daemon.NavigationRuntimeOptions{})
	defer func() { _ = runtime.Close() }()
	executor := runtimeExecutor(runtime)

	// Open the static fixture in the first tab.
	if _, err := executor(ctx, frame("open", map[string]any{"url": fixture.URLFor(testserver.Static)}, "e2e-tabs")); err != nil {
		t.Fatalf("open: %v", err)
	}
	// Snapshot and remember a ref on tab 1.
	snapshot1, err := executor(ctx, frame("snapshot", map[string]any{}, "e2e-tabs"))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ref1 := firstRef(t, snapshot1)
	if ref1 == "" {
		t.Fatal("tab 1 has no refs")
	}

	// Open a second tab with the form fixture.
	if _, err := executor(ctx, frame("tab.new", map[string]any{"label": "form", "url": fixture.URLFor(testserver.Form)}, "e2e-tabs")); err != nil {
		t.Fatalf("tab.new: %v", err)
	}
	// The new tab is active; snapshot there resolves the form fixture.
	snapshot2, err := executor(ctx, frame("snapshot", map[string]any{}, "e2e-tabs"))
	if err != nil {
		t.Fatalf("snapshot tab2: %v", err)
	}
	ref2 := firstRef(t, snapshot2)
	if ref2 == "" {
		t.Fatal("tab 2 has no refs")
	}
	// Both tabs resolve their own refs: a ref that exists on tab 2 must
	// resolve there (click a form button target by its own ref).
	if _, err := executor(ctx, frame("click", map[string]any{"selector": "@" + ref2}, "e2e-tabs")); err != nil {
		t.Fatalf("click tab-2 ref on tab 2: %v", err)
	}

	// Switch back to tab 1: its ref must still resolve and click through.
	if _, err := executor(ctx, frame("tab.switch", map[string]any{"tab": "t1"}, "e2e-tabs")); err != nil {
		t.Fatalf("tab.switch: %v", err)
	}
	titleBefore, err := executor(ctx, frame("get.title", map[string]any{}, "e2e-tabs"))
	if err != nil {
		t.Fatalf("get.title after switch: %v", err)
	}
	if !strings.Contains(payloadString(titleBefore), "Static fixture") {
		t.Fatalf("tab 1 title after switch = %s, want static fixture", payloadString(titleBefore))
	}
	// The ref from tab 1 still resolves: clicking the link navigates.
	if _, err := executor(ctx, frame("click", map[string]any{"selector": "@" + ref1}, "e2e-tabs")); err != nil {
		t.Fatalf("click tab-1 ref after switch: %v", err)
	}
}

// TestFramesNestedIframesEndToEnd verifies nested iframes are addressable.
func TestFramesNestedIframesEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	fixture := testserver.NewServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e-frames"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, daemon.NavigationRuntimeOptions{})
	defer func() { _ = runtime.Close() }()
	executor := runtimeExecutor(runtime)

	if _, err := executor(ctx, frame("open", map[string]any{"url": fixture.URLFor(testserver.Iframe)}, "e2e-frames")); err != nil {
		t.Fatalf("open: %v", err)
	}
	treeResponse, err := executor(ctx, frame("frame.tree", nil, "e2e-frames"))
	if err != nil {
		t.Fatalf("frame.tree: %v", err)
	}
	tree := parseFrameTree(t, treeResponse)
	if len(tree) == 0 {
		t.Fatal("frame tree is empty")
	}
	// Find the deepest frame (grandchild).
	grandchild := findDeepestFrame(tree)
	if grandchild == "" {
		t.Fatalf("no nested frames found in tree: %+v", tree)
	}
	if _, err := executor(ctx, frame("frame.select", map[string]any{"frame": grandchild}, "e2e-frames")); err != nil {
		t.Fatalf("frame.select: %v", err)
	}
	// The grandchild content must be addressable after selection.
	html, err := executor(ctx, frame("get.html", map[string]any{}, "e2e-frames"))
	if err != nil {
		t.Fatalf("get.html in frame: %v", err)
	}
	if !strings.Contains(payloadString(html), "grandchild") {
		t.Errorf("grandchild frame content not addressed; got %s", payloadString(html))
	}
}

// TestDialogBeforeUnloadDoesNotBlockEndToEnd verifies that a beforeunload
// dialog never blocks automation (auto-dismiss default).
func TestDialogBeforeUnloadDoesNotBlockEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e-dialog"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, daemon.NavigationRuntimeOptions{})
	defer func() { _ = runtime.Close() }()
	executor := runtimeExecutor(runtime)

	// A page that registers a beforeunload handler via a data URL.
	page := `<!doctype html><html><body><h1>Dialog page</h1><script>window.addEventListener('beforeunload', function(e){ e.preventDefault(); e.returnValue=''; });</script></body></html>`
	dataURL := "data:text/html;base64," + base64Encode(page)
	if _, err := executor(ctx, frame("open", map[string]any{"url": dataURL}, "e2e-dialog")); err != nil {
		t.Fatalf("open dialog page: %v", err)
	}
	// Navigating away must not hang: the beforeunload dialog is auto-dismissed.
	done := make(chan error, 1)
	go func() {
		_, err := executor(ctx, frame("open", map[string]any{"url": "about:blank"}, "e2e-dialog"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("navigation blocked by beforeunload dialog: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("navigation hung: beforeunload dialog blocked automation")
	}
	// Explicit dialog control still works.
	if _, err := executor(ctx, frame("dialog.status", nil, "e2e-dialog")); err != nil {
		t.Fatalf("dialog.status: %v", err)
	}
}

func frame(command string, args any, session string) daemon.Frame {
	raw, _ := json.Marshal(args)
	return daemon.Frame{Cmd: command, Args: raw, Session: session}
}

func firstRef(t *testing.T, response daemon.Response) string {
	t.Helper()
	raw, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var payload struct {
		Refs map[string]engine.SnapshotRef `json:"refs"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	for ref := range payload.Refs {
		return ref
	}
	return ""
}

func parseFrameTree(t *testing.T, response daemon.Response) []engine.FrameInfo {
	t.Helper()
	raw, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatalf("marshal frame tree: %v", err)
	}
	var payload struct {
		Frames []engine.FrameInfo `json:"frames"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode frame tree: %v", err)
	}
	return payload.Frames
}

func findDeepestFrame(frames []engine.FrameInfo) string {
	var deepest string
	var walk func([]engine.FrameInfo, int)
	walk = func(list []engine.FrameInfo, depth int) {
		for _, frame := range list {
			if len(frame.Children) > 0 {
				walk(frame.Children, depth+1)
			} else if frame.ID != "" {
				deepest = frame.ID
			}
		}
	}
	walk(frames, 0)
	return deepest
}

func payloadString(response daemon.Response) string {
	raw, _ := json.Marshal(response.Data)
	return string(raw)
}

func base64Encode(value string) string {
	return strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(
		encodeStd(value), "+", "-"), "/", "_"), "=")
}

func encodeStd(value string) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var builder strings.Builder
	data := []byte(value)
	for i := 0; i < len(data); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], data[i:])
		builder.WriteByte(table[chunk[0]>>2])
		builder.WriteByte(table[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			builder.WriteByte(table[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			builder.WriteByte('=')
		}
		if n > 2 {
			builder.WriteByte(table[chunk[2]&0x3f])
		} else {
			builder.WriteByte('=')
		}
	}
	return builder.String()
}
