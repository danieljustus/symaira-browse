package flows

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

// TestA11yAuditEndToEnd runs a real axe-core audit against a deliberately
// inaccessible fixture page (missing label, low contrast) and verifies the
// violations[] payload.
func TestA11yAuditEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e-a11y"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, daemon.NavigationRuntimeOptions{})
	defer func() { _ = runtime.Close() }()
	executor := runtimeExecutor(runtime)

	// A deliberately inaccessible page: unlabeled input, missing lang,
	// low-contrast text. wcag2a must flag these.
	brokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Broken a11y</title></head>
<body><h1>Broken</h1><input type="text"><p style="color:#eee;background:#fff">invisible text</p></body></html>`))
	}))
	defer brokenServer.Close()
	if _, err := executor(ctx, frame("open", map[string]any{"url": brokenServer.URL}, "e2e-a11y")); err != nil {
		t.Fatalf("open: %v", err)
	}
	response, err := executor(ctx, frame("a11y", map[string]any{"tags": []string{"wcag2a"}}, "e2e-a11y"))
	if err != nil {
		t.Fatalf("a11y: %v", err)
	}
	raw, _ := json.Marshal(response.Data)
	var result engine.A11yResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode a11y result: %v\n%s", err, raw)
	}
	if result.AxeVersion == "" {
		t.Errorf("AxeVersion is empty (must be reported per issue #61)")
	}
	if !strings.HasPrefix(result.AxeVersion, "4.") {
		t.Errorf("AxeVersion = %q, want 4.x", result.AxeVersion)
	}
	if result.ViolationCount == 0 {
		t.Fatalf("no violations on deliberately broken page (passes=%d): audit did not run or fixture changed", result.Passes)
	}
	// The result must be bounded (token budget): violations carry targets and
	// impact, not full page dumps.
	for _, violation := range result.Violations {
		if violation.ID == "" || violation.Impact == "" {
			t.Errorf("violation lacks id/impact: %+v", violation)
		}
	}
}

// TestA11yAuditWithoutEngine verifies the capability error path: engines
// without the A11yAuditor extension fail clearly.
func TestA11yAuditWithoutEngine(t *testing.T) {
	service := engine.NewNavigationService(&noA11yEngine{}, engine.Page{}, engine.NavigationOptions{})
	_, err := service.Audit(t.Context(), engine.A11yOptions{})
	if err == nil {
		t.Fatal("Audit succeeded without an A11yAuditor engine")
	}
	if !strings.Contains(err.Error(), "does not support accessibility audits") {
		t.Errorf("error = %v, want capability message", err)
	}
}

// noA11yEngine is a minimal engine stub without the audit extension.
type noA11yEngine struct{}

func (n *noA11yEngine) Launch(context.Context) error { return nil }
func (n *noA11yEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{}, nil
}
func (n *noA11yEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{}, nil
}
func (n *noA11yEngine) Navigate(context.Context, engine.Page, string) (engine.NavigationResult, error) {
	return engine.NavigationResult{}, nil
}
func (n *noA11yEngine) Evaluate(context.Context, engine.Page, string) (engine.EvaluationResult, error) {
	return engine.EvaluationResult{}, nil
}
func (n *noA11yEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) { return nil, nil }
func (n *noA11yEngine) Screenshot(context.Context, engine.Page) ([]byte, error)      { return nil, nil }
func (n *noA11yEngine) Close() error                                                 { return nil }
