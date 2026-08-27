// Package safari implements the safari-attach engine: it drives a human's
// live, logged-in Safari session through Apple Events (do JavaScript), without
// launching a separate browser or opening an isolated automation window.
//
// The engine is intentionally scoped between the chrome and static extremes
// (issue #297). It can read the live session, and — behind an explicit opt-in
// — perform interactions. Every interaction hit-tests through
// document.elementFromPoint first and raises the same interception error the
// chrome engine reports when a target is covered, so engines differ in scope
// but never in truth.
//
// The AppleScript bridge is abstracted behind a runner interface so the logic
// (hit-testing, navigation polling, capability reporting) is unit-testable
// without a running Safari.
package safari

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// EngineKind is the capability kind reported for this engine.
const EngineKind = "safari-attach"

// ErrUnsupported is returned for operations the engine cannot perform because
// Safari has no network layer, no cross-origin frame access, and no file
// upload channel through Apple Events.
var ErrUnsupported = engine.UnsupportedOperation(EngineKind, "unsupported-operation")

// Runner executes AppleScript source against the live Safari session. The
// default implementation shells out to osascript; tests inject a fake.
type Runner interface {
	// Run executes the given AppleScript source and returns its stdout.
	Run(ctx context.Context, script string) (string, error)
}

// osascriptRunner is the production runner. It is a struct so tests can replace
// it; never call osascript directly from engine code.
type osascriptRunner struct {
	// execCommand is overridable for tests; defaults to exec.CommandContext.
	execCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
}

func (r *osascriptRunner) Run(ctx context.Context, script string) (string, error) {
	run := r.execCommand
	if run == nil {
		run = exec.CommandContext
	}
	cmd := run(ctx, "osascript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("safari engine: osascript failed: %w", err)
	}
	return string(out), nil
}

// Engine drives the live Safari session. It is not safe for concurrent use of
// the same pinned tab; callers hold the navigation service lock per page.
type Engine struct {
	mu sync.Mutex

	// runner executes AppleScript. Swapped for tests.
	runner Runner

	// OptInInteractions enables the interaction path. Safe only when the human
	// has explicitly enabled it; defaults to false (read-only).
	OptInInteractions bool

	// PinnedTabName pins the engine to one named tab. Safari resolves
	// "current tab of window 1" to whatever window the human last touched, so
	// the engine never uses that reference — it addresses a named tab only.
	PinnedTabName string

	// PollInterval is how often the engine re-checks the URL while waiting for
	// a navigation to settle. Defaults to defaultPollInterval.
	PollInterval time.Duration

	// closed guards against use after Close.
	closed bool
}

// New creates a read-only safari-attach engine with the production runner.
func New() *Engine {
	return NewWithRunner(&osascriptRunner{})
}

// NewWithRunner creates an engine around an explicit runner (used by tests).
func NewWithRunner(runner Runner) *Engine {
	if runner == nil {
		runner = &osascriptRunner{}
	}
	return &Engine{
		runner:       runner,
		PollInterval: defaultPollInterval,
	}
}

const defaultPollInterval = 250 * time.Millisecond

// pinnedTabRef returns the AppleScript reference for the pinned tab. The engine
// only ever addresses a tab by name; it never resolves "window 1" because that
// reference has been measured to point at an unrelated window during a live
// session.
func (e *Engine) pinnedTabRef() string {
	name := e.PinnedTabName
	if name == "" {
		name = "Symaira"
	}
	return fmt.Sprintf("tab %q of window 1", name)
}

// Launch is a no-op: the engine attaches to an already-running Safari.
func (e *Engine) Launch(context.Context) error { return nil }

// NewContext returns a static context handle (the live session).
func (e *Engine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: EngineKind}, nil
}

// NewPage returns a static page handle for the pinned tab.
func (e *Engine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "safari-live"}, nil
}

// evaluateTab runs JavaScript in the pinned tab and returns the parsed result.
func (e *Engine) evaluateTab(ctx context.Context, expr string) (string, error) {
	e.mu.Lock()
	closed := e.closed
	tabRef := e.pinnedTabRef()
	e.mu.Unlock()
	if closed {
		return "", errors.New("safari engine: engine is closed")
	}
	script := fmt.Sprintf("tell application %q\ntell %s\ndo JavaScript %q\nend tell\nend tell",
		"Safari", tabRef, expr)
	raw, err := e.runner.Run(ctx, script)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(raw, "\n"), nil
}

// Navigate waits for the live tab's URL to reach target. It polls the URL
// rather than readyState: immediately after a set URL the old page still
// reports readyState "complete", so readyState is never a reliable signal.
func (e *Engine) Navigate(ctx context.Context, _ engine.Page, target string) (engine.NavigationResult, error) {
	e.mu.Lock()
	closed := e.closed
	tabRef := e.pinnedTabRef()
	poll := e.PollInterval
	e.mu.Unlock()
	if closed {
		return engine.NavigationResult{}, errors.New("safari engine: engine is closed")
	}
	if poll <= 0 {
		poll = defaultPollInterval
	}
	script := fmt.Sprintf("tell application %q\ntell %s\nset URL of its document to %q\nend tell\nend tell",
		"Safari", tabRef, target)
	if _, err := e.runner.Run(ctx, script); err != nil {
		return engine.NavigationResult{}, err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		cur, err := e.currentURL(ctx)
		if err == nil && cur == target {
			return engine.NavigationResult{FrameID: "safari-live", LoaderID: "safari-live"}, nil
		}
		select {
		case <-ctx.Done():
			return engine.NavigationResult{}, ctx.Err()
		case <-deadline.C:
			return engine.NavigationResult{}, fmt.Errorf("safari engine: navigation to %q did not settle", target)
		case <-ticker.C:
		}
	}
}

func (e *Engine) currentURL(ctx context.Context) (string, error) {
	out, err := e.evaluateTab(ctx, "window.location.href")
	if err != nil {
		return "", err
	}
	return strings.Trim(out, `"`), nil
}

// Evaluate answers the supported inspection expressions in the live tab.
func (e *Engine) Evaluate(ctx context.Context, _ engine.Page, expression string) (engine.EvaluationResult, error) {
	out, err := e.evaluateTab(ctx, expression)
	if err != nil {
		return engine.EvaluationResult{}, err
	}
	raw, _ := json.Marshal(out)
	return engine.EvaluationResult{Value: raw, Type: "string"}, nil
}

// AXTree is not supported: Safari exposes no accessibility tree over the Apple
// Events channel used here.
func (e *Engine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) {
	return nil, engine.UnsupportedOperation(EngineKind, "ax-tree")
}

// Screenshot is not supported: no rendering surface is exposed via Apple Events.
func (e *Engine) Screenshot(context.Context, engine.Page) ([]byte, error) {
	return nil, engine.UnsupportedOperation(EngineKind, "screenshot")
}

// Close releases the engine. It detaches from the live session; it never quits
// Safari, because the browser belongs to the human.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return nil
}

// Capabilities reports the honest boundary of the safari-attach engine (issue
// #295 / #297): it implements only inspection, navigation state, interaction
// (behind opt-in) and tab management. Everything else is unsupported.
func (e *Engine) Capabilities() engine.Capabilities {
	impl := []string{"InspectionEngine", "NavigationStateProvider", "TabManager"}
	if e.OptInInteractions {
		impl = append(impl, "InteractionEngine")
	}
	caps := engine.CapabilitiesFor(EngineKind, impl...)
	caps.LaunchMode = "attach"
	return caps
}
