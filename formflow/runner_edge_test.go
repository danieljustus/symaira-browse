package formflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestSubmitFormInteractionFailure: a navigation failure that is not a
// timeout maps to interaction_failed, and a submit control that vanished
// maps to form_not_found — both loud and typed.
func TestSubmitFormNavigationErrorIsInteractionFailed(t *testing.T) {
	driver := newFakeDriver()
	driver.navErr = errors.New("net::ERR_NAME_NOT_RESOLVED")
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeInteractionFailed {
		t.Fatalf("code = %q, want interaction_failed", result.Code)
	}
	if result.FailedStep != "navigate" {
		t.Fatalf("failed step = %q, want navigate", result.FailedStep)
	}
}

func TestSubmitFormSubmitControlVanished(t *testing.T) {
	driver := newFakeDriver()
	driver.fillable["label Email"] = true
	driver.fillable["label Phone"] = true
	driver.clickErrs["text Send request"] = fmt.Errorf("%w: %s", ErrElementNotFound, "text Send request")
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeFormNotFound {
		t.Fatalf("code = %q, want form_not_found", result.Code)
	}
}

func TestSubmitFormCustomTimeout(t *testing.T) {
	// A spec-provided timeout must be honored (bounded per-run timeout).
	driver := newFakeDriver()
	driver.timeout = true
	runner := &Runner{Driver: driver}

	spec := specFor("broker.example")
	spec.Timeout = 100 * time.Millisecond
	result, err := runner.SubmitForm(context.Background(), spec)
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeNavigationTimeout {
		t.Fatalf("code = %q, want navigation_timeout", result.Code)
	}
}

func TestRunnerNilDriver(t *testing.T) {
	var runner *Runner
	if _, err := runner.SubmitForm(context.Background(), specFor("x.example")); err == nil {
		t.Fatal("nil runner must error")
	}

	runner = &Runner{Driver: newFakeDriver()}
	pacer := NewPacer(time.Hour)
	pacer.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	_ = pacer.Wait(context.Background(), "x.example") // records the pacing window
	runner.Pacer = pacer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runner.SubmitForm(ctx, specFor("x.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeNavigationTimeout || result.FailedStep != "pace" {
		t.Fatalf("paced-out run: %+v", result)
	}
}

// TestOutcomeErrorStepFormat pins the step-only error format.
func TestOutcomeErrorStepFormat(t *testing.T) {
	outcome := Outcome{Code: CodeSubmitFailed, Step: "wait", Message: "no success url"}
	if got := outcome.Error(); got != "submit_failed: wait: no success url" {
		t.Fatalf("unexpected error format: %q", got)
	}
}
