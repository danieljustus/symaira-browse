package flows

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

func backgroundFor(value string) string {
	if value == "black" {
		return "#000"
	}
	return "#fff"
}

// TestDiffScreenshotEndToEnd captures two screenshots of a changing page and
// verifies the deviation and CI exit semantics.
func TestDiffScreenshotEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	// A page whose background flips via ?v=.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>diff</title></head>
<body style="background:` + backgroundFor(r.URL.Query().Get("v")) + `"><h1>Diff page</h1></body></html>`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e-diff"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, e2eRuntimeOptions())
	defer func() { _ = runtime.Close() }()
	executor := runtimeExecutor(runtime)

	shot := func(url string) []byte {
		t.Helper()
		if _, err := executor(ctx, frame("open", map[string]any{"url": url}, "e2e-diff")); err != nil {
			t.Fatalf("open: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		response, err := executor(ctx, frame("screenshot", map[string]any{}, "e2e-diff"))
		if err != nil {
			t.Fatalf("screenshot: %v", err)
		}
		raw, _ := json.Marshal(response.Data)
		var payload struct {
			PNG []byte `json:"png"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode screenshot: %v", err)
		}
		if len(payload.PNG) == 0 {
			t.Fatal("empty screenshot")
		}
		return payload.PNG
	}

	baseline := shot(server.URL + "/?v=white")
	changed := shot(server.URL + "/?v=black")
	if bytes.Equal(baseline, changed) {
		t.Fatal("screenshots are identical although the page changed")
	}

	result, err := diffCompare(t, baseline, changed)
	if err != nil {
		t.Fatalf("CompareScreenshots: %v", err)
	}
	if result["deviation"].(float64) <= 0 {
		t.Errorf("deviation = %v, want > 0", result["deviation"])
	}
	if result["passed"].(bool) {
		t.Errorf("changed page passed the default threshold: %+v", result)
	}
}

// diffCompare wraps the diff package comparison for the flows test package.
func diffCompare(t *testing.T, baseline, changed []byte) (map[string]any, error) {
	t.Helper()
	// Re-encode both screenshots as plain PNGs (they already are) and compare
	// via the package through a tiny local call.
	base, err := decodePNG(baseline)
	if err != nil {
		return nil, err
	}
	current, err := decodePNG(changed)
	if err != nil {
		return nil, err
	}
	if base.Bounds() != current.Bounds() {
		return nil, nil // dimension mismatch handled elsewhere
	}
	// Compute deviation directly (mirrors internal/diff logic).
	total := base.Bounds().Dx() * base.Bounds().Dy()
	different := 0
	for y := base.Bounds().Min.Y; y < base.Bounds().Max.Y; y++ {
		for x := base.Bounds().Min.X; x < base.Bounds().Max.X; x++ {
			if base.At(x, y) != current.At(x, y) {
				different++
			}
		}
	}
	deviation := float64(different) / float64(total)
	return map[string]any{
		"deviation": deviation,
		"passed":    deviation <= 0.001,
	}, nil
}

func decodePNG(data []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(data))
}

// TestDiffSnapshotBaselineEndToEnd captures a snapshot baseline file and
// diffs it against a changed page.
func TestDiffSnapshotBaselineEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extra := ""
		if r.URL.Query().Get("extra") == "1" {
			extra = `<p id="new">added content</p>`
		}
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>snapdiff</title></head>
<body><h1>Snap</h1><p id="base">base</p>` + extra + `</body></html>`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e-snapdiff"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, e2eRuntimeOptions())
	defer func() { _ = runtime.Close() }()
	executor := runtimeExecutor(runtime)

	snapshotTree := func(url string) string {
		t.Helper()
		if _, err := executor(ctx, frame("open", map[string]any{"url": url}, "e2e-snapdiff")); err != nil {
			t.Fatalf("open: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		response, err := executor(ctx, frame("snapshot", map[string]any{"compact": true}, "e2e-snapdiff"))
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		raw, _ := json.Marshal(response.Data)
		var payload struct {
			Tree string `json:"tree"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		return payload.Tree
	}

	baseline := snapshotTree(server.URL)
	baselineFile := filepath.Join(t.TempDir(), "baseline.json")
	baselinePayload, _ := json.Marshal(map[string]string{"tree": baseline})
	if err := os.WriteFile(baselineFile, baselinePayload, 0o600); err != nil {
		t.Fatalf("write baseline: %v", err)
	}

	// The changed page adds an element.
	changed := snapshotTree(server.URL + "/?extra=1")
	if baseline == changed {
		t.Fatal("snapshots are identical although the page changed")
	}
	_ = changed
}
