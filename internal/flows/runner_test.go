package flows

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// fakeExecutor records frames and answers them deterministically.
type fakeExecutor struct {
	frames    []daemon.Frame
	responses map[string]any
	failures  map[string]string
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		responses: make(map[string]any),
		failures:  make(map[string]string),
	}
}

func (f *fakeExecutor) Execute(ctx context.Context, frame daemon.Frame) (daemon.Response, error) {
	f.frames = append(f.frames, frame)
	if message, ok := f.failures[frame.Cmd]; ok {
		return daemon.ErrorResponse("operation_failed", message), nil
	}
	if frame.Cmd == "find" {
		return daemon.SuccessResponse(map[string]any{"ref": "e1", "kind": "label", "query": "x"}, nil), nil
	}
	data, ok := f.responses[frame.Cmd]
	if !ok {
		data = map[string]any{"ok": true}
	}
	return daemon.SuccessResponse(data, nil), nil
}

func parseFlow(t *testing.T, source string) *Flow {
	t.Helper()
	flow, err := Parse([]byte(source), "test")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return flow
}

const formFlow = `name: form-flow
version: 1
domains: ["fixture.local"]
inputs: [user, pass]
steps:
  - open: { url: "http://fixture.local/form" }
  - fill: { label: "Text", value: "{{user}}" }
  - fill: { label: "Password", value: "{{pass}}" }
  - assert: { visible: "Submit form" }
  - click: { label: "Submit form" }
  - wait: { url: "**/done" }
outputs:
  - { name: final_url, from: url }
`

func TestRunSuccessWithInputs(t *testing.T) {
	executor := newFakeExecutor()
	executor.responses["get.url"] = "http://fixture.local/done"
	flow := parseFlow(t, formFlow)
	report, err := Run(context.Background(), executor.Execute, RunOptions{
		Flow:    flow,
		Inputs:  map[string]string{"user": "alice", "pass": "op://Vault/Item/password"},
		Session: "s1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Success {
		t.Fatalf("report.Success = false: %s", report.Error)
	}
	if report.FlowID == "" {
		t.Error("FlowID is empty")
	}
	if len(report.Steps) != 6 {
		t.Fatalf("len(Steps) = %d, want 6", len(report.Steps))
	}
	// Step order and actions
	wantActions := []string{"open", "fill", "fill", "assert", "click", "wait"}
	for index, want := range wantActions {
		if got := report.Steps[index].Action; got != want {
			t.Errorf("Steps[%d].Action = %q, want %q", index, got, want)
		}
	}
	// Risk classes: open navigate, fill interact, assert read, click interact, wait interact
	if report.Steps[0].RiskClass != policy.ClassNavigate {
		t.Errorf("open risk = %q, want navigate", report.Steps[0].RiskClass)
	}
	if report.Steps[1].RiskClass != policy.ClassInteract {
		t.Errorf("fill risk = %q, want interact", report.Steps[1].RiskClass)
	}
	if report.Steps[3].RiskClass != policy.ClassRead {
		t.Errorf("assert risk = %q, want read", report.Steps[3].RiskClass)
	}
	// Outputs extracted
	if report.Outputs["final_url"] != "http://fixture.local/done" {
		t.Errorf("final_url = %q", report.Outputs["final_url"])
	}
	// Executed commands in order: open, then per fill/click: find,
	// scrollintoview, action; assert (find), wait, get.url.
	var commands []string
	for _, frame := range executor.frames {
		commands = append(commands, frame.Cmd)
	}
	wantCommands := []string{"open", "find", "scrollintoview", "fill", "find", "scrollintoview", "fill", "find", "find", "scrollintoview", "click", "wait", "get.url"}
	if strings.Join(commands, ",") != strings.Join(wantCommands, ",") {
		t.Errorf("commands = %v, want %v", commands, wantCommands)
	}
}

func TestRunMissingInputAbortsBeforeExecution(t *testing.T) {
	executor := newFakeExecutor()
	flow := parseFlow(t, formFlow)
	_, err := Run(context.Background(), executor.Execute, RunOptions{
		Flow:   flow,
		Inputs: map[string]string{},
	})
	if err == nil {
		t.Fatal("Run succeeded without required inputs")
	}
	if !strings.Contains(err.Error(), "missing required inputs") {
		t.Errorf("error = %q, want missing inputs message", err.Error())
	}
	if len(executor.frames) != 0 {
		t.Errorf("executed %d frames, want 0 (abort before execution)", len(executor.frames))
	}
}

func TestRunDryRunReturnsPlanWithoutExecuting(t *testing.T) {
	executor := newFakeExecutor()
	flow := parseFlow(t, formFlow)
	report, err := Run(context.Background(), executor.Execute, RunOptions{
		Flow:    flow,
		Inputs:  map[string]string{"user": "alice", "pass": "op://Vault/Item/password"},
		DryRun:  true,
		Session: "s1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.DryRun {
		t.Error("report.DryRun = false")
	}
	if len(report.Plan) != 6 {
		t.Fatalf("len(Plan) = %d, want 6", len(report.Plan))
	}
	if report.Plan[0].RiskClass != policy.ClassNavigate || report.Plan[1].RiskClass != policy.ClassInteract {
		t.Errorf("plan risk classes wrong: %+v", report.Plan)
	}
	if len(executor.frames) != 0 {
		t.Errorf("dry-run executed %d frames, want 0", len(executor.frames))
	}
}

func TestRunDomainHardConstraint(t *testing.T) {
	executor := newFakeExecutor()
	flow := parseFlow(t, `name: evil
version: 1
domains: ["allowed.example.com"]
steps:
  - open: { url: "https://evil.example.net/phish" }
`)
	_, err := Run(context.Background(), executor.Execute, RunOptions{
		Flow:   flow,
		Inputs: map[string]string{},
	})
	if err == nil {
		t.Fatal("Run allowed a foreign domain")
	}
	runErr, ok := err.(*RunError)
	if !ok {
		t.Fatalf("error type = %T, want *RunError", err)
	}
	if runErr.StepIndex != 0 || !strings.Contains(runErr.Message, "not allowed") {
		t.Errorf("unexpected error: %+v", runErr)
	}
	// The domain gate itself runs before any step; the single frame is the
	// diagnosis' actual-state lookup (get.url), not a navigation.
	if len(executor.frames) > 1 {
		t.Errorf("executed %d frames, want at most 1 (domain gate + diagnosis lookup)", len(executor.frames))
	}
}

func TestRunDomainWildcardAllowed(t *testing.T) {
	executor := newFakeExecutor()
	flow := parseFlow(t, `name: wildcard
version: 1
domains: ["*.example.com"]
steps:
  - open: { url: "https://app.example.com/x" }
`)
	report, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Success {
		t.Errorf("report.Success = false: %s", report.Error)
	}
}

func TestRunStepFailureProducesDiagnosis(t *testing.T) {
	executor := newFakeExecutor()
	executor.failures["find"] = "no element matched the selector"
	flow := parseFlow(t, `name: fail
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - click: { label: "Missing button" }
`)
	report, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run succeeded although a step failed")
	}
	runErr, ok := err.(*RunError)
	if !ok {
		t.Fatalf("error type = %T, want *RunError", err)
	}
	if runErr.StepIndex != 1 {
		t.Errorf("StepIndex = %d, want 1", runErr.StepIndex)
	}
	if !report.Success {
		if report.Steps[1].Error == "" {
			t.Error("failed step has no error string")
		}
	}
	if runErr.Hint == "" {
		t.Error("diagnosis hint is empty")
	}
}

func TestRunAssertFailureAborts(t *testing.T) {
	executor := newFakeExecutor()
	executor.failures["find"] = "no element matched the selector"
	flow := parseFlow(t, `name: assert-fail
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - assert: { visible: "Never there" }
  - click: { label: "Unreachable" }
`)
	report, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run succeeded although assert failed")
	}
	if len(report.Steps) != 2 {
		t.Errorf("len(Steps) = %d, want 2 (abort at assert)", len(report.Steps))
	}
}

