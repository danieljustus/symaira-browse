package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

// Diagnosis is the structured failure payload for one flow step. It is
// designed to be understandable without access to the page and sufficient for
// an agent to repair the flow itself.
type Diagnosis struct {
	StepIndex        int    `json:"step_index"`
	Action           string `json:"action"`
	Selector         string `json:"selector,omitempty"`
	Expected         string `json:"expected,omitempty"`
	Actual           string `json:"actual,omitempty"`
	Message          string `json:"message"`
	SnapshotDiff     string `json:"snapshot_diff,omitempty"`
	RepairSuggestion string `json:"repair_suggestion,omitempty"`
}

// diagnoseFailure builds a Diagnosis for a failed step. It is best-effort:
// snapshot diff and actual-state lookups may fail (e.g. engine already
// closed) and are then omitted rather than failing the diagnosis.
func diagnoseFailure(ctx context.Context, executor Executor, session string, index int, step *Step, stepErr error) Diagnosis {
	diagnosis := Diagnosis{
		StepIndex: index,
		Action:    step.Action(),
		Message:   stepErr.Error(),
		Selector:  stepSelector(step),
		Expected:  stepExpected(step),
	}
	diagnosis.Actual = observeActual(ctx, executor, session, step)
	diagnosis.SnapshotDiff = observeSnapshotDiff(ctx, executor, session)
	diagnosis.RepairSuggestion = repairSuggestion(step, stepErr, diagnosis)
	return diagnosis
}

// stepSelector extracts the human-readable selector of a step.
func stepSelector(step *Step) string {
	switch {
	case step.Open != nil:
		return step.Open.URL
	case step.Find != nil:
		return finderDescription(step.Find)
	case step.Click != nil:
		return selectorDescription(step.Click.Label, step.Click.Role, step.Click.Text, step.Click.Name)
	case step.Fill != nil:
		return selectorDescription(step.Fill.Label, step.Fill.Role, step.Fill.Text, step.Fill.Name)
	case step.Wait != nil:
		return waitDescription(step.Wait)
	case step.Assert != nil:
		return assertDescription(step.Assert)
	default:
		return ""
	}
}

// stepExpected describes what the step expected to observe.
func stepExpected(step *Step) string {
	switch {
	case step.Assert != nil:
		return assertDescription(step.Assert)
	case step.Wait != nil:
		return waitDescription(step.Wait)
	case step.Find != nil && step.Find.Action != "":
		return fmt.Sprintf("%s %s", step.Find.Action, finderDescription(step.Find))
	case step.Click != nil:
		return "element clickable"
	case step.Fill != nil:
		return "element fillable"
	default:
		return ""
	}
}

// observeActual reads the current observable state that the step cared about.
func observeActual(ctx context.Context, executor Executor, session string, step *Step) string {
	if step.Assert != nil && step.Assert.URL != "" {
		if value, err := readOutputString(ctx, executor, session, "get.url", map[string]any{}); err == nil {
			return "url=" + value
		}
	}
	if step.Assert != nil && step.Assert.Visible != "" {
		return "element not found"
	}
	if step.Wait != nil {
		if value, err := readOutputString(ctx, executor, session, "get.url", map[string]any{}); err == nil {
			return "url=" + value
		}
	}
	return ""
}

// observeSnapshotDiff requests the daemon's snapshot diff against the last
// successful snapshot of the session.
func observeSnapshotDiff(ctx context.Context, executor Executor, session string) string {
	raw, err := json.Marshal(engine.SnapshotOptions{Diff: true})
	if err != nil {
		return ""
	}
	response, err := executor(ctx, daemon.Frame{Cmd: "snapshot", Args: raw, Session: session})
	if err != nil || !response.Success {
		return ""
	}
	payload, err := json.Marshal(response.Data)
	if err != nil {
		return ""
	}
	var result struct {
		Tree string `json:"tree"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return ""
	}
	return strings.TrimSpace(result.Tree)
}

// repairSuggestion derives a concrete repair hint from the failure.
func repairSuggestion(step *Step, stepErr error, diagnosis Diagnosis) string {
	message := stepErr.Error()
	lower := strings.ToLower(message)
	switch {
	case step.Wait != nil:
		return "the wait condition was not met; extend the wait or adjust the condition (url/visible/ms)"
	case step.Assert != nil:
		return "the assertion failed; compare the expected state with the snapshot diff and adjust the selector or the assertion"
	case step.Open != nil:
		if strings.Contains(lower, "not allowed") {
			return "the URL domain is outside the flow's domains list; add the domain or fix the URL"
		}
		return "the page could not be opened; check the URL and network reachability"
	case step.Find != nil || step.Click != nil || step.Fill != nil:
		switch {
		case strings.Contains(lower, "matched no elements") || strings.Contains(lower, "not found"):
			return fmt.Sprintf("selector %q matched no element; check the label/role/text against the current page (see snapshot diff)", diagnosis.Selector)
		case strings.Contains(lower, "matched %d") || strings.Contains(lower, "matched") && strings.Contains(lower, "elements"):
			return fmt.Sprintf("selector %q is ambiguous; add name=, exact: true, or a more specific role to disambiguate", diagnosis.Selector)
		case strings.Contains(lower, "obstructed") || strings.Contains(lower, "covered") || strings.Contains(lower, "overlay"):
			return "another element covers the target; use a more specific selector or dismiss the overlay first"
		case strings.Contains(lower, "stale"):
			return "the element ref is stale; the page changed between steps — add a wait or snapshot step before this one"
		default:
			return "verify the selector against the snapshot diff; the page may have changed"
		}
	default:
		return ""
	}
}

func finderDescription(find *FindStep) string {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"label", find.Label}, {"role", find.Role}, {"text", find.Text},
		{"placeholder", find.Placeholder}, {"alt", find.Alt}, {"title", find.Title},
		{"testid", find.TestID},
	} {
		if field.value != "" {
			return fmt.Sprintf("%s=%q", field.name, field.value)
		}
	}
	return ""
}

func selectorDescription(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func waitDescription(wait *WaitStep) string {
	switch {
	case wait.URL != "":
		return "url=" + wait.URL
	case wait.Visible != "":
		return "visible=" + wait.Visible
	default:
		return fmt.Sprintf("ms=%d", wait.Ms)
	}
}

func assertDescription(assert *AssertStep) string {
	switch {
	case assert.Visible != "":
		return "visible=" + assert.Visible
	case assert.URL != "":
		return "url=" + assert.URL
	case assert.Text != "":
		return "text=" + assert.Text
	case assert.Not != "":
		return "not=" + assert.Not
	default:
		return ""
	}
}
