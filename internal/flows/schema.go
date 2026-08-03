// Package flows defines the declarative symbrowse flow schema
// (ARCHITEKTUR.md §5.6) and its parser. A flow is a versioned, human-reviewable
// automation script: it opens one or more allowed domains, runs semantic
// find-based steps, and extracts outputs from the final state. Secrets may
// only appear as op://… references, never as plaintext values.
package flows

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the current flow schema version.
const SchemaVersion = 1

// Flow is one parsed and validated flow document.
type Flow struct {
	Name    string   `yaml:"name" json:"name"`
	Version int      `yaml:"version" json:"version"`
	Domains []string `yaml:"domains" json:"domains"`
	Inputs  []string `yaml:"inputs" json:"inputs,omitempty"`
	Steps   []Step   `yaml:"steps" json:"steps"`
	Outputs []Output `yaml:"outputs" json:"outputs,omitempty"`

	// Source carries the origin for discovery and error reporting.
	Source string `yaml:"-" json:"-"`
}

// Step is one executable flow step. Exactly one action field is set.
type Step struct {
	Open     *OpenStep     `yaml:"open,omitempty" json:"open,omitempty"`
	Find     *FindStep     `yaml:"find,omitempty" json:"find,omitempty"`
	Click    *SelectorStep `yaml:"click,omitempty" json:"click,omitempty"`
	Fill     *FillStep     `yaml:"fill,omitempty" json:"fill,omitempty"`
	Wait     *WaitStep     `yaml:"wait,omitempty" json:"wait,omitempty"`
	Assert   *AssertStep   `yaml:"assert,omitempty" json:"assert,omitempty"`
	Snapshot *SnapshotStep `yaml:"snapshot,omitempty" json:"snapshot,omitempty"`
}

// Action returns the step's action name (open, find, click, fill, wait,
// assert, snapshot) for risk classification and journaling.
func (s Step) Action() string {
	switch {
	case s.Open != nil:
		return "open"
	case s.Find != nil:
		return "find"
	case s.Click != nil:
		return "click"
	case s.Fill != nil:
		return "fill"
	case s.Wait != nil:
		return "wait"
	case s.Assert != nil:
		return "assert"
	case s.Snapshot != nil:
		return "snapshot"
	default:
		return ""
	}
}

// OpenStep navigates to a URL. The URL may reference inputs via {{name}}.
type OpenStep struct {
	URL string `yaml:"url" json:"url"`
}

// FindStep locates an element semantically and optionally acts on it.
// Action mirrors engine.FinderAction (click, fill, check, hover, text).
type FindStep struct {
	Label       string `yaml:"label,omitempty" json:"label,omitempty"`
	Role        string `yaml:"role,omitempty" json:"role,omitempty"`
	Text        string `yaml:"text,omitempty" json:"text,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty" json:"placeholder,omitempty"`
	Alt         string `yaml:"alt,omitempty" json:"alt,omitempty"`
	Title       string `yaml:"title,omitempty" json:"title,omitempty"`
	TestID      string `yaml:"testid,omitempty" json:"testid,omitempty"`
	Action      string `yaml:"action,omitempty" json:"action,omitempty"`
	Value       string `yaml:"value,omitempty" json:"value,omitempty"`
}

// SelectorStep clicks an element identified by a semantic selector.
type SelectorStep struct {
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	Role  string `yaml:"role,omitempty" json:"role,omitempty"`
	Text  string `yaml:"text,omitempty" json:"text,omitempty"`
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
}

// FillStep fills a form field identified by a semantic selector.
type FillStep struct {
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	Role  string `yaml:"role,omitempty" json:"role,omitempty"`
	Text  string `yaml:"text,omitempty" json:"text,omitempty"`
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Value string `yaml:"value" json:"value"`
}

// WaitStep waits for a condition (url glob, visible element or fixed ms).
type WaitStep struct {
	URL     string `yaml:"url,omitempty" json:"url,omitempty"`
	Visible string `yaml:"visible,omitempty" json:"visible,omitempty"`
	Ms      int    `yaml:"ms,omitempty" json:"ms,omitempty"`
}

// AssertStep is a hard abort condition verified after the previous step.
type AssertStep struct {
	Visible string `yaml:"visible,omitempty" json:"visible,omitempty"`
	URL     string `yaml:"url,omitempty" json:"url,omitempty"`
	Text    string `yaml:"text,omitempty" json:"text,omitempty"`
	Not     string `yaml:"not,omitempty" json:"not,omitempty"`
}

