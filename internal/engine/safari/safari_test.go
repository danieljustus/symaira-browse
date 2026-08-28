package safari

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// fakeRunner records the scripts it was asked to run and returns scripted
// output keyed by substring, so tests exercise the engine logic without
// osascript or a running Safari.
type fakeRunner struct {
	calls   []string
	answers map[string]string
}

func (f *fakeRunner) Run(_ context.Context, script string) (string, error) {
	f.calls = append(f.calls, script)
	for needle, out := range f.answers {
		if strings.Contains(script, needle) {
			return out, nil
		}
	}
	return "", nil
}

func newFake(answers map[string]string) *fakeRunner {
	return &fakeRunner{answers: answers}
}

func TestCapabilitiesReflectsOptIn(t *testing.T) {
	ro := NewWithRunner(newFake(nil))
	caps := ro.Capabilities()
	if caps.Kind != EngineKind {
		t.Fatalf("Kind = %q, want %q", caps.Kind, EngineKind)
	}
	if caps.LaunchMode != "attach" {
		t.Fatalf("LaunchMode = %q, want attach", caps.LaunchMode)
	}
	if includes(caps.Interfaces, "InteractionEngine") {
		t.Fatalf("read-only engine must not report InteractionEngine: %+v", caps.Interfaces)
	}
	if !includes(caps.Interfaces, "TabManager") || !includes(caps.Interfaces, "InspectionEngine") {
		t.Fatalf("expected TabManager/InspectionEngine in interfaces: %+v", caps.Interfaces)
	}

	opt := NewWithRunner(newFake(nil))
	opt.OptInInteractions = true
	allowlist, err := policy.ParseAllowlist([]string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	opt.Allowlist = allowlist
	caps2 := opt.Capabilities()
	if !includes(caps2.Interfaces, "InteractionEngine") {
		t.Fatalf("opt-in engine must report InteractionEngine: %+v", caps2.Interfaces)
	}
}

func includes(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestDiagnoseClickReportsInterception is the core correctness guard from #297:
// elementFromPoint must detect an overlay-covered target and refuse the click,
// mirroring the chrome engine's interception behaviour.
func TestDiagnoseClickReportsInterception(t *testing.T) {
	// The diagnostic expression returns JSON with targeted:false (covered).
	fake := newFake(map[string]string{
		"document.elementFromPoint": `{"targeted":false,"reason":"offscreen","nodename":"div"}`,
	})
	e := NewWithRunner(fake)
	diag, err := e.DiagnoseClick(context.Background(), engine.Page{}, engine.InteractionTarget{NodeID: "#btn"})
	if err != nil {
		t.Fatalf("DiagnoseClick returned error: %v", err)
	}
	if diag.Targeted {
		t.Fatalf("expected covered target to be reported as not targeted")
	}
}

func TestPerformInteractionRefusesInterceptedClick(t *testing.T) {
	fake := newFake(map[string]string{
		"document.elementFromPoint": `{"targeted":false,"reason":"overlay","nodename":"div"}`,
	})
	e := NewWithRunner(fake)
	e.OptInInteractions = true
	err := e.PerformInteraction(context.Background(), engine.Page{},
		engine.InteractionTarget{NodeID: "#btn"},
		engine.InteractionRequest{Action: engine.ActionClick, Selector: "#btn"})
	if err == nil {
		t.Fatal("expected click on covered target to be refused")
	}
	if !strings.Contains(err.Error(), "intercepted") {
		t.Fatalf("expected interception error, got: %v", err)
	}
}

func TestPerformInteractionRefusesWithoutOptIn(t *testing.T) {
	e := NewWithRunner(newFake(nil))
	err := e.PerformInteraction(context.Background(), engine.Page{},
		engine.InteractionTarget{NodeID: "#btn"},
		engine.InteractionRequest{Action: engine.ActionClick, Selector: "#btn"})
	if err == nil {
		t.Fatal("expected interaction to be refused without opt-in")
	}
	if _, ok := err.(*engine.UnsupportedOperationError); !ok {
		t.Fatalf("expected UnsupportedOperationError, got %T: %v", err, err)
	}
}

func TestPerformInteractionUnsupportedAction(t *testing.T) {
	e := NewWithRunner(newFake(nil))
	e.OptInInteractions = true
	err := e.PerformInteraction(context.Background(), engine.Page{},
		engine.InteractionTarget{NodeID: "#f"},
		engine.InteractionRequest{Action: engine.ActionHover, Selector: "#f"})
	if err == nil {
		t.Fatal("expected hover (no network layer) to be unsupported")
	}
	if _, ok := err.(*engine.UnsupportedOperationError); !ok {
		t.Fatalf("expected UnsupportedOperationError, got %T: %v", err, err)
	}
}

func TestPinnedTabNeverUsesWindow1CurrentTab(t *testing.T) {
	fake := newFake(nil)
	e := NewWithRunner(fake)
	e.PinnedTabName = "Symaira"
	_, _ = e.evaluateTab(context.Background(), "1+1")
	if len(fake.calls) != 1 {
		t.Fatalf("expected exactly one osascript call, got %d", len(fake.calls))
	}
	script := fake.calls[0]
	if strings.Contains(script, "current tab of window 1") {
		t.Fatalf("engine must never resolve 'current tab of window 1'; got: %s", script)
	}
	if !strings.Contains(script, `tab "Symaira" of window 1`) {
		t.Fatalf("expected pinned tab reference, got: %s", script)
	}
}

func TestReadPathReturnsURLAndTitle(t *testing.T) {
	fake := newFake(map[string]string{
		"window.location.href": `"https://example.com/page"`,
		"document.title":       `"Example"`,
	})
	e := NewWithRunner(fake)
	e.OptInInteractions = true
	url, err := e.Evaluate(context.Background(), engine.Page{}, "window.location.href")
	if err != nil {
		t.Fatalf("Evaluate href: %v", err)
	}
	if !strings.Contains(string(url.Value), "example.com/page") {
		t.Fatalf("unexpected href result: %s", url.Value)
	}
	title, err := e.Evaluate(context.Background(), engine.Page{}, "document.title")
	if err != nil {
		t.Fatalf("Evaluate title: %v", err)
	}
	if !strings.Contains(string(title.Value), "Example") {
		t.Fatalf("unexpected title result: %s", title.Value)
	}
}

func TestUnsupportedOperationsReturnTypedError(t *testing.T) {
	e := NewWithRunner(newFake(nil))
	if _, err := e.AXTree(context.Background(), engine.Page{}); err == nil {
		t.Fatal("AXTree should be unsupported")
	} else if _, ok := err.(*engine.UnsupportedOperationError); !ok {
		t.Fatalf("expected UnsupportedOperationError, got %T", err)
	}
	if _, err := e.Screenshot(context.Background(), engine.Page{}); err == nil {
		t.Fatal("Screenshot should be unsupported")
	} else if _, ok := err.(*engine.UnsupportedOperationError); !ok {
		t.Fatalf("expected UnsupportedOperationError, got %T", err)
	}
}

func TestCloseIsIdempotentAndDetaches(t *testing.T) {
	e := NewWithRunner(newFake(nil))
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := e.Evaluate(context.Background(), engine.Page{}, "1+1"); err == nil {
		t.Fatal("expected closed engine to refuse Evaluate")
	}
}

func TestEvaluateRefusesArbitraryExpressionWithoutOptIn(t *testing.T) {
	fake := newFake(nil)
	e := NewWithRunner(fake)
	_, err := e.Evaluate(context.Background(), engine.Page{}, "document.cookie")
	if err == nil {
		t.Fatal("arbitrary Evaluate expression succeeded without opt-in")
	}
	var unsupported *engine.UnsupportedOperationError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Evaluate error = %T: %v, want UnsupportedOperationError", err, err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("Evaluate called Safari runner %d times, want none", len(fake.calls))
	}
}

func TestEvaluateAllowsOnlyInspectionExpressionsWithOptIn(t *testing.T) {
	fake := newFake(map[string]string{"document.title": `"Example"`, "window.location.href": `"https://example.com/"`})
	e := NewWithRunner(fake)
	e.OptInInteractions = true
	for _, expression := range []string{"document.title", "location.href", "window.location.href"} {
		if _, err := e.Evaluate(context.Background(), engine.Page{}, expression); err != nil {
			t.Fatalf("Evaluate(%q) = %v, want allowed", expression, err)
		}
	}
	if _, err := e.Evaluate(context.Background(), engine.Page{}, "document.cookie"); err == nil {
		t.Fatal("arbitrary Evaluate expression succeeded with opt-in")
	} else {
		var unsupported *engine.UnsupportedOperationError
		if !errors.As(err, &unsupported) {
			t.Fatalf("Evaluate error = %T: %v, want UnsupportedOperationError", err, err)
		}
	}
}

func TestNavigateRefusesNonWebSchemesBeforeRunner(t *testing.T) {
	for _, target := range []string{"file:///Users/daniel/.ssh/id_rsa", "about:blank"} {
		fake := newFake(nil)
		e := NewWithRunner(fake)
		if _, err := e.Navigate(context.Background(), engine.Page{}, target); err == nil {
			t.Fatalf("Navigate(%q) succeeded, want refusal", target)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("Navigate(%q) called Safari runner before refusal", target)
		}
	}
}

func TestNavigateEnforcesAllowlistAndSSRFGuardBeforeRunner(t *testing.T) {
	allowlist, err := policy.ParseAllowlist([]string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		target string
		setup  func(*Engine)
	}{
		{name: "non allowlisted host", target: "https://other.example.net/", setup: func(e *Engine) { e.Allowlist = allowlist }},
		{name: "loopback", target: "http://127.0.0.1:8080/", setup: func(e *Engine) { e.SSRFGuard = policy.NewSSRFGuard(false) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFake(nil)
			e := NewWithRunner(fake)
			tt.setup(e)
			if _, err := e.Navigate(context.Background(), engine.Page{}, tt.target); err == nil {
				t.Fatalf("Navigate(%q) succeeded, want refusal", tt.target)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("Navigate(%q) called Safari runner before refusal", tt.target)
			}
		})
	}
}

func TestCapabilitiesHideInteractionsWithoutGuard(t *testing.T) {
	e := NewWithRunner(newFake(nil))
	e.OptInInteractions = true
	if includes(e.Capabilities().Interfaces, "InteractionEngine") {
		t.Fatal("engine without URL guard advertises InteractionEngine")
	}
}
