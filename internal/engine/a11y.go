package engine

import (
	"context"
	"encoding/json"
	"fmt"
)

// A11yOptions configures one accessibility audit.
type A11yOptions struct {
	Tags     []string `json:"tags,omitempty"`
	Selector string   `json:"selector,omitempty"`
}

// A11yNode is one affected element of a violation.
type A11yNode struct {
	Target  []string `json:"target"`
	HTML    string   `json:"html,omitempty"`
	Impact  string   `json:"impact,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

// A11yViolation is one accessibility violation.
type A11yViolation struct {
	ID          string     `json:"id"`
	Impact      string     `json:"impact"`
	Description string     `json:"description"`
	Help        string     `json:"help,omitempty"`
	HelpURL     string     `json:"help_url,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Nodes       []A11yNode `json:"nodes"`
}

// A11yResult is the stable audit payload.
type A11yResult struct {
	AxeVersion     string          `json:"axe_version"`
	URL            string          `json:"url"`
	Violations     []A11yViolation `json:"violations"`
	ViolationCount int             `json:"violation_count"`
	Passes         int             `json:"passes"`
	Incomplete     int             `json:"incomplete"`
}

// A11yAuditor runs axe-core audits against the current page. It is an
// optional engine extension so fake engines can skip it.
type A11yAuditor interface {
	// RunA11y evaluates axe-core in the page and returns the raw audit
	// results (axe_version + results payload).
	RunA11y(context.Context, Page, A11yOptions) (json.RawMessage, error)
}

// Audit runs an accessibility audit on the current page. Without an
// A11yAuditor engine the audit fails with a clear capability error.
func (s *NavigationService) Audit(ctx context.Context, options A11yOptions) (A11yResult, error) {
	auditor, ok := s.engine.(A11yAuditor)
	if !ok {
		return A11yResult{}, fmt.Errorf("browser engine does not support accessibility audits")
	}
	raw, err := auditor.RunA11y(ctx, s.page, options)
	if err != nil {
		return A11yResult{}, err
	}
	var payload struct {
		AxeVersion string `json:"axe_version"`
		Results    struct {
			Violations []A11yViolation   `json:"violations"`
			Passes     []json.RawMessage `json:"passes"`
			Incomplete []json.RawMessage `json:"incomplete"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return A11yResult{}, fmt.Errorf("decode axe results: %w", err)
	}
	result := A11yResult{
		AxeVersion:     payload.AxeVersion,
		Violations:     payload.Results.Violations,
		ViolationCount: len(payload.Results.Violations),
		Passes:         len(payload.Results.Passes),
		Incomplete:     len(payload.Results.Incomplete),
	}
	if url, err := s.Inspect(ctx, InspectionRequest{Kind: InspectURL}); err == nil {
		_ = json.Unmarshal(url.Value, &result.URL)
	}
	return result, nil
}
