package flows

import (
	"context"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// The three intentionally broken flows required by #57: a selector that
// matches nothing, an ambiguous selector, and a failing assertion. Each
// diagnosis must be understandable without page access and actionable for an
// agent.

func TestDiagnosisSelectorMatchesNothing(t *testing.T) {
	executor := newFakeExecutor()
	executor.failures["find"] = "find label \"Missing\" matched no elements"
	flow := parseFlow(t, `name: broken-1
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - fill: { label: "Missing", value: "x" }
`)
	_, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run succeeded although selector matches nothing")
	}
	runErr, ok := err.(*RunError)
	if !ok {
		t.Fatalf("error type = %T, want *RunError", err)
	}
	if runErr.Diagnosis == nil {
		t.Fatal("Diagnosis is nil")
	}
	diagnosis := runErr.Diagnosis
	if diagnosis.StepIndex != 1 {
		t.Errorf("StepIndex = %d, want 1", diagnosis.StepIndex)
	}
	if diagnosis.Selector != "Missing" {
		t.Errorf("Selector = %q, want Missing", diagnosis.Selector)
	}
	if diagnosis.RepairSuggestion == "" {
		t.Error("RepairSuggestion is empty")
	}
	if !strings.Contains(diagnosis.RepairSuggestion, "matched no element") {
		t.Errorf("RepairSuggestion = %q, want no-element hint", diagnosis.RepairSuggestion)
	}
}

func TestDiagnosisAmbiguousSelector(t *testing.T) {
	executor := newFakeExecutor()
	executor.failures["find"] = "find label \"Text\" matched 3 elements; use --name, --exact, first, last, or nth"
	flow := parseFlow(t, `name: broken-2
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - click: { label: "Text" }
`)
	_, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run succeeded although selector is ambiguous")
	}
	runErr, ok := err.(*RunError)
	if !ok {
		t.Fatalf("error type = %T, want *RunError", err)
	}
	if runErr.Diagnosis == nil {
		t.Fatal("Diagnosis is nil")
	}
	suggestion := runErr.Diagnosis.RepairSuggestion
	if !strings.Contains(suggestion, "ambiguous") && !strings.Contains(suggestion, "exact") {
		t.Errorf("RepairSuggestion = %q, want ambiguity hint", suggestion)
	}
}

func TestDiagnosisFailedAssertion(t *testing.T) {
	executor := newFakeExecutor()
	executor.failures["find"] = "find text \"Never visible\" matched no elements"
	flow := parseFlow(t, `name: broken-3
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - assert: { visible: "Never visible" }
`)
	_, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run succeeded although assertion failed")
	}
	runErr, ok := err.(*RunError)
	if !ok {
		t.Fatalf("error type = %T, want *RunError", err)
	}
	diagnosis := runErr.Diagnosis
	if diagnosis == nil {
		t.Fatal("Diagnosis is nil")
	}
	if !strings.Contains(diagnosis.Expected, "visible=Never visible") {
		t.Errorf("Expected = %q, want visible=Never visible", diagnosis.Expected)
	}
	if !strings.Contains(diagnosis.RepairSuggestion, "assertion failed") {
		t.Errorf("RepairSuggestion = %q, want assertion hint", diagnosis.RepairSuggestion)
	}
}

func TestDiagnosisIncludesSnapshotDiff(t *testing.T) {
	executor := newFakeExecutor()
	executor.failures["find"] = "find label \"Missing\" matched no elements"
	executor.responses["snapshot"] = map[string]any{
		"tree":        "+ button \"Save\"\n- link \"Old\"\n",
		"snapshot_id": "snap-1",
	}
	flow := parseFlow(t, `name: broken-diff
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - fill: { label: "Missing", value: "x" }
`)
	_, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run succeeded although selector matches nothing")
	}
	runErr, ok := err.(*RunError)
	if !ok {
		t.Fatalf("error type = %T, want *RunError", err)
	}
	if runErr.Diagnosis == nil || runErr.Diagnosis.SnapshotDiff == "" {
		t.Fatal("Diagnosis.SnapshotDiff is empty; the repair hint needs the page diff")
	}
	if !strings.Contains(runErr.Diagnosis.SnapshotDiff, "Save") {
		t.Errorf("SnapshotDiff = %q, want the diff content", runErr.Diagnosis.SnapshotDiff)
	}
}

func TestDiagnosisDomainViolation(t *testing.T) {
	executor := newFakeExecutor()
	flow := parseFlow(t, `name: broken-domain
version: 1
domains: ["allowed.example.com"]
steps:
  - open: { url: "https://evil.example.net/x" }
`)
	_, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run allowed a foreign domain")
	}
	runErr, ok := err.(*RunError)
	if !ok {
		t.Fatalf("error type = %T, want *RunError", err)
	}
	if runErr.Diagnosis == nil || !strings.Contains(runErr.Diagnosis.RepairSuggestion, "domains") {
		t.Errorf("Diagnosis = %+v, want domain repair hint", runErr.Diagnosis)
	}
}

func TestDiagnosisUnderstandableWithoutPage(t *testing.T) {
	executor := newFakeExecutor()
	executor.failures["find"] = "find role \"textbox\" matched no elements"
	flow := parseFlow(t, `name: broken-4
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - fill: { role: "textbox", name: "User", value: "x" }
`)
	_, err := Run(context.Background(), executor.Execute, RunOptions{Flow: flow})
	if err == nil {
		t.Fatal("Run succeeded although selector matches nothing")
	}
	runErr := err.(*RunError)
	diagnosis := runErr.Diagnosis
	if diagnosis == nil {
		t.Fatal("Diagnosis is nil")
	}
	// The diagnosis must name the selector, the step and the repair action
	// without requiring page access.
	combined := diagnosis.Selector + " " + diagnosis.Message + " " + diagnosis.RepairSuggestion
	for _, required := range []string{"textbox", "selector", "snapshot diff"} {
		if !strings.Contains(strings.ToLower(combined), required) {
			t.Errorf("diagnosis lacks %q: %s", required, combined)
		}
	}
	if diagnosis.RepairSuggestion == "" {
		t.Error("RepairSuggestion is empty; an agent cannot repair the flow")
	}
}

var _ = daemon.SuccessResponse
