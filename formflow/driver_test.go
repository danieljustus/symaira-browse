package formflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// fakeEngine is a minimal engine.Engine + InteractionEngine for driver tests.
type fakeEngine struct {
	evalResults map[string]engine.EvaluationResult
	evalErr     error
	navErr      error
	shot        []byte
	shotErr     error

	lastAction engine.InteractionRequest
	actionErr  error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		evalResults: map[string]engine.EvaluationResult{},
		shot:        []byte("png"),
	}
}

func (f *fakeEngine) Launch(context.Context) error { return nil }
func (f *fakeEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{}, nil
}
func (f *fakeEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{}, nil
}
func (f *fakeEngine) Navigate(_ context.Context, _ engine.Page, _ string) (engine.NavigationResult, error) {
	return engine.NavigationResult{}, f.navErr
}
func (f *fakeEngine) Evaluate(_ context.Context, _ engine.Page, expression string) (engine.EvaluationResult, error) {
	if f.evalErr != nil {
		return engine.EvaluationResult{}, f.evalErr
	}
	if result, ok := f.evalResults[expression]; ok {
		return result, nil
	}
	// Navigation state probes must decode into engine.NavigationState.
	return engine.EvaluationResult{
		Value: json.RawMessage(`{"url":"https://x.example","http_status":200,"ready_state":"complete","network_idle":true}`),
		Type:  "object",
	}, nil
}
func (f *fakeEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) { return nil, nil }
func (f *fakeEngine) Screenshot(context.Context, engine.Page) ([]byte, error) {
	return f.shot, f.shotErr
}
func (f *fakeEngine) Close() error { return nil }

func (f *fakeEngine) ResolveElement(context.Context, engine.Page, string) (engine.InteractionTarget, error) {
	return engine.InteractionTarget{BackendNodeID: 1}, nil
}
func (f *fakeEngine) ScrollIntoView(context.Context, engine.Page, engine.InteractionTarget) error {
	return nil
}
func (f *fakeEngine) PerformInteraction(_ context.Context, _ engine.Page, _ engine.InteractionTarget, request engine.InteractionRequest) error {
	if f.actionErr != nil {
		return f.actionErr
	}
	f.lastAction = request
	return nil
}

func strResult(value string) engine.EvaluationResult {
	return engine.EvaluationResult{Value: json.RawMessage(fmt.Sprintf("%q", value)), Type: "string"}
}

func TestEngineDriverNavigate(t *testing.T) {
	f := newFakeEngine()
	driver := NewEngineDriver(f, engine.Page{})
	if err := driver.Navigate(context.Background(), "https://x.example"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	f.navErr = errors.New("boom")
	if err := driver.Navigate(context.Background(), "https://x.example"); err == nil {
		t.Fatal("navigation error must propagate")
	}
}

func TestEngineDriverEvalString(t *testing.T) {
	f := newFakeEngine()
	f.evalResults["location.href"] = strResult("https://x.example/a")
	f.evalResults["document.body ? document.body.innerText : ''"] = strResult("Visible text")
	f.evalResults["document.documentElement ? document.documentElement.outerHTML : ''"] = strResult("<html>…</html>")
	driver := NewEngineDriver(f, engine.Page{})

	url, err := driver.CurrentURL(context.Background())
	if err != nil || url != "https://x.example/a" {
		t.Fatalf("CurrentURL = %q, %v", url, err)
	}
	text, err := driver.PageText(context.Background())
	if err != nil || text != "Visible text" {
		t.Fatalf("PageText = %q, %v", text, err)
	}
	html, err := driver.PageHTML(context.Background())
	if err != nil || html != "<html>…</html>" {
		t.Fatalf("PageHTML = %q, %v", html, err)
	}

	f.evalErr = errors.New("evaluate failed")
	if _, err := driver.CurrentURL(context.Background()); err == nil {
		t.Fatal("evaluation error must propagate")
	}
	f.evalErr = nil
	f.evalResults["location.href"] = engine.EvaluationResult{Value: json.RawMessage(`{"not":"a string"}`), Type: "object"}
	if _, err := driver.CurrentURL(context.Background()); err == nil {
		t.Fatal("malformed evaluation result must error")
	}
}

func TestEngineDriverScreenshot(t *testing.T) {
	f := newFakeEngine()
	driver := NewEngineDriver(f, engine.Page{})
	shot, err := driver.Screenshot(context.Background())
	if err != nil || string(shot) != "png" {
		t.Fatalf("Screenshot = %q, %v", shot, err)
	}
	f.shotErr = errors.New("shot failed")
	if _, err := driver.Screenshot(context.Background()); err == nil {
		t.Fatal("screenshot error must propagate")
	}
}

func TestEngineDriverFillUsesCSSFallback(t *testing.T) {
	// Without a snapshot, semantic finds produce no matches; the driver must
	// fall back to the CSS selector and perform the fill through the engine.
	f := newFakeEngine()
	driver := NewEngineDriver(f, engine.Page{})
	if err := driver.Fill(context.Background(), Selector{Label: "Email", CSS: "#email"}, "ada@example.com"); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if f.lastAction.Action != engine.ActionFill || f.lastAction.Selector != "#email" || f.lastAction.Value != "ada@example.com" {
		t.Fatalf("unexpected interaction: %+v", f.lastAction)
	}
}

func TestEngineDriverClickUsesCSSFallback(t *testing.T) {
	f := newFakeEngine()
	driver := NewEngineDriver(f, engine.Page{})
	if err := driver.Click(context.Background(), Selector{Text: "Send request", CSS: "#submit"}); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if f.lastAction.Action != engine.ActionClick || f.lastAction.Selector != "#submit" {
		t.Fatalf("unexpected interaction: %+v", f.lastAction)
	}
}

func TestEngineDriverElementNotFound(t *testing.T) {
	f := newFakeEngine()
	driver := NewEngineDriver(f, engine.Page{})
	err := driver.Fill(context.Background(), Selector{Label: "Missing"}, "x")
	if !errors.Is(err, ErrElementNotFound) {
		t.Fatalf("expected ErrElementNotFound, got %v", err)
	}
	err = driver.Click(context.Background(), Selector{})
	if !errors.Is(err, ErrElementNotFound) {
		t.Fatalf("expected ErrElementNotFound for empty selector, got %v", err)
	}
}

func TestEngineDriverInteractionErrorPropagates(t *testing.T) {
	f := newFakeEngine()
	f.actionErr = errors.New("element detached")
	driver := NewEngineDriver(f, engine.Page{})
	if err := driver.Fill(context.Background(), Selector{CSS: "#email"}, "x"); err == nil {
		t.Fatal("interaction error must propagate")
	}
}
