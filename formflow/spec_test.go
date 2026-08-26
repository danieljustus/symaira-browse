package formflow

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormSpecValidate(t *testing.T) {
	cases := []struct {
		name string
		spec FormSpec
		want string // substring expected in the message; empty means valid
	}{
		{"valid", specFor("broker.example"), ""},
		{"missing name", FormSpec{StartURL: "https://x.example", Fields: []Field{{Name: "a", Selector: Selector{Label: "A"}}}, Submit: Selector{Text: "Go"}}, "name is required"},
		{"missing url", FormSpec{Name: "x"}, "start_url is required"},
		{"no fields", FormSpec{Name: "x", StartURL: "https://x.example"}, "at least one field"},
		{"field without selector", FormSpec{Name: "x", StartURL: "https://x.example", Fields: []Field{{Name: "a"}}, Submit: Selector{Text: "Go"}}, "no selector"},
		{"no submit", FormSpec{Name: "x", StartURL: "https://x.example", Fields: []Field{{Name: "a", Selector: Selector{Label: "A"}}}}, "submit selector is required"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.spec.Validate()
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("expected valid spec, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected invalid spec (%q)", testCase.want)
			}
			outcome, ok := err.(Outcome)
			if !ok || outcome.Code != CodeInvalidSpec {
				t.Fatalf("validation must return a typed Outcome, got %T", err)
			}
			if !strings.Contains(outcome.Message, testCase.want) {
				t.Fatalf("message %q missing %q", outcome.Message, testCase.want)
			}
		})
	}
}

func TestOutcomeErrorFormat(t *testing.T) {
	outcome := Outcome{Code: CodeFieldNotFound, Field: "email", Message: "not located"}
	if got := outcome.Error(); !strings.Contains(got, "field_not_found") || !strings.Contains(got, `"email"`) {
		t.Fatalf("unexpected error format: %q", got)
	}
	blocked := Outcome{Code: CodeBlockedCaptcha}
	clean := Outcome{Code: CodeSuccess}
	if !blocked.Blocked() || clean.Blocked() {
		t.Fatal("Blocked() classification wrong")
	}
}

// TestResultJSONSchema pins the machine-readable result schema (repository
// invariant: every output has a stable JSON field schema).
func TestResultJSONSchema(t *testing.T) {
	result := &Result{
		Code:        CodeFieldNotFound,
		Message:     "m",
		FailedStep:  "fill",
		FailedField: "email",
		Hint:        "h",
		Skipped:     []string{"phone"},
		Evidence:    &Evidence{FinalURL: "https://x.example", PageText: "t", PreSubmitScreenshot: []byte("a")},
		DurationMS:  12,
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantKeys := []string{"code", "message", "failed_step", "failed_field", "hint", "skipped_fields", "evidence", "duration_ms"}
	for _, key := range wantKeys {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("result JSON missing key %q: %s", key, data)
		}
	}
	evidence, ok := decoded["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("evidence must be an object: %s", data)
	}
	for _, key := range []string{"final_url", "page_text", "pre_submit_screenshot"} {
		if _, ok := evidence[key]; !ok {
			t.Fatalf("evidence JSON missing key %q", key)
		}
	}
}
