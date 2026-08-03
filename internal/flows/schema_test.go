package flows

import (
	"strings"
	"testing"
)

const validFlow = `name: jira-ticket-anlegen
version: 1
domains: ["jira.example.com"]
inputs: [title, description]
steps:
  - open: { url: "https://jira.example.com/browse/NEW" }
  - find: { label: "Summary", action: fill, value: "{{title}}" }
  - find: { label: "Description", action: fill, value: "{{description}}" }
  - assert: { visible: "button[name=Create]" }
  - find: { role: button, name: "Create", action: click }
  - wait: { url: "**/browse/*" }
outputs:
  - { name: ticket_url, from: url }
`

func TestParseValidFlow(t *testing.T) {
	flow, err := Parse([]byte(validFlow), "test")
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if flow.Name != "jira-ticket-anlegen" {
		t.Errorf("Name = %q, want jira-ticket-anlegen", flow.Name)
	}
	if flow.Version != 1 {
		t.Errorf("Version = %d, want 1", flow.Version)
	}
	if len(flow.Domains) != 1 || flow.Domains[0] != "jira.example.com" {
		t.Errorf("Domains = %v, want [jira.example.com]", flow.Domains)
	}
	if len(flow.Steps) != 6 {
		t.Fatalf("len(Steps) = %d, want 6", len(flow.Steps))
	}
	actions := []string{"open", "find", "find", "assert", "find", "wait"}
	for index, want := range actions {
		if got := flow.Steps[index].Action(); got != want {
			t.Errorf("Steps[%d].Action() = %q, want %q", index, got, want)
		}
	}
	if len(flow.Outputs) != 1 || flow.Outputs[0].Name != "ticket_url" || flow.Outputs[0].From != "url" {
		t.Errorf("Outputs = %+v, want ticket_url from url", flow.Outputs)
	}
}

func TestParseRejectsMissingName(t *testing.T) {
	_, err := Parse([]byte(`version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
`), "test")
	assertValidation(t, err, "name", "required")
}

func TestParseRejectsMissingDomains(t *testing.T) {
	_, err := Parse([]byte(`name: no-domains
version: 1
steps:
  - open: { url: "https://example.com" }
`), "test")
	assertValidation(t, err, "domains", "required")
}

func TestParseRejectsUnsupportedVersion(t *testing.T) {
	_, err := Parse([]byte(`name: v2-flow
version: 2
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
`), "test")
	assertValidation(t, err, "version", "unsupported version 2")
}

func TestParseRejectsInvalidDomainPattern(t *testing.T) {
	_, err := Parse([]byte(`name: bad-domain
version: 1
domains: ["exa mple.com"]
steps:
  - open: { url: "https://example.com" }
`), "test")
	assertValidation(t, err, "domains[0]", "invalid domain pattern")
}

func TestParseAcceptsWildcardSubdomain(t *testing.T) {
	flow, err := Parse([]byte(`name: wildcard
version: 1
domains: ["*.example.com"]
steps:
  - open: { url: "https://app.example.com" }
`), "test")
	if err != nil {
		t.Fatalf("wildcard domain rejected: %v", err)
	}
	if flow.Domains[0] != "*.example.com" {
		t.Errorf("Domains[0] = %q", flow.Domains[0])
	}
}

func TestParseRejectsEmptySteps(t *testing.T) {
	_, err := Parse([]byte(`name: no-steps
version: 1
domains: ["example.com"]
steps: []
`), "test")
	assertValidation(t, err, "steps", "required")
}

func TestParseRejectsPlaintextSecretInFill(t *testing.T) {
	_, err := Parse([]byte(`name: secret-flow
version: 1
domains: ["example.com"]
steps:
  - fill: { label: "Password", value: "hunter2password" }
`), "test")
	assertValidation(t, err, "fill.value", "plaintext secret")
}

func TestParseRejectsPlaintextSecretInFindValue(t *testing.T) {
	_, err := Parse([]byte(`name: secret-flow-2
version: 1
domains: ["example.com"]
steps:
  - find: { label: "Token", action: fill, value: "secret-token-abc" }
`), "test")
	assertValidation(t, err, "find.value", "plaintext secret")
}