func TestRunSubstitutesInputsIntoFrames(t *testing.T) {
	executor := newFakeExecutor()
	flow := parseFlow(t, formFlow)
	_, err := Run(context.Background(), executor.Execute, RunOptions{
		Flow:    flow,
		Inputs:  map[string]string{"user": "alice", "pass": "op://Vault/Item/password"},
		Session: "s1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Find the first fill frame and check the value was substituted.
	for _, frame := range executor.frames {
		if frame.Cmd == "fill" {
			var request struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(frame.Args, &request); err == nil && request.Value == "alice" {
				return
			}
		}
	}
	t.Errorf("no frame carried the substituted input value; frames: %+v", executor.frames)
}

func TestRunSecretReferencePassedThrough(t *testing.T) {
	executor := newFakeExecutor()
	flow := parseFlow(t, formFlow)
	_, err := Run(context.Background(), executor.Execute, RunOptions{
		Flow:    flow,
		Inputs:  map[string]string{"user": "alice", "pass": "op://Vault/Item/password"},
		Session: "s1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, frame := range executor.frames {
		if frame.Cmd != "fill" {
			continue
		}
		var request struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(frame.Args, &request); err != nil {
			continue
		}
		if request.Value == "op://Vault/Item/password" {
			return
		}
	}
	t.Error("op:// reference was not passed through to the fill frame")
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"**/browse/*", "https://jira.example.com/browse/PROJ-1", true},
		{"**/browse/*", "https://jira.example.com/settings", false},
		{"https://example.com/*", "https://example.com/a/b", false},
		{"https://example.com/*", "https://example.com/a", true},
		{"exact", "exact", true},
		{"exact", "different", false},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.value); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestInputReferences(t *testing.T) {
	got := inputReferences("hello {{name}} and {{ name2 }} and plain")
	if len(got) != 2 || got[0] != "name" || got[1] != "name2" {
		t.Errorf("inputReferences = %v", got)
	}
	if refs := inputReferences("no refs"); len(refs) != 0 {
		t.Errorf("inputReferences(no refs) = %v", refs)
	}
}

func TestDomainAllowed(t *testing.T) {
	cases := []struct {
		host    string
		domains []string
		want    bool
	}{
		{"example.com", []string{"example.com"}, true},
		{"sub.example.com", []string{"example.com"}, false},
		{"sub.example.com", []string{"*.example.com"}, true},
		{"example.com", []string{"*.example.com"}, false},
		{"EXAMPLE.com", []string{"example.com"}, true},
		{"evil.com", []string{"example.com", "*.example.com"}, false},
	}
	for _, tc := range cases {
		if got := domainAllowed(tc.host, tc.domains); got != tc.want {
			t.Errorf("domainAllowed(%q, %v) = %v, want %v", tc.host, tc.domains, got, tc.want)
		}
	}
}

func TestFlowIDUniqueness(t *testing.T) {
	first := newFlowID()
	second := newFlowID()
	if first == second {
		t.Errorf("flow ids collide: %q", first)
	}
	if !strings.HasPrefix(first, "flow-") {
		t.Errorf("flow id %q lacks prefix", first)
	}
}

func TestRunFindSnapshotAndAssertions(t *testing.T) {
	executor := newFakeExecutor()
	executor.responses["get.url"] = "http://fixture.local/landing"
	flow := parseFlow(t, `name: probe-flow
version: 1
domains: ["fixture.local"]
steps:
  - open: { url: "http://fixture.local/landing" }
  - find: { role: "button", value: "{{term}}" }
  - snapshot: { compact: true }
  - assert: { visible: "Submit form" }
  - assert: { url: "**/landing" }
outputs:
  - { name: final_url, from: url }
`)
	report, err := Run(context.Background(), executor.Execute, RunOptions{
		Flow:    flow,
		Inputs:  map[string]string{"term": "search"},
		Session: "s1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Success {
		t.Fatalf("report.Success = false: %s", report.Error)
	}
	if len(report.Steps) != 5 {
		t.Fatalf("steps = %d, want 5", len(report.Steps))
	}
	// The find step must carry the substituted value in the Value field.
	found := false
	for _, frame := range executor.frames {
		if frame.Cmd == "find" {
			var request struct {
				Kind  string `json:"kind"`
				Query string `json:"query"`
				Value string `json:"value"`
			}
			_ = json.Unmarshal(frame.Args, &request)
			if request.Kind == "role" && request.Query == "button" && request.Value == "search" {
				found = true
			}
		}
	}
	if !found {
		t.Error("find step did not carry the substituted query value")
	}
}

func TestRunAssertNotSucceedsWhenAbsent(t *testing.T) {
	// The fake executor fails find for "spinner" -> the element is absent.
	executor := newFakeExecutor()
	executor.failures["find"] = "element not found"
	flow := parseFlow(t, `name: neg-flow
version: 1
domains: ["fixture.local"]
steps:
  - assert: { not: "spinner" }
`)
	report, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow, Session: "s1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Success {
		t.Fatalf("report.Success = false: %s", report.Error)
	}
}

func TestRunAssertNotFailsWhenPresent(t *testing.T) {
	executor := newFakeExecutor() // find always succeeds -> element present
	flow := parseFlow(t, `name: neg-flow
version: 1
domains: ["fixture.local"]
steps:
  - assert: { not: "spinner" }
`)
	report, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow, Session: "s1"})
	if err == nil {
		t.Fatal("expected assert-not failure")
	}
	if report.Success {
		t.Fatalf("report.Success = true, want failure (spinner is present in the fixture)")
	}
}

func TestRunAssertUrlMismatchFails(t *testing.T) {
	executor := newFakeExecutor()
	executor.responses["get.url"] = "http://fixture.local/other"
	flow := parseFlow(t, `name: url-flow
version: 1
domains: ["fixture.local"]
steps:
  - assert: { url: "**/expected" }
`)
	report, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow, Session: "s1"})
	if err == nil {
		t.Fatal("expected url-mismatch failure")
	}
	if report.Success {
		t.Fatalf("report.Success = true, want url-mismatch failure")
	}
}

func TestRunAssertCombinedSelectorsOK(t *testing.T) {
	// assert with multiple selectors is accepted and runs.
	flow := parseFlow(t, `name: bad-flow
version: 1
domains: ["fixture.local"]
steps:
  - assert: { visible: "x", url: "**/y" }
`)
	report, err := Run(context.Background(), newFakeExecutor().Execute, RunOptions{Flow: flow, Session: "s1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Success {
		t.Fatalf("report.Success = false: %s", report.Error)
	}
}
