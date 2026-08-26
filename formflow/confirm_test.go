package formflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestConfirmLinkSuccess(t *testing.T) {
	driver := newFakeDriver()
	driver.text = "One last step. Confirm your erasure request."
	runner := &Runner{Driver: driver}

	result, err := runner.ConfirmLink(context.Background(), ConfirmationSpec{LinkURL: "https://broker.example/confirm?token=abc"})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeSuccess {
		t.Fatalf("code = %q, want success (%s)", result.Code, result.Message)
	}
	if len(driver.clicks) == 0 {
		t.Fatal("a confirmation control must have been clicked")
	}
	if result.Evidence == nil || len(result.Evidence.PostSubmitScreenshot) == 0 {
		t.Fatal("confirmation evidence must capture the final state")
	}
}

func TestConfirmLinkNoControl(t *testing.T) {
	driver := newFakeDriver()
	for _, candidate := range DefaultConfirmCandidates {
		driver.clickErrs["label "+candidate] = fmt.Errorf("%w: %s", ErrElementNotFound, candidate)
		driver.clickErrs["text "+candidate] = fmt.Errorf("%w: %s", ErrElementNotFound, candidate)
	}
	runner := &Runner{Driver: driver}

	result, err := runner.ConfirmLink(context.Background(), ConfirmationSpec{LinkURL: "https://broker.example/confirm"})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeConfirmationFailed {
		t.Fatalf("code = %q, want confirmation_failed", result.Code)
	}
}

func TestConfirmLinkBlocked(t *testing.T) {
	driver := newFakeDriver()
	driver.text = "Checking your browser before accessing the site"
	runner := &Runner{Driver: driver}

	result, err := runner.ConfirmLink(context.Background(), ConfirmationSpec{LinkURL: "https://broker.example/confirm"})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeBlockedBotwall {
		t.Fatalf("code = %q, want blocked_botwall", result.Code)
	}
}

func TestConfirmLinkNavigationError(t *testing.T) {
	driver := newFakeDriver()
	driver.navErr = errors.New("net::ERR_CONNECTION_RESET")
	runner := &Runner{Driver: driver}

	result, err := runner.ConfirmLink(context.Background(), ConfirmationSpec{LinkURL: "https://broker.example/confirm"})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeInteractionFailed {
		t.Fatalf("code = %q, want interaction_failed", result.Code)
	}
}

func TestConfirmLinkSuccessURLNotReached(t *testing.T) {
	driver := newFakeDriver()
	driver.text = "One last step. Confirm your erasure request."
	driver.waitErr = fmt.Errorf("wait timed out")
	runner := &Runner{Driver: driver}

	result, err := runner.ConfirmLink(context.Background(), ConfirmationSpec{
		LinkURL:        "https://broker.example/confirm?token=abc",
		SuccessURLGlob: "**/confirmed",
	})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeConfirmationFailed {
		t.Fatalf("code = %q, want confirmation_failed", result.Code)
	}
}

func TestConfirmLinkPacingInterrupted(t *testing.T) {
	driver := newFakeDriver()
	pacer := NewPacer(time.Hour)
	pacer.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	_ = pacer.Wait(context.Background(), "broker.example") // records the pacing window
	runner := &Runner{Driver: driver, Pacer: pacer}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := runner.ConfirmLink(ctx, ConfirmationSpec{LinkURL: "https://broker.example/confirm"})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeNavigationTimeout || result.FailedStep != "pace" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestConfirmLinkInteractionError(t *testing.T) {
	driver := newFakeDriver()
	// First candidate click fails with a non-not-found error → interaction_failed.
	driver.clickErrs["label confirm"] = errors.New("element detached")
	runner := &Runner{Driver: driver}

	result, err := runner.ConfirmLink(context.Background(), ConfirmationSpec{LinkURL: "https://broker.example/confirm"})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeInteractionFailed {
		t.Fatalf("code = %q, want interaction_failed", result.Code)
	}
}

func TestConfirmLinkEmptyURL(t *testing.T) {
	runner := &Runner{Driver: newFakeDriver()}
	result, err := runner.ConfirmLink(context.Background(), ConfirmationSpec{})
	if err != nil {
		t.Fatalf("ConfirmLink: %v", err)
	}
	if result.Code != CodeInvalidSpec {
		t.Fatalf("code = %q, want invalid_spec", result.Code)
	}
}
