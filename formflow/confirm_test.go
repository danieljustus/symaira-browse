package formflow

import (
	"context"
	"fmt"
	"testing"
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
