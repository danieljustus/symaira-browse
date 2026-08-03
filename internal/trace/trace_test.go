package trace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/journal"
)

// fakeTraceEngine implements Engine + InteractionEngine + NavigationState.
type fakeTraceEngine struct {
	openURL string
}

func (f *fakeTraceEngine) Launch(context.Context) error { return nil }
func (f *fakeTraceEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "ctx"}, nil
}
func (f *fakeTraceEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "page"}, nil
}
func (f *fakeTraceEngine) Navigate(_ context.Context, _ engine.Page, _ string) (engine.NavigationResult, error) {
	// The fake navigates to a fixed, deviating URL so replay reports a
	// mismatch for open steps.
	return engine.NavigationResult{}, nil
}
func (f *fakeTraceEngine) Evaluate(context.Context, engine.Page, string) (engine.EvaluationResult, error) {
	return engine.EvaluationResult{Value: json.RawMessage(`{"url":"` + f.openURL + `","http_status":200,"ready_state":"complete","network_idle":true}`)}, nil
}
func (f *fakeTraceEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) {
	return nil, nil
}
func (f *fakeTraceEngine) Screenshot(context.Context, engine.Page) ([]byte, error) { return nil, nil }
func (f *fakeTraceEngine) Close() error                                            { return nil }

func (f *fakeTraceEngine) ResolveElement(context.Context, engine.Page, string) (engine.InteractionTarget, error) {
	return engine.InteractionTarget{NodeID: "n1"}, nil
}
func (f *fakeTraceEngine) ScrollIntoView(context.Context, engine.Page, engine.InteractionTarget) error {
	return nil
}
func (f *fakeTraceEngine) PerformInteraction(context.Context, engine.Page, engine.InteractionTarget, engine.InteractionRequest) error {
	return nil
}

func sampleEntries() []journal.Entry {
	return []journal.Entry{
		{Command: "open", Args: map[string]any{"url": "https://example.com/form"}, RiskClass: "navigate", Result: "ok"},
		{Command: "fill", Args: map[string]any{"selector": "#user", "value": "ada"}, RiskClass: "interact", Result: "ok"},
		{Command: "fill", Args: map[string]any{"selector": "#pass", "value": "••••"}, RiskClass: "credential", Result: "ok"},
		{Command: "click", Args: map[string]any{"selector": "#submit"}, RiskClass: "submit", Result: "ok"},
		{Command: "open", Args: map[string]any{"url": "https://example.com/dashboard"}, RiskClass: "navigate", Result: "error:timeout"},
	}
}

func TestExportBuildsReplayableSteps(t *testing.T) {
	file := Export(sampleEntries(), "default")
	if file.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %d", file.SchemaVersion)
	}
	// open, fill#user, click survive; the credential fill and the failed
	// open are excluded.
	if len(file.Steps) != 3 {
		t.Fatalf("steps = %d (want 3)", len(file.Steps))
	}
	if file.Steps[0].Command != "open" || file.Steps[0].ExpectedURL != "https://example.com/form" {
		t.Fatalf("step0 = %#v", file.Steps[0])
	}
	if file.Steps[1].Selector != "#user" || file.Steps[1].Value != "ada" {
		t.Fatalf("step1 = %#v", file.Steps[1])
	}
}

func TestExportNeverContainsSecrets(t *testing.T) {
	file := Export(sampleEntries(), "default")
	raw, _ := json.Marshal(file)
	if string(raw) == "" {
		t.Fatal("empty export")
	}
	// The password field in the journal was already redacted (••••); export
	// must not invent or restore any value, and credential-class steps are
	// skipped entirely.
	for _, step := range file.Steps {
		if step.Command == "fill" && step.Selector == "#pass" {
			t.Fatalf("password step exported: %#v", step)
		}
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	file := Export(sampleEntries(), "default")
	path := t.TempDir() + "/trace.json"
	if err := Write(path, file); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != len(file.Steps) || got.Session != "default" {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}

func TestReadRejectsForeignSchema(t *testing.T) {
	path := t.TempDir() + "/bad.json"
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"steps":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("foreign schema accepted")
	}
}

func TestReplayReportsDeviations(t *testing.T) {
	fake := &fakeTraceEngine{
		openURL: "https://other.example.com",
	}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	file := Export(sampleEntries(), "default")
	result := Replay(context.Background(), service, file)
	if result.Total != 3 {
		t.Fatalf("total = %d", result.Total)
	}
	// open deviates (fake returns another URL); the fill/click steps match.
	if result.Deviated != 1 || result.Matched != 2 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	for _, outcome := range result.Outcomes {
		if outcome.Index == 0 {
			if outcome.Matched || outcome.ActualURL != "https://other.example.com" || outcome.ExpectedURL != "https://example.com/form" {
				t.Fatalf("deviation outcome = %#v", outcome)
			}
		}
	}
}

func TestReplayCredentialStepNotExecuted(t *testing.T) {
	fake := &fakeTraceEngine{}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	file := &File{SchemaVersion: SchemaVersion, Steps: []Step{{Command: "auth.login", Value: "myapp"}}}
	result := Replay(context.Background(), service, file)
	if result.Failed != 1 || result.Outcomes[0].Error == "" {
		t.Fatalf("result = %#v", result)
	}
	if !errors.Is(ErrNoSteps, ErrNoSteps) {
		t.Fatal("sanity")
	}
}

func TestNormalizeURL(t *testing.T) {
	if normalizeURL("https://x.com/a/") != normalizeURL("https://x.com/a") {
		t.Fatal("trailing slash not normalized")
	}
	if normalizeURL("https://x.com/a#frag") != "https://x.com/a" {
		t.Fatal("fragment not stripped")
	}
}
