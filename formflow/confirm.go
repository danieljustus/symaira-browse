package formflow

import (
	"context"
	"errors"
	"time"
)

// DefaultConfirmCandidates are the control labels/texts tried, in order, to
// find a broker's confirmation control. German and English are both covered
// because broker emails arrive in both languages.
var DefaultConfirmCandidates = []string{
	"confirm",
	"bestätigen",
	"verify",
	"activate",
	"confirm email",
	"abschließen",
	"weiter",
}

// ConfirmationSpec describes one confirmation-link flow: open the link from
// the broker email, click the confirmation control through, and report a
// verifiable outcome (issue #281).
type ConfirmationSpec struct {
	// LinkURL is the confirmation link extracted from the broker email.
	LinkURL string `json:"link_url"`
	// Candidates overrides the confirmation-control search terms.
	Candidates []string `json:"candidates,omitempty"`
	// SuccessURLGlob, when set, must match the URL after confirmation.
	SuccessURLGlob string `json:"success_url_glob,omitempty"`
	// Timeout bounds the flow; DefaultRunTimeout applies when zero.
	Timeout time.Duration `json:"timeout_ms,omitempty"`
}

// ConfirmLink runs one confirmation-link flow end-to-end and captures
// evidence at the final state. The result distinguishes: confirmed
// (CodeSuccess), blocked by bot protection (CodeBlocked*), no confirmation
// control found or unverifiable (CodeConfirmationFailed).
func (r *Runner) ConfirmLink(ctx context.Context, spec ConfirmationSpec) (*Result, error) {
	if r == nil || r.Driver == nil {
		return nil, errors.New("formflow: runner needs a driver")
	}
	started := time.Now()
	if spec.LinkURL == "" {
		return failed(started, Outcome{Code: CodeInvalidSpec, Message: "link_url is required"}), nil
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultRunTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if r.Pacer != nil {
		if err := r.Pacer.Wait(ctx, hostOf(spec.LinkURL)); err != nil {
			return r.fail(ctx, started, Outcome{Code: CodeNavigationTimeout, Step: "pace", Message: err.Error()}), nil
		}
	}

	if err := r.Driver.Navigate(ctx, spec.LinkURL); err != nil {
		return r.fail(ctx, started, Outcome{
			Code:    classifyNavigation(err),
			Step:    "navigate",
			Message: err.Error(),
		}), nil
	}

	if code, err := r.detectBlock(ctx); err != nil {
		return r.fail(ctx, started, Outcome{Code: CodeInteractionFailed, Step: "detect", Message: err.Error()}), nil
	} else if code != "" {
		return r.fail(ctx, started, Outcome{
			Code:    code,
			Step:    "navigate",
			Message: "bot protection detected on the confirmation page",
			Hint:    "route to the human task queue",
		}), nil
	}

	candidates := spec.Candidates
	if len(candidates) == 0 {
		candidates = DefaultConfirmCandidates
	}
	clicked := false
	for _, candidate := range candidates {
		for _, sel := range []Selector{{Label: candidate}, {Text: candidate}} {
			if err := r.Driver.Click(ctx, sel); err == nil {
				clicked = true
				break
			} else if !errors.Is(err, ErrElementNotFound) {
				return r.fail(ctx, started, Outcome{Code: CodeInteractionFailed, Step: "confirm", Message: err.Error()}), nil
			}
		}
		if clicked {
			break
		}
	}
	if !clicked {
		return r.fail(ctx, started, Outcome{
			Code:    CodeConfirmationFailed,
			Step:    "confirm",
			Message: "no confirmation control found on the page",
			Hint:    "the broker confirmation page may have changed; handle manually",
		}), nil
	}

	if spec.SuccessURLGlob != "" {
		if err := r.Driver.WaitForURL(ctx, spec.SuccessURLGlob); err != nil {
			return r.fail(ctx, started, Outcome{
				Code:    CodeConfirmationFailed,
				Step:    "wait",
				Message: "did not reach the contracted confirmed URL: " + err.Error(),
			}), nil
		}
	} else {
		_ = r.Driver.WaitSettled(ctx)
	}

	if code, err := r.detectBlock(ctx); err != nil {
		return r.fail(ctx, started, Outcome{Code: CodeInteractionFailed, Step: "detect", Message: err.Error()}), nil
	} else if code != "" {
		return r.fail(ctx, started, Outcome{Code: code, Step: "post-confirm", Message: "bot protection appeared after confirmation"}), nil
	}

	postShot, _ := r.Driver.Screenshot(ctx)
	finalURL, _ := r.Driver.CurrentURL(ctx)
	pageText, _ := r.Driver.PageText(ctx)
	return &Result{
		Code: CodeSuccess,
		Evidence: &Evidence{
			FinalURL:             finalURL,
			PageText:             pageText,
			PostSubmitScreenshot: postShot,
		},
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}
