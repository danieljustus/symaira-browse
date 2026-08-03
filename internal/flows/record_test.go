package flows

import (
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestGenerateDraftConvertsActions(t *testing.T) {
	actions := []RecordedAction{
		{Index: 0, Command: "open", Selector: "http://fixture.local/form"},
		{Index: 1, Command: "fill", Selector: "@e3", Value: "alice", Role: "textbox", Name: "Text"},
		{Index: 2, Command: "fill", Selector: "@e4", Value: "password123", Role: "textbox", Name: "Password"},
		{Index: 3, Command: "click", Selector: "@e9", Role: "button", Name: "Submit form"},
	}
	draft, err := GenerateDraft(actions, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if len(draft.Steps) != 4 {
		t.Fatalf("len(Steps) = %d, want 4", len(draft.Steps))
	}
	// open step
	if draft.Steps[0].Open == nil || draft.Steps[0].Open.URL != "http://fixture.local/form" {
		t.Errorf("step 0 = %+v, want open step", draft.Steps[0])
	}
	// fill with concrete value -> input reference
	if draft.Steps[1].Fill == nil || draft.Steps[1].Fill.Value != "{{input_1}}" {
		t.Errorf("step 1 = %+v, want fill with {{input_1}}", draft.Steps[1])
	}
	if draft.Steps[1].Fill.Role != "textbox" || draft.Steps[1].Fill.Name != "Text" {
		t.Errorf("step 1 selector = %q/%q, want textbox/Text", draft.Steps[1].Fill.Role, draft.Steps[1].Fill.Name)
	}
	// secret value -> op:// placeholder
	if draft.Steps[2].Fill == nil || !strings.HasPrefix(draft.Steps[2].Fill.Value, "op://recording/secret-") {
		t.Errorf("step 2 = %+v, want op:// secret placeholder", draft.Steps[2])
	}
	// click
	if draft.Steps[3].Click == nil || draft.Steps[3].Click.Role != "button" || draft.Steps[3].Click.Name != "Submit form" {
		t.Errorf("step 3 = %+v, want button click", draft.Steps[3])
	}
	// inputs declared
	if len(draft.Inputs) != 1 || draft.Inputs[0] != "input_1" {
		t.Errorf("Inputs = %v, want [input_1]", draft.Inputs)
	}
	// domains from URLs
	if len(draft.Domains) != 1 || draft.Domains[0] != "fixture.local" {
		t.Errorf("Domains = %v, want [fixture.local]", draft.Domains)
	}
	// secret refs recorded
	if len(draft.SecretRefs) != 1 {
		t.Errorf("SecretRefs = %v, want 1 entry", draft.SecretRefs)
	}
}

func TestGenerateDraftNoRefsInYAML(t *testing.T) {
	actions := []RecordedAction{
		{Index: 0, Command: "open", Selector: "http://fixture.local/form"},
		{Index: 1, Command: "click", Selector: "@e12", Role: "button", Name: "Go"},
	}
	draft, err := GenerateDraft(actions, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	raw, err := draft.RenderYAML()
	if err != nil {
		t.Fatalf("RenderYAML: %v", err)
	}
	output := string(raw)
	if strings.Contains(output, "@e") {
		t.Errorf("draft contains session ref:\n%s", output)
	}
	if !strings.Contains(output, "#") {
		t.Errorf("draft contains no review comments:\n%s", output)
	}
	// the draft must be re-parseable and valid
	flow, err := Parse(raw, "draft")
	if err != nil {
		t.Fatalf("generated draft does not parse: %v\n%s", err, output)
	}
	if len(flow.Steps) != 2 {
		t.Errorf("parsed steps = %d, want 2", len(flow.Steps))
	}
}

func TestGenerateDraftObservedEndStateBecomesAssert(t *testing.T) {
	actions := []RecordedAction{
		{Index: 0, Command: "open", Selector: "http://fixture.local/form"},
		{Index: 1, Command: "click", Selector: "@e5", Role: "button", Name: "Submit", URL: "http://fixture.local/done"},
	}
	draft, err := GenerateDraft(actions, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	foundAssert := false
	for _, step := range draft.Steps {
		if step.Assert != nil && step.Assert.URL == "http://fixture.local/done" {
			foundAssert = true
		}
	}
	if !foundAssert {
		t.Errorf("no assert for observed end state; steps: %+v", draft.Steps)
	}
}

func TestGenerateDraftResolverConvertsRefs(t *testing.T) {
	actions := []RecordedAction{
		{Index: 0, Command: "open", Selector: "http://fixture.local/form"},
		{Index: 1, Command: "click", Selector: "@e7"},
	}
	resolver := func(ref string) (engine.SnapshotRef, bool) {
		if ref == "e7" {
			return engine.SnapshotRef{Role: "button", Name: "Resolved Button"}, true
		}
		return engine.SnapshotRef{}, false
	}
	draft, err := GenerateDraft(actions, resolver)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.Steps[1].Click == nil || draft.Steps[1].Click.Role != "button" || draft.Steps[1].Click.Name != "Resolved Button" {
		t.Errorf("step 1 = %+v, want resolved button click", draft.Steps[1])
	}
}

func TestGenerateDraftEmptyActions(t *testing.T) {
	if _, err := GenerateDraft(nil, nil); err == nil {
		t.Fatal("GenerateDraft accepted empty actions")
	}
}

func TestGenerateDraftSnapshotStep(t *testing.T) {
	actions := []RecordedAction{
		{Index: 0, Command: "open", Selector: "http://fixture.local/form"},
		{Index: 1, Command: "snapshot"},
	}
	draft, err := GenerateDraft(actions, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.Steps[1].Snapshot == nil || !draft.Steps[1].Snapshot.Compact {
		t.Errorf("step 1 = %+v, want compact snapshot", draft.Steps[1])
	}
}

func TestGenerateDraftSecretDetection(t *testing.T) {
	actions := []RecordedAction{
		{Index: 0, Command: "open", Selector: "http://fixture.local/login"},
		{Index: 1, Command: "fill", Selector: "@e2", Value: "Bearer abcdef123456", Role: "textbox", Name: "Token"},
	}
	draft, err := GenerateDraft(actions, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	value := draft.Steps[1].Fill.Value
	if !strings.HasPrefix(value, "op://recording/secret-") {
		t.Errorf("Fill.Value = %q, want op:// placeholder", value)
	}
}
