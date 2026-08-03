package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// hintProbeEngine is a fake engine whose probe page renders a fixed
// JavaScript-disabled HTML, so JSRequired can be tested without a browser.
type hintProbeEngine struct {
	fakeNavigationEngine
	probeHTML         string
	scriptsDisabledOn []string
	disableSupported  bool
}

func (f *hintProbeEngine) NewPage(_ context.Context, _ Context, _ string) (Page, error) {
	return Page{ID: "probe-page", SessionID: "probe-session"}, nil
}

func (f *hintProbeEngine) DisableScripts(_ context.Context, page Page) error {
	f.scriptsDisabledOn = append(f.scriptsDisabledOn, page.SessionID)
	return nil
}

func (f *hintProbeEngine) Evaluate(_ context.Context, page Page, expression string) (EvaluationResult, error) {
	switch expression {
	case "document.readyState":
		payload, _ := json.Marshal("complete")
		return EvaluationResult{Value: payload}, nil
	case "document.documentElement.outerHTML":
		html := f.probeHTML
		if html == "" {
			html = "<html><body><p>probe content</p></body></html>"
		}
		payload, _ := json.Marshal(html)
		return EvaluationResult{Value: payload}, nil
	}
	return EvaluationResult{}, nil
}

func newHintService(engine Engine) *NavigationService {
	return NewNavigationService(engine, Page{ID: "page", SessionID: "session"}, NavigationOptions{
		Timeout:        time.Second,
		PollInterval:   time.Millisecond,
		NetworkIdleFor: time.Millisecond,
		ProbeContext:   Context{ID: "ctx"},
	})
}

// TestJSRequiredStaticPage verifies the static-fixture acceptance case:
// identical rendering with JavaScript disabled reports js_required=false.
func TestJSRequiredStaticPage(t *testing.T) {
	enabledHTML := "<html><body><main><h1>Static fixture</h1><p>Deterministic document.</p></main></body></html>"
	fake := &hintProbeEngine{probeHTML: enabledHTML, disableSupported: true}
	service := newHintService(fake)

	result, err := service.JSRequired(context.Background(), "http://fixture.test/static", enabledHTML)
	if err != nil {
		t.Fatal(err)
	}
	if result.Required {
		t.Fatalf("result = %+v, want js_required=false for a static page", result)
	}
	if !strings.Contains(result.Reason, "static") {
		t.Errorf("reason = %q, want a static-content explanation", result.Reason)
	}
	if len(fake.scriptsDisabledOn) != 1 || fake.scriptsDisabledOn[0] != "probe-session" {
		t.Errorf("DisableScripts calls = %v, want exactly the probe session", fake.scriptsDisabledOn)
	}
}

// TestJSRequiredSPAPage verifies the SPA-fixture acceptance case: the probe
// (JavaScript disabled) renders the pre-hydration skeleton, so the page
// reports js_required=true with a reason.
func TestJSRequiredSPAPage(t *testing.T) {
	enabledHTML := "<html><body><main><h1>SPA</h1><div id=\"app\" data-hydrated=\"true\">Hydrated application content</div></main></body></html>"
	disabledHTML := "<html><body><main><h1>SPA</h1><div id=\"app\" data-hydrated=\"false\">Loading application…</div></main></body></html>"
	fake := &hintProbeEngine{probeHTML: disabledHTML, disableSupported: true}
	service := newHintService(fake)

	result, err := service.JSRequired(context.Background(), "http://fixture.test/spa", enabledHTML)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Required {
		t.Fatalf("result = %+v, want js_required=true for the SPA fixture", result)
	}
	if !strings.Contains(result.Reason, "differs") {
		t.Errorf("reason = %q, want a content-differs explanation", result.Reason)
	}
}

// TestJSRequiredIgnoresMarkupOnlyDifferences: attribute noise (e.g. a
// hydration marker) without body-text change must not count as JS-needed.
func TestJSRequiredIgnoresMarkupOnlyDifferences(t *testing.T) {
	enabledHTML := `<html><body><main><p>same text</p></main></body></html>`
	disabledHTML := `<html><body><main data-hydrated="false"><p>same text</p></main></body></html>`
	fake := &hintProbeEngine{probeHTML: disabledHTML, disableSupported: true}
	service := newHintService(fake)

	result, err := service.JSRequired(context.Background(), "http://fixture.test/static", enabledHTML)
	if err != nil {
		t.Fatal(err)
	}
	if result.Required {
		t.Fatalf("result = %+v, want js_required=false when only markup differs", result)
	}
}

// TestJSRequiredRequiresProbeContext: without a configured probe context the
// hint fails loudly instead of guessing.
func TestJSRequiredRequiresProbeContext(t *testing.T) {
	fake := &hintProbeEngine{disableSupported: true}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{Timeout: time.Second})
	if _, err := service.JSRequired(context.Background(), "http://fixture.test/static", "<html></html>"); err == nil {
		t.Fatal("JSRequired without a probe context must fail")
	}
}

// TestJSRequiredRequiresScriptDisabler: engines without the extension fail
// loudly.
func TestJSRequiredRequiresScriptDisabler(t *testing.T) {
	fake := &fakeNavigationEngine{}
	service := newHintService(fake)
	if _, err := service.JSRequired(context.Background(), "http://fixture.test/static", "<html></html>"); err == nil {
		t.Fatal("JSRequired without ScriptDisabler must fail")
	}
}

// TestJSRequiredRequiresURL: the hint is meaningless without the target URL.
func TestJSRequiredRequiresURL(t *testing.T) {
	fake := &hintProbeEngine{disableSupported: true}
	service := newHintService(fake)
	if _, err := service.JSRequired(context.Background(), "  ", "<html></html>"); err == nil {
		t.Fatal("JSRequired without a URL must fail")
	}
}
