package formflow

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// Risk classes for the formflow actions, prepared per the repository risk
// policy (enforcement starts in milestone M4; the classes are constants now).
var (
	// RiskNavigate classifies page navigation.
	RiskNavigate = policy.ClassForCommand("open")
	// RiskFill classifies filling one form field.
	RiskFill = policy.ClassInteract
	// RiskSubmit classifies the submit click.
	RiskSubmit = policy.ClassSubmit
	// RiskEvidence classifies evidence capture (screenshots, page text).
	RiskEvidence = policy.ClassRead
)

// Evidence is the compliance trail of one run, captured at deterministic
// points: the pre-submit screenshot is taken immediately before the submit
// click, the post-submit screenshot after the post-submit wait resolves.
type Evidence struct {
	FinalURL             string `json:"final_url"`
	PageText             string `json:"page_text,omitempty"`
	PreSubmitScreenshot  []byte `json:"pre_submit_screenshot,omitempty"`
	PostSubmitScreenshot []byte `json:"post_submit_screenshot,omitempty"`
}

// Result is the machine-readable outcome of one automation run. Consumers
// switch on Code; Message/Hint are for humans and logs. Screenshots marshal
// as base64 in JSON.
type Result struct {
	Code        Code      `json:"code"`
	Message     string    `json:"message,omitempty"`
	FailedStep  string    `json:"failed_step,omitempty"`
	FailedField string    `json:"failed_field,omitempty"`
	Hint        string    `json:"hint,omitempty"`
	Skipped     []string  `json:"skipped_fields,omitempty"`
	Evidence    *Evidence `json:"evidence,omitempty"`
	DurationMS  int64     `json:"duration_ms"`
}

// Runner drives form automations over a Driver. A nil Pacer disables pacing;
// production consumers should keep the default pacer for respectful
// per-host intervals.
type Runner struct {
	Driver Driver
	Pacer  *Pacer
}

// NewRunner returns a Runner with the default pacing.
func NewRunner(driver Driver) *Runner {
	return &Runner{Driver: driver, Pacer: NewPacer(0)}
}

