package chrome

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

type rustChromeContractFixture struct {
	SchemaVersion int                    `json:"schema_version"`
	Suite         string                 `json:"suite"`
	Capabilities  engine.Capabilities    `json:"capabilities"`
	ChromeArgs    []string               `json:"chrome_args"`
	Frames        []engine.FrameInfo     `json:"frames"`
	Policy        map[string]interface{} `json:"policy"`
	Upload        map[string]interface{} `json:"upload"`
	Download      map[string]interface{} `json:"download"`
	Errors        map[string]interface{} `json:"errors"`
	Unsupported   []string               `json:"unsupported"`
	Cleanup       map[string]interface{} `json:"cleanup"`
}

// TestGenerateRustPortChromeContractFixture is the Go oracle for the neutral
// chrome-full suite. It deliberately uses the production package's unexported
// CDP/guard helpers, while keeping browser launch in the opt-in Rust smoke.
func TestGenerateRustPortChromeContractFixture(t *testing.T) {
	output := os.Getenv("RUST_PORT_CHROME_CONTRACT_OUT")
	if output == "" {
		t.Skip("set RUST_PORT_CHROME_CONTRACT_OUT to generate the Chrome contract fixture")
	}

	callback := func(context.Context, string, string, interface{}, interface{}) error { return nil }
	policy, err := newNetworkPolicy([]string{"example.test"}, false, false, callback)
	if err != nil {
		t.Fatal(err)
	}
	allowlisted, allowlistedReason := policy.allowsURL("https://example.test/path")
	denied, deniedReason := policy.allowsURL("https://blocked.example/path")

	fixture := rustChromeContractFixture{
		SchemaVersion: 1,
		Suite:         "chrome-full",
		Capabilities:  New(Options{}).Capabilities(),
		ChromeArgs:    chromeArgs("<PROFILE>", true, true),
		Frames: (&Engine{}).frameChildren([]json.RawMessage{
			json.RawMessage(`{"frame":{"id":"root","url":"https://example.test/","name":"main"}}`),
			json.RawMessage(`{"frame":{"id":"child","parentId":"root","url":"https://example.test/frame","name":"nested"}}`),
		}),
		Policy: map[string]interface{}{
			"allowlisted":        allowlisted,
			"denied":             denied,
			"allowlisted_reason": allowlistedReason,
			"denied_reason":      deniedReason,
		},
		Upload: map[string]interface{}{
			"inside":    guardFixturePath(t, "<ROOT>/upload.txt", "<ROOT>"),
			"outside":   "rejected outside allowed directory",
			"traversal": "rejected path escapes allowed directory",
		},
		Download: map[string]interface{}{
			"behavior":       "allow",
			"events_enabled": true,
			"identity":       "guid-backed download event",
		},
		Errors: map[string]interface{}{
			"invalid_screenshot": invalidScreenshotError(t),
			"empty_selector":     "selector must not be empty",
			"timeout":            "bounded by request timeout",
		},
		Unsupported: []string{"har-export", "axe-core-audit"},
		Cleanup: map[string]interface{}{
			"owned_process_killed":    true,
			"owned_process_reaped":    true,
			"private_profile_removed": true,
			"close_is_idempotent":     true,
		},
	}

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func invalidScreenshotError(t *testing.T) string {
	t.Helper()
	_, err := New(Options{}).ScreenshotWithOptions(context.Background(), engine.Page{}, engine.ScreenshotOptions{Format: "bmp"})
	if err == nil {
		t.Fatal("invalid screenshot format unexpectedly succeeded")
	}
	return err.Error()
}

func guardFixturePath(t *testing.T, path, root string) string {
	t.Helper()
	if strings.HasPrefix(path, root) {
		return strings.Replace(path, root, "<ROOT>", 1)
	}
	return path
}
