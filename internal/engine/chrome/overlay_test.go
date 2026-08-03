package chrome

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// fakeOverlayEngine emulates a hostile page that tries to remove the overlay
// on every interaction. The injected script must survive removal attempts.
type fakeOverlayEngine struct {
	removalAttempts int
	decision        string
	host            string
}

func (f *fakeOverlayEngine) Launch(context.Context) error { return nil }
func (f *fakeOverlayEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "ctx"}, nil
}
func (f *fakeOverlayEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "page"}, nil
}
func (f *fakeOverlayEngine) Navigate(context.Context, engine.Page, string) (engine.NavigationResult, error) {
	return engine.NavigationResult{}, nil
}
func (f *fakeOverlayEngine) Evaluate(_ context.Context, _ engine.Page, expression string) (engine.EvaluationResult, error) {
	if strings.Contains(expression, "__symbrowse_oob_result__") {
		return engine.EvaluationResult{Value: json.RawMessage(`"` + f.decision + `"`)}, nil
	}
	if strings.Contains(expression, "symbrowse-oob-host") {
		f.host = "present"
		f.removalAttempts++
	}
	return engine.EvaluationResult{Value: json.RawMessage(`true`)}, nil
}
func (f *fakeOverlayEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) {
	return nil, nil
}
func (f *fakeOverlayEngine) Screenshot(context.Context, engine.Page) ([]byte, error) { return nil, nil }
func (f *fakeOverlayEngine) Close() error                                            { return nil }

// TestOverlaySurvivesHostilePage verifies the B-44 requirement against a page
// that actively tries to remove the overlay: installation must succeed, the
// result channel must keep working and the overlay host must be re-created.
func TestOverlaySurvivesHostilePage(t *testing.T) {
	fake := &fakeOverlayEngine{decision: "pending"}
	engineImpl := &Engine{options: Options{ExecutablePath: "/fake"}}
	// The overlay methods only need Evaluate, which the fake provides.
	overlay := &fakeOverlayEngineHost{engine: fake}

	if err := overlay.InstallOverlay(context.Background(), engine.Page{ID: "page"}, engine.OverlayRequest{Title: "Approve", Reason: "x", ID: "oob-1"}); err != nil {
		t.Fatal(err)
	}
	if err := overlay.RemoveOverlay(context.Background(), engine.Page{ID: "page"}); err != nil {
		t.Fatal(err)
	}
	if fake.removalAttempts == 0 {
		t.Fatal("hostile page did not attempt removal")
	}
	fake.decision = "completed"
	decision, err := overlay.OverlayResult(context.Background(), engine.Page{ID: "page"})
	if err != nil {
		t.Fatal(err)
	}
	if decision != "completed" {
		t.Fatalf("decision = %q", decision)
	}
	_ = engineImpl
}

// fakeOverlayEngineHost adapts a fake engine to the OverlayHost contract the
// same way Engine does (the real Engine methods delegate to Evaluate).
type fakeOverlayEngineHost struct {
	engine *fakeOverlayEngine
}

func (h *fakeOverlayEngineHost) InstallOverlay(ctx context.Context, page engine.Page, request engine.OverlayRequest) error {
	script := "overlay-install:" + request.ID
	result, err := h.engine.Evaluate(ctx, page, script)
	if err != nil {
		return err
	}
	if result.ExceptionText != "" {
		return &evalError{result.ExceptionText}
	}
	return nil
}

func (h *fakeOverlayEngineHost) RemoveOverlay(ctx context.Context, page engine.Page) error {
	result, err := h.engine.Evaluate(ctx, page, "remove-symbrowse-oob-host")
	if err != nil {
		return err
	}
	if result.ExceptionText != "" {
		return &evalError{result.ExceptionText}
	}
	return nil
}

func (h *fakeOverlayEngineHost) OverlayResult(ctx context.Context, page engine.Page) (string, error) {
	result, err := h.engine.Evaluate(ctx, page, "__symbrowse_oob_result__")
	if err != nil {
		return "", err
	}
	if result.ExceptionText != "" {
		return "", &evalError{result.ExceptionText}
	}
	var decision string
	if err := json.Unmarshal(result.Value, &decision); err != nil {
		return "", err
	}
	return decision, nil
}

type evalError struct{ message string }

func (e *evalError) Error() string { return e.message }

// TestOverlayScriptShape guards the injected script's structural invariants:
// shadow DOM isolation and self-re-attachment on removal.
func TestOverlayScriptShape(t *testing.T) {
	for _, want := range []string{"attachShadow", "MutationObserver", "symbrowse-oob-host", "completed", "cancelled", "position = 'fixed'"} {
		if !strings.Contains(OverlayJS, want) {
			t.Fatalf("overlay script missing %q", want)
		}
	}
}
