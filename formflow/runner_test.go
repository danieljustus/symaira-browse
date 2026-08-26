package formflow

import (
	"context"
	"fmt"
	"testing"
)

// fakeDriver is a scripted Driver for runner tests. It records every call and
// answers from its configured state.
type fakeDriver struct {
	url     string
	text    string
	html    string
	shots   [][]byte
	navErr  error
	timeout bool

	fillable  map[string]bool // describe(sel) -> resolvable
	fills     []string
	clicks    []string
	clickErrs map[string]error
	waitErr   error

	// postSubmit swaps page state after a successful submit click.
	postText string
	postURL  string
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		html:      `<html><body><form><input name="email"></form></body></html>`,
		text:      "Opt-out form",
		shots:     [][]byte{[]byte("png-1"), []byte("png-2")},
		fillable:  map[string]bool{},
		clickErrs: map[string]error{},
	}
}

func (f *fakeDriver) Navigate(_ context.Context, url string) error {
	if f.navErr != nil {
		return f.navErr
	}
	if f.timeout {
		return context.DeadlineExceeded
	}
	f.url = url
	return nil
}

func (f *fakeDriver) CurrentURL(context.Context) (string, error) {
	if f.postURL != "" {
		return f.postURL, nil
	}
	return f.url, nil
}

func (f *fakeDriver) PageText(context.Context) (string, error) {
	if f.postText != "" {
		return f.postText, nil
	}
	return f.text, nil
}

func (f *fakeDriver) PageHTML(context.Context) (string, error) { return f.html, nil }

func (f *fakeDriver) Screenshot(context.Context) ([]byte, error) {
	if len(f.shots) == 0 {
		return nil, fmt.Errorf("no screenshot")
	}
	shot := f.shots[0]
	f.shots = f.shots[1:]
	return shot, nil
}

func (f *fakeDriver) Fill(_ context.Context, sel Selector, value string) error {
	key := describe(sel)
	if ok, known := f.fillable[key]; known && !ok {
		return fmt.Errorf("%w: %s", ErrElementNotFound, key)
	}
	if !known(f.fillable, key) {
		return fmt.Errorf("%w: %s", ErrElementNotFound, key)
	}
	f.fills = append(f.fills, fmt.Sprintf("%s=%s", key, value))
	return nil
}

func known(m map[string]bool, key string) bool { _, ok := m[key]; return ok }

func (f *fakeDriver) Click(_ context.Context, sel Selector) error {
	key := describe(sel)
	if err, ok := f.clickErrs[key]; ok {
		return err
	}
	f.clicks = append(f.clicks, key)
	return nil
}

func (f *fakeDriver) WaitForURL(context.Context, string) error { return f.waitErr }
func (f *fakeDriver) WaitSettled(context.Context) error        { return nil }

func specFor(host string) FormSpec {
	return FormSpec{
		Name:     "test-broker",
		StartURL: "https://" + host + "/optout",
		Fields: []Field{
			{Name: "email", Selector: Selector{Label: "Email"}, Value: "ada@example.com", Required: true},
			{Name: "phone", Selector: Selector{Label: "Phone"}, Value: "+49123", Required: false},
		},
		Submit: Selector{Text: "Send request"},
	}
}

func TestSubmitFormSuccess(t *testing.T) {
	driver := newFakeDriver()
	driver.fillable["label Email"] = true
	driver.fillable["label Phone"] = true
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeSuccess {
		t.Fatalf("code = %q, want success (%s)", result.Code, result.Message)
	}
	if len(driver.fills) != 2 {
		t.Fatalf("expected 2 fills, got %v", driver.fills)
	}
	if result.Evidence == nil {
		t.Fatal("evidence missing on success")
	}
	if len(result.Evidence.PreSubmitScreenshot) == 0 || len(result.Evidence.PostSubmitScreenshot) == 0 {
		t.Fatal("both deterministic screenshot points must be captured")
	}
	if result.Evidence.FinalURL != "https://broker.example/optout" {
		t.Fatalf("final url = %q", result.Evidence.FinalURL)
	}
}

func TestSubmitFormRequiredFieldMissingIsLoud(t *testing.T) {
	// Issue #281: a broker renaming a field must fail loudly and specifically,
	// never submit a silently half-filled GDPR erasure request.
	driver := newFakeDriver()
	driver.fillable["label Email"] = false // broker renamed the field
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeFieldNotFound {
		t.Fatalf("code = %q, want field_not_found", result.Code)
	}
	if result.FailedField != "email" {
		t.Fatalf("failed field = %q, want email", result.FailedField)
	}
	if len(driver.clicks) != 0 {
		t.Fatal("submit must not happen when a required field is missing")
	}
	if result.Evidence == nil || result.Evidence.PageText == "" {
		t.Fatal("failure evidence must capture the page state")
	}
}

func TestSubmitFormOptionalFieldMissingIsSkipped(t *testing.T) {
	driver := newFakeDriver()
	driver.fillable["label Email"] = true
	driver.fillable["label Phone"] = false
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeSuccess {
		t.Fatalf("code = %q", result.Code)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "phone" {
		t.Fatalf("skipped = %v, want [phone]", result.Skipped)
	}
}

func TestSubmitFormCaptcha(t *testing.T) {
	driver := newFakeDriver()
	driver.html = `<div class="g-recaptcha" data-sitekey="x"></div>`
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeBlockedCaptcha {
		t.Fatalf("code = %q, want blocked_captcha", result.Code)
	}
	if result.Hint == "" {
		t.Fatal("captcha outcome must carry the human-queue hint")
	}
	if len(driver.clicks) != 0 {
		t.Fatal("no interaction may happen behind a CAPTCHA")
	}
}

func TestSubmitFormNavigationTimeout(t *testing.T) {
	driver := newFakeDriver()
	driver.timeout = true
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeNavigationTimeout {
		t.Fatalf("code = %q, want navigation_timeout", result.Code)
	}
}

func TestSubmitFormNoFormOnPage(t *testing.T) {
	driver := newFakeDriver()
	driver.html = `<html><body><p>Nothing here</p></body></html>`
	driver.fillable["label Email"] = true
	runner := &Runner{Driver: driver}

	result, err := runner.SubmitForm(context.Background(), specFor("broker.example"))
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeFormNotFound {
		t.Fatalf("code = %q, want form_not_found", result.Code)
	}
}

func TestSubmitFormSuccessURLNotReached(t *testing.T) {
	driver := newFakeDriver()
	driver.fillable["label Email"] = true
	driver.fillable["label Phone"] = true
	driver.waitErr = fmt.Errorf("wait timed out")
	runner := &Runner{Driver: driver}

	spec := specFor("broker.example")
	spec.SuccessURLGlob = "**/thank-you"
	result, err := runner.SubmitForm(context.Background(), spec)
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeSubmitFailed {
		t.Fatalf("code = %q, want submit_failed", result.Code)
	}
}

func TestSubmitFormInvalidSpec(t *testing.T) {
	runner := &Runner{Driver: newFakeDriver()}
	result, err := runner.SubmitForm(context.Background(), FormSpec{})
	if err != nil {
		t.Fatalf("SubmitForm: %v", err)
	}
	if result.Code != CodeInvalidSpec {
		t.Fatalf("code = %q, want invalid_spec", result.Code)
	}
}
