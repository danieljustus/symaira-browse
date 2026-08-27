package safari

import (
	"context"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
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