// SnapshotStep captures the page state (compact by default).
type SnapshotStep struct {
	Diff     bool `yaml:"diff,omitempty" json:"diff,omitempty"`
	Compact  bool `yaml:"compact,omitempty" json:"compact,omitempty"`
	MaxToken int  `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
}

// Output extracts a value from the final state.
type Output struct {
	Name string `yaml:"name" json:"name"`
	From string `yaml:"from" json:"from"`
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

// ValidationError describes one precise schema violation with the offending
// YAML line and field.
type ValidationError struct {
	Line   int    `json:"line"`
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e ValidationError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", e.Line, e.Field, e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

// ValidateError bundles all violations of one document.
type ValidateError struct {
	Errors []ValidationError
}

func (e *ValidateError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, err := range e.Errors {
		parts = append(parts, err.Error())
	}
	return "flow validation failed: " + strings.Join(parts, "; ")
}

// Parse parses and validates a flow document. source names the origin for
// error reporting. The returned Flow is nil when validation fails.
func Parse(data []byte, source string) (*Flow, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, &ValidateError{Errors: []ValidationError{{Line: errLine(err), Field: "document", Reason: "invalid YAML: " + err.Error()}}}
	}
	if document.Kind == 0 || len(document.Content) == 0 {
		return nil, &ValidateError{Errors: []ValidationError{{Line: 1, Field: "document", Reason: "empty flow document"}}}
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, &ValidateError{Errors: []ValidationError{{Line: root.Line, Field: "document", Reason: "flow must be a YAML mapping"}}}
	}

	var flow Flow
	if err := root.Decode(&flow); err != nil {
		return nil, &ValidateError{Errors: []ValidationError{{Line: root.Line, Field: "document", Reason: "decode: " + err.Error()}}}
	}
	flow.Source = source

	if errors := validateFlow(&flow, root); len(errors) > 0 {
		return nil, &ValidateError{Errors: errors}
	}
	return &flow, nil
}

// errLine extracts a best-effort YAML line from a parser error.
func errLine(err error) int {
	var line int
	if _, err := fmt.Sscanf(err.Error(), "yaml: line %d:", &line); err == nil {
		return line
	}
	return 0
}

func validateFlow(flow *Flow, root *yaml.Node) []ValidationError {
	var errors []ValidationError

	if strings.TrimSpace(flow.Name) == "" {
		errors = append(errors, ValidationError{Line: fieldLine(root, "name"), Field: "name", Reason: "required"})
	}
	if flow.Version == 0 {
		errors = append(errors, ValidationError{Line: fieldLine(root, "version"), Field: "version", Reason: "required (use 1)"})
	}
	if flow.Version != SchemaVersion {
		errors = append(errors, ValidationError{Line: fieldLine(root, "version"), Field: "version", Reason: fmt.Sprintf("unsupported version %d (supported: %d)", flow.Version, SchemaVersion)})
	}
	if len(flow.Domains) == 0 {
		errors = append(errors, ValidationError{Line: fieldLine(root, "domains"), Field: "domains", Reason: "required: at least one allowed domain"})
	}
	for index, domain := range flow.Domains {
		if !validDomainPattern(domain) {
			errors = append(errors, ValidationError{Line: fieldLine(root, "domains"), Field: fmt.Sprintf("domains[%d]", index), Reason: fmt.Sprintf("invalid domain pattern %q", domain)})
		}
	}
	if len(flow.Steps) == 0 {
		errors = append(errors, ValidationError{Line: fieldLine(root, "steps"), Field: "steps", Reason: "required: at least one step"})
	}
	for index := range flow.Steps {
		if stepErrors := validateStep(&flow.Steps[index], stepNode(root, index)); len(stepErrors) > 0 {
			errors = append(errors, stepErrors...)
		}
	}
	for index := range flow.Outputs {
		if strings.TrimSpace(flow.Outputs[index].Name) == "" {
			errors = append(errors, ValidationError{Line: fieldLine(root, "outputs"), Field: fmt.Sprintf("outputs[%d].name", index), Reason: "required"})
		}
		if from := flow.Outputs[index].From; from != "url" && from != "text" && from != "attribute" && from != "html" {
			errors = append(errors, ValidationError{Line: fieldLine(root, "outputs"), Field: fmt.Sprintf("outputs[%d].from", index), Reason: fmt.Sprintf("invalid source %q (use url, text, attribute or html)", from)})
		}
	}
	return errors
}

func validateStep(step *Step, node *yaml.Node) []ValidationError {
	if step.Action() == "" {
		return []ValidationError{{Line: node.Line, Field: "steps[]", Reason: "step must declare exactly one of open, find, click, fill, wait, assert, snapshot"}}
	}
	var errors []ValidationError
	switch {
	case step.Open != nil:
		if strings.TrimSpace(step.Open.URL) == "" {
			errors = append(errors, ValidationError{Line: node.Line, Field: "open.url", Reason: "required"})
		}
	case step.Find != nil:
		find := step.Find
		if find.Label == "" && find.Role == "" && find.Text == "" && find.Placeholder == "" && find.Alt == "" && find.Title == "" && find.TestID == "" {
			errors = append(errors, ValidationError{Line: node.Line, Field: "find", Reason: "at least one semantic selector (label, role, text, placeholder, alt, title, testid) is required"})
		}
		if find.Action != "" && !validFindAction(find.Action) {
			errors = append(errors, ValidationError{Line: node.Line, Field: "find.action", Reason: fmt.Sprintf("invalid action %q (use click, fill, check, hover or text)", find.Action)})
		}
		if (find.Action == "fill" || find.Action == "text") && strings.TrimSpace(find.Value) == "" {
			errors = append(errors, ValidationError{Line: node.Line, Field: "find.value", Reason: fmt.Sprintf("action %q requires a value", find.Action)})
		}
		if secretInValue(find.Value) {
			errors = append(errors, ValidationError{Line: node.Line, Field: "find.value", Reason: "plaintext secret detected: use an op://… reference instead"})
		}
	case step.Click != nil:
		if !hasSelector(step.Click.Label, step.Click.Role, step.Click.Text, step.Click.Name) {
			errors = append(errors, ValidationError{Line: node.Line, Field: "click", Reason: "at least one semantic selector is required"})
		}
	case step.Fill != nil:
		fill := step.Fill
		if !hasSelector(fill.Label, fill.Role, fill.Text, fill.Name) {
			errors = append(errors, ValidationError{Line: node.Line, Field: "fill", Reason: "at least one semantic selector is required"})
		}
		if strings.TrimSpace(fill.Value) == "" {
			errors = append(errors, ValidationError{Line: node.Line, Field: "fill.value", Reason: "required"})
		}
		if secretInValue(fill.Value) {
			errors = append(errors, ValidationError{Line: node.Line, Field: "fill.value", Reason: "plaintext secret detected: use an op://… reference instead"})
		}
	case step.Wait != nil:
		wait := step.Wait
		if wait.URL == "" && wait.Visible == "" && wait.Ms <= 0 {
			errors = append(errors, ValidationError{Line: node.Line, Field: "wait", Reason: "one of url, visible or ms is required"})
		}
		if wait.Ms < 0 {
			errors = append(errors, ValidationError{Line: node.Line, Field: "wait.ms", Reason: "must not be negative"})
		}
	case step.Assert != nil:
		assert := step.Assert
		if assert.Visible == "" && assert.URL == "" && assert.Text == "" && assert.Not == "" {
			errors = append(errors, ValidationError{Line: node.Line, Field: "assert", Reason: "one of visible, url, text or not is required"})
		}
	case step.Snapshot != nil:
		if step.Snapshot.MaxToken < 0 {
			errors = append(errors, ValidationError{Line: node.Line, Field: "snapshot.max_tokens", Reason: "must not be negative"})
		}
	}
	return errors
}

// secretInValue detects likely plaintext secrets: values that look like
// passwords, tokens or credential pairs outside an op:// reference.
func secretInValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "op://") {
		return false
	}
	lower := strings.ToLower(trimmed)
	secretHints := []string{
		"password", "passwd", "pwd=", "secret", "token", "apikey", "api_key",
		"api-key", "access_key", "private_key", "credential", "authorization",
		"bearer ", "client_secret",
	}
	for _, hint := range secretHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func validFindAction(action string) bool {
	switch action {
	case "click", "fill", "check", "hover", "text", "ref", "first", "last", "nth":
		return true
	default:
		return false
	}
}

func hasSelector(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// validDomainPattern accepts plain hostnames and *.subdomain wildcards.
func validDomainPattern(pattern string) bool {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return false
	}
	parts := strings.Split(trimmed, ".")
	for index, part := range parts {
		if part == "*" && index == 0 {
			continue
		}
		if part == "" {
			return false
		}
		for _, char := range part {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

// fieldLine returns the YAML line of a top-level field, or 0 when absent.
func fieldLine(root *yaml.Node, field string) int {
	if root == nil || root.Kind != yaml.MappingNode {
		return 0
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == field {
			return root.Content[index].Line
		}
	}
	return 0
}

// stepNode returns the YAML node of the steps sequence or the i-th element.
func stepNode(root *yaml.Node, index int) *yaml.Node {
	steps := mappingValue(root, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode || index >= len(steps.Content) {
		if steps != nil {
			return steps
		}
		return root
	}
	return steps.Content[index]
}

func mappingValue(root *yaml.Node, field string) *yaml.Node {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == field {
			return root.Content[index+1]
		}
	}
	return nil
}