// SubmitForm runs one broker opt-out form end-to-end: pace, navigate, fill
// the field map, capture pre-submit evidence, submit, capture post-submit
// evidence. Every failure mode lands in Result.Code; the Go error return is
// reserved for programmer misuse (nil driver/spec), never for page outcomes.
func (r *Runner) SubmitForm(ctx context.Context, spec FormSpec) (*Result, error) {
	if r == nil || r.Driver == nil {
		return nil, errors.New("formflow: runner needs a driver")
	}
	started := time.Now()
	if err := spec.Validate(); err != nil {
		return failed(started, err.(Outcome)), nil //nolint:errcheck // Validate only returns Outcome
	}

	ctx, cancel := context.WithTimeout(ctx, spec.timeout())
	defer cancel()

	if r.Pacer != nil {
		if err := r.Pacer.Wait(ctx, hostOf(spec.StartURL)); err != nil {
			return r.fail(ctx, started, Outcome{
				Code:    CodeNavigationTimeout,
				Step:    "pace",
				Message: "pacing wait interrupted: " + err.Error(),
			}), nil
		}
	}

	if err := r.Driver.Navigate(ctx, spec.StartURL); err != nil {
		return r.fail(ctx, started, Outcome{
			Code:    classifyNavigation(err),
			Step:    "navigate",
			Message: err.Error(),
			Hint:    "check the URL, network reachability and the per-run timeout",
		}), nil
	}

	if code, err := r.detectBlock(ctx); err != nil {
		return r.fail(ctx, started, Outcome{Code: CodeInteractionFailed, Step: "detect", Message: err.Error()}), nil
	} else if code != "" {
		return r.fail(ctx, started, Outcome{
			Code:    code,
			Step:    "navigate",
			Message: "bot protection detected on the form page",
			Hint:    "route to the human task queue; symbrowse does not solve CAPTCHAs",
		}), nil
	}

	result := &Result{Code: CodeSuccess}
	var filled int
	for _, field := range spec.Fields {
		if field.Required && !formPresent(ctx, r.Driver) {
			return r.fail(ctx, started, Outcome{
				Code:    CodeFormNotFound,
				Step:    "fill",
				Message: "no fillable form on the page",
			}), nil
		}
		err := r.Driver.Fill(ctx, field.Selector, field.Value)
		switch {
		case err == nil:
			filled++
		case errors.Is(err, ErrElementNotFound):
			if field.Required {
				return r.fail(ctx, started, Outcome{
					Code:    CodeFieldNotFound,
					Step:    "fill",
					Field:   field.Name,
					Message: "required field could not be located — the broker page may have changed",
					Hint:    "update the field map for this broker; the request was NOT submitted",
				}), nil
			}
			result.Skipped = append(result.Skipped, field.Name)
		default:
			return r.fail(ctx, started, Outcome{
				Code:    CodeInteractionFailed,
				Step:    "fill",
				Field:   field.Name,
				Message: err.Error(),
			}), nil
		}
	}
	if filled == 0 && len(spec.Fields) > 0 {
		return r.fail(ctx, started, Outcome{
			Code:    CodeFormNotFound,
			Step:    "fill",
			Message: "no field of the field map matched the page",
		}), nil
	}

	// Deterministic capture point 1: immediately before the submit click.
	preShot, err := r.Driver.Screenshot(ctx)
	if err != nil {
		return r.fail(ctx, started, Outcome{Code: CodeInteractionFailed, Step: "evidence", Message: err.Error()}), nil
	}

	if err := r.Driver.Click(ctx, spec.Submit); err != nil {
		code := CodeSubmitFailed
		if errors.Is(err, ErrElementNotFound) {
			code = CodeFormNotFound
		}
		return r.fail(ctx, started, Outcome{Code: code, Step: "submit", Message: err.Error()}), nil
	}

	// Post-submit settle: verify the success URL when one is contracted,
	// otherwise wait for network idle on a best-effort basis.
	if spec.SuccessURLGlob != "" {
		if err := r.Driver.WaitForURL(ctx, spec.SuccessURLGlob); err != nil {
			return r.fail(ctx, started, Outcome{
				Code:    CodeSubmitFailed,
				Step:    "wait",
				Message: "did not reach the contracted success URL: " + err.Error(),
				Hint:    "the submission may have failed silently; treat as NOT submitted",
			}), nil
		}
	} else {
		_ = r.Driver.WaitSettled(ctx)
	}

	if code, err := r.detectBlock(ctx); err != nil {
		return r.fail(ctx, started, Outcome{Code: CodeInteractionFailed, Step: "detect", Message: err.Error()}), nil
	} else if code != "" {
		return r.fail(ctx, started, Outcome{
			Code:    code,
			Step:    "post-submit",
			Message: "bot protection appeared after submit",
			Hint:    "route to the human task queue; submission state is unknown",
		}), nil
	}

	// Deterministic capture point 2: after the post-submit wait resolved.
	postShot, _ := r.Driver.Screenshot(ctx)
	finalURL, _ := r.Driver.CurrentURL(ctx)
	pageText, _ := r.Driver.PageText(ctx)
	result.Evidence = &Evidence{
		FinalURL:             finalURL,
		PageText:             pageText,
		PreSubmitScreenshot:  preShot,
		PostSubmitScreenshot: postShot,
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

// fail captures best-effort evidence (screenshots, final URL, page text) for
// a failed run and returns the failed result. Evidence on failure is part of
// the compliance trail: it proves what the page actually showed.
func (r *Runner) fail(ctx context.Context, started time.Time, outcome Outcome) *Result {
	result := failed(started, outcome)
	if r == nil || r.Driver == nil {
		return result
	}
	evidence := &Evidence{}
	if url, err := r.Driver.CurrentURL(ctx); err == nil {
		evidence.FinalURL = url
	}
	if text, err := r.Driver.PageText(ctx); err == nil {
		evidence.PageText = text
	}
	if shot, err := r.Driver.Screenshot(ctx); err == nil {
		evidence.PostSubmitScreenshot = shot
	}
	result.Evidence = evidence
	return result
}

// detectBlock classifies the current page via the shared heuristics.
func (r *Runner) detectBlock(ctx context.Context) (Code, error) {
	text, err := r.Driver.PageText(ctx)
	if err != nil {
		return "", err
	}
	html, err := r.Driver.PageHTML(ctx)
	if err != nil {
		return "", err
	}
	return DetectBlock(text, html), nil
}

// failed builds a bare failed result without evidence.
func failed(started time.Time, outcome Outcome) *Result {
	return &Result{
		Code:        outcome.Code,
		Message:     outcome.Message,
		FailedStep:  outcome.Step,
		FailedField: outcome.Field,
		Hint:        outcome.Hint,
		DurationMS:  time.Since(started).Milliseconds(),
	}
}

// classifyNavigation maps a navigation error to its outcome code.
func classifyNavigation(err error) Code {
	var waitErr *engine.WaitTimeoutError
	if errors.As(err, &waitErr) || errors.Is(err, context.DeadlineExceeded) {
		return CodeNavigationTimeout
	}
	return CodeInteractionFailed
}

// formPresent reports whether the page still contains a form element.
func formPresent(ctx context.Context, driver Driver) bool {
	html, err := driver.PageHTML(ctx)
	if err != nil {
		return true // unknown is not "absent"; field resolution decides
	}
	return strings.Contains(strings.ToLower(html), "<form")
}

// hostOf extracts the host portion of a URL for per-host pacing.
func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}
	return parsed.Host
}
