package safari

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

type rustSafariAttachFixture struct {
	SchemaVersion int                           `json:"schema_version"`
	EngineKind    string                        `json:"engine_kind"`
	LaunchMode    string                        `json:"launch_mode"`
	Script        string                        `json:"script_shape"`
	Capabilities  interface{}                   `json:"capabilities"`
	Policy        map[string]any                `json:"policy"`
	Unsupported   []string                      `json:"unsupported"`
	Lifecycle     map[string]any                `json:"lifecycle"`
	Navigation    []rustSafariNavigationFixture `json:"navigation"`
}

type rustSafariNavigationFixture struct {
	Name         string                  `json:"name"`
	Target       string                  `json:"target"`
	ObservedURLs []string                `json:"observed_urls"`
	URLReads     int                     `json:"url_reads"`
	Result       engine.NavigationResult `json:"result"`
}

type navigationFixtureRunner struct {
	urls  []string
	reads int
}

func (r *navigationFixtureRunner) Run(_ context.Context, script string) (string, error) {
	if strings.Contains(script, "set URL of its document") {
		return "", nil
	}
	if r.reads >= len(r.urls) {
		return "", fmt.Errorf("navigation fixture exhausted after %d reads", r.reads)
	}
	value := r.urls[r.reads]
	r.reads++
	return value, nil
}

func navigationFixtures(t *testing.T) []rustSafariNavigationFixture {
	t.Helper()
	cases := []rustSafariNavigationFixture{
		{Name: "immediate_target", Target: "https://allowed.example/target", ObservedURLs: []string{`"https://allowed.example/target"`}},
		{Name: "stable_old_url_before_target", Target: "https://allowed.example/target", ObservedURLs: []string{`"https://allowed.example/old"`, `"https://allowed.example/old"`, `"https://allowed.example/target"`}},
	}
	for i := range cases {
		c := &cases[i]
		runner := &navigationFixtureRunner{urls: c.ObservedURLs}
		e := NewWithRunner(runner)
		e.PollInterval = time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := e.Navigate(ctx, engine.Page{ID: "safari-live"}, c.Target)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		c.Result = result
		c.URLReads = runner.reads
	}
	return cases
}

type fixtureRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *fixtureRunner) Run(_ context.Context, script string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, script)
	r.mu.Unlock()
	return `"https://allowed.example/"`, nil
}

// TestGenerateRustPortSafariAttachFixture freezes the Go AppleScript boundary
// without touching Safari or launching osascript.
func TestGenerateRustPortSafariAttachFixture(t *testing.T) {
	output := os.Getenv("RUST_PORT_SAFARI_ATTACH_OUT")
	if output == "" {
		t.Skip("set RUST_PORT_SAFARI_ATTACH_OUT to generate the Safari attach fixture")
	}
	runner := &fixtureRunner{}
	e := NewWithRunner(runner)
	e.PinnedTabName = "Symaira"
	e.OptInInteractions = true
	ctx := context.Background()
	live, err := e.NewContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	page, err := e.NewPage(ctx, live, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Navigate(ctx, page, "file:///etc/passwd"); err == nil {
		t.Fatal("file URL unexpectedly accepted")
	}
	before := len(runner.calls)
	if _, err := e.Navigate(ctx, page, "https://allowed.example/"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) <= before {
		t.Fatal("allowed navigation did not reach the runner")
	}
	caps := e.Capabilities()
	fixture := rustSafariAttachFixture{
		Navigation:    navigationFixtures(t),
		SchemaVersion: 1,
		EngineKind:    EngineKind,
		LaunchMode:    "attach",
		Script:        runner.calls[len(runner.calls)-1],
		Capabilities:  caps,
		Policy: map[string]any{
			"invalid_target_before_runner":   true,
			"allowlist_denial_before_runner": true,
			"ssrf_denial_before_runner":      true,
		},
		Unsupported: []string{"ax-tree", "screenshot", "network", "upload", "arbitrary evaluation"},
		Lifecycle: map[string]any{
			"launch_starts_safari": false,
			"close_quits_safari":   false,
			"close_is_idempotent":  true,
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
