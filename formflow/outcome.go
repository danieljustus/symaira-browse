// Package formflow defines the consumable web-form automation contract for
// downstream Symaira products (first consumer: symaira-eraseme, issue #280).
// It exposes a stable, typed Go API for driving hostile data-broker opt-out
// forms: navigate, fill from a field map, submit, capture compliance evidence
// at deterministic points, and classify failures into distinguishable
// outcomes instead of opaque error strings.
//
// The package is deliberately public: consumers import it as a normal Go
// module dependency (the same way symaira-corekit is consumed). Chrome access
// stays behind the internal engine boundary; the only runtime requirement is
// a Chrome/Chromium binary.
package formflow

import "fmt"

// Code is the machine-readable outcome of a form automation run. Consumers
// must switch on Code, never on Message text: messages are for humans and may
// change, codes are part of the contract and only ever grow additively.
type Code string

const (
	// CodeSuccess means the flow completed and evidence was captured.
	CodeSuccess Code = "success"
	// CodeInvalidSpec means the supplied spec failed validation before any
	// browser interaction happened.
	CodeInvalidSpec Code = "invalid_spec"
	// CodeNavigationTimeout means the target page did not load within the
	// bounded per-run timeout.
	CodeNavigationTimeout Code = "navigation_timeout"
	// CodeFormNotFound means no fillable form was found on the page.
	CodeFormNotFound Code = "form_not_found"
	// CodeFieldNotFound means a required field from the field map could not
	// be located on the page. This is always loud: a broker renaming a field
	// must never result in a silently half-filled submission.
	CodeFieldNotFound Code = "field_not_found"
	// CodeBlockedCaptcha means a CAPTCHA challenge was detected. Solving is
	// deliberately out of scope; consumers route this to a human task queue.
	CodeBlockedCaptcha Code = "blocked_captcha"
	// CodeBlockedBotwall means a bot-protection wall (rate page, browser
	// check, access denial) was detected without an interactive CAPTCHA.
	CodeBlockedBotwall Code = "blocked_botwall"
	// CodeInteractionFailed means a field fill or click failed for reasons
	// other than a missing element (overlay interception, detached node, ...).
	CodeInteractionFailed Code = "interaction_failed"
	// CodeSubmitFailed means the submit action itself failed.
	CodeSubmitFailed Code = "submit_failed"
	// CodeConfirmationFailed means a confirmation-link flow did not reach a
	// verifiable confirmed state.
	CodeConfirmationFailed Code = "confirmation_failed"
)

// Outcome is the structured result classification of one run or step.
type Outcome struct {
	Code    Code   `json:"code"`
	Step    string `json:"step,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (o Outcome) Error() string {
	if o.Field != "" {
		return fmt.Sprintf("%s: field %q: %s", o.Code, o.Field, o.Message)
	}
	if o.Step != "" {
		return fmt.Sprintf("%s: %s: %s", o.Code, o.Step, o.Message)
	}
	return fmt.Sprintf("%s: %s", o.Code, o.Message)
}

// Blocked reports whether the outcome is a bot-protection stop that a human
// task queue should handle (CAPTCHA or bot wall).
func (o Outcome) Blocked() bool {
	return o.Code == CodeBlockedCaptcha || o.Code == CodeBlockedBotwall
}