func TestParseAcceptsOpRefSecret(t *testing.T) {
	flow, err := Parse([]byte(`name: op-flow
version: 1
domains: ["example.com"]
steps:
  - fill: { label: "Password", value: "op://vault/item/password" }
`), "test")
	if err != nil {
		t.Fatalf("op:// reference rejected: %v", err)
	}
	if got := flow.Steps[0].Fill.Value; got != "op://vault/item/password" {
		t.Errorf("Fill.Value = %q", got)
	}
}

func TestParseRejectsStepWithoutAction(t *testing.T) {
	_, err := Parse([]byte(`name: empty-step
version: 1
domains: ["example.com"]
steps:
  - {}
`), "test")
	assertValidation(t, err, "steps[]", "exactly one")
}

func TestParseRejectsFindWithoutSelector(t *testing.T) {
	_, err := Parse([]byte(`name: find-empty
version: 1
domains: ["example.com"]
steps:
  - find: { action: click }
`), "test")
	assertValidation(t, err, "find", "semantic selector")
}

func TestParseRejectsInvalidFindAction(t *testing.T) {
	_, err := Parse([]byte(`name: find-bad-action
version: 1
domains: ["example.com"]
steps:
  - find: { label: "X", action: explode }
`), "test")
	assertValidation(t, err, "find.action", "invalid action")
}

func TestParseRejectsWaitWithoutCondition(t *testing.T) {
	_, err := Parse([]byte(`name: wait-empty
version: 1
domains: ["example.com"]
steps:
  - wait: {}
`), "test")
	assertValidation(t, err, "wait", "one of url, visible or ms")
}

func TestParseRejectsInvalidOutputSource(t *testing.T) {
	_, err := Parse([]byte(`name: bad-output
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
outputs:
  - { name: x, from: javascript }
`), "test")
	assertValidation(t, err, "outputs[0].from", "invalid source")
}

func TestParseReportsYAMLLineNumbers(t *testing.T) {
	_, err := Parse([]byte(`name: line-check
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - fill: { label: "Password", value: "password123" }
`), "test")
	validateErr, ok := err.(*ValidateError)
	if !ok {
		t.Fatalf("error type = %T, want *ValidateError", err)
	}
	found := false
	for _, item := range validateErr.Errors {
		if item.Field == "fill.value" && item.Line == 6 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected line 7 fill.value error, got %+v", validateErr.Errors)
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	_, err := Parse([]byte("name: [unclosed\n"), "test")
	if err == nil {
		t.Fatal("malformed YAML accepted")
	}
	if !strings.Contains(err.Error(), "invalid YAML") {
		t.Errorf("error = %q, want invalid YAML message", err.Error())
	}
}

func TestParseRejectsEmptyDocument(t *testing.T) {
	_, err := Parse([]byte(""), "test")
	assertValidation(t, err, "document", "empty")
}

func TestParseAcceptsMinimalSnapshotFlow(t *testing.T) {
	flow, err := Parse([]byte(`name: snap
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
  - snapshot: { compact: true, max_tokens: 2000 }
`), "test")
	if err != nil {
		t.Fatalf("snapshot flow rejected: %v", err)
	}
	if !flow.Steps[1].Snapshot.Compact || flow.Steps[1].Snapshot.MaxToken != 2000 {
		t.Errorf("Snapshot step = %+v", flow.Steps[1].Snapshot)
	}
}

func TestParseRejectsNegativeWaitMs(t *testing.T) {
	_, err := Parse([]byte(`name: neg-wait
version: 1
domains: ["example.com"]
steps:
  - wait: { ms: -5 }
`), "test")
	assertValidation(t, err, "wait.ms", "negative")
}

func assertValidation(t *testing.T, err error, field, reasonPart string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected validation error for %s (%s), got nil", field, reasonPart)
	}
	validateErr, ok := err.(*ValidateError)
	if !ok {
		t.Fatalf("error type = %T, want *ValidateError (%v)", err, err)
	}
	for _, item := range validateErr.Errors {
		if item.Field == field && strings.Contains(item.Reason, reasonPart) {
			return
		}
	}
	t.Errorf("no error for field %s containing %q; got %+v", field, reasonPart, validateErr.Errors)
}
