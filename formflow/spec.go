package formflow

import (
	"fmt"
	"strings"
	"time"
)

// DefaultRunTimeout is the bounded per-run timeout applied when a spec does
// not set one (issue #280: headless operation with a bounded per-run timeout).
const DefaultRunTimeout = 60 * time.Second

// Selector identifies one page element. Prefer the semantic fields (Label,
// Role, Text, Placeholder, TestID) over CSS: they survive markup churn
// better. When both are set, semantic resolution is tried first and CSS is
// the fallback.
type Selector struct {
	CSS         string `json:"css,omitempty"`
	Label       string `json:"label,omitempty"`
	Role        string `json:"role,omitempty"`
	Text        string `json:"text,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	TestID      string `json:"testid,omitempty"`
	Exact       bool   `json:"exact,omitempty"`
}

func (s Selector) empty() bool {
	return s.CSS == "" && s.Label == "" && s.Role == "" && s.Text == "" &&
		s.Placeholder == "" && s.TestID == ""
}

// Field is one entry of the field map: a logical name, the on-page selector
// and the value to fill. Required fields that cannot be located abort the run
// with CodeFieldNotFound — never with a partial silent submission.
type Field struct {
	Name      string   `json:"name"`
	Selector  Selector `json:"selector"`
	Value     string   `json:"value"`
	Required  bool     `json:"required"`
	Sensitive bool     `json:"sensitive,omitempty"`
}

// FormSpec describes one broker opt-out form run.
type FormSpec struct {
	// Name identifies the flow in results and journals.
	Name string `json:"name"`
	// StartURL is the form page to navigate to.
	StartURL string `json:"start_url"`
	// Domains restricts navigation; subdomains of the start host are implied
	// when empty. Supplying an explicit allowlist is recommended for hostile
	// targets.
	Domains []string `json:"domains,omitempty"`
	// Fields is the field map to fill before submitting.
	Fields []Field `json:"fields"`
	// Submit identifies the submit control.
	Submit Selector `json:"submit"`
	// SuccessURLGlob, when set, makes the run verify the post-submit URL.
	SuccessURLGlob string `json:"success_url_glob,omitempty"`
	// Timeout bounds the whole run; DefaultRunTimeout applies when zero.
	Timeout time.Duration `json:"timeout_ms,omitempty"`
}

// Validate checks the spec before any browser interaction. It returns a
// CodeInvalidSpec outcome describing every violation.
func (s FormSpec) Validate() error {
	var problems []string
	if strings.TrimSpace(s.Name) == "" {
		problems = append(problems, "name is required")
	}
	if strings.TrimSpace(s.StartURL) == "" {
		problems = append(problems, "start_url is required")
	}
	if len(s.Fields) == 0 {
		problems = append(problems, "at least one field is required")
	}
	for _, field := range s.Fields {
		if strings.TrimSpace(field.Name) == "" {
			problems = append(problems, "every field needs a name")
			continue
		}
		if field.Selector.empty() {
			problems = append(problems, fmt.Sprintf("field %q has no selector", field.Name))
		}
	}
	if s.Submit.empty() {
		problems = append(problems, "submit selector is required")
	}
	if len(problems) > 0 {
		return Outcome{
			Code:    CodeInvalidSpec,
			Message: "invalid form spec: " + strings.Join(problems, "; "),
			Hint:    "fix the spec and retry; no page was touched",
		}
	}
	return nil
}

// timeout returns the effective bounded run timeout.
func (s FormSpec) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return DefaultRunTimeout
}
