package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/output"
)

func TestFlowValidateCommandValid(t *testing.T) {
	path := writeFlowFixture(t, "valid", `name: test-flow
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
`)
	root := newRootCommand()
	buffer := new(bytes.Buffer)
	root.SetOut(buffer)
	root.SetErr(buffer)
	root.SetArgs([]string{"flow", "validate", path})
	if err := root.Execute(); err != nil {
		t.Fatalf("flow validate returned error: %v", err)
	}
	if !strings.Contains(buffer.String(), "valid: test-flow") {
		t.Errorf("output = %q, want valid message", buffer.String())
	}
}

func TestFlowValidateCommandJSON(t *testing.T) {
	path := writeFlowFixture(t, "valid-json", `name: json-flow
version: 1
domains: ["example.com"]
steps:
  - open: { url: "https://example.com" }
`)
	root := newRootCommand()
	buffer := new(bytes.Buffer)
	root.SetOut(buffer)
	root.SetErr(buffer)
	root.SetArgs([]string{"flow", "validate", "--json", path})
	if err := root.Execute(); err != nil {
		t.Fatalf("flow validate returned error: %v", err)
	}
	var envelope output.Envelope
	if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buffer.String())
	}
	if !envelope.Success {
		t.Fatalf("envelope.Success = false: %+v", envelope)
	}
}

func TestFlowValidateCommandRejectsSecret(t *testing.T) {
	path := writeFlowFixture(t, "secret", `name: secret-flow
version: 1
domains: ["example.com"]
steps:
  - fill: { label: "Passwort", value: "password123" }
`)
	root := newRootCommand()
	buffer := new(bytes.Buffer)
	root.SetOut(buffer)
	root.SetErr(buffer)
	root.SetArgs([]string{"flow", "validate", "--json", path})
	err := root.Execute()
	if err == nil {
		t.Fatal("flow validate accepted a plaintext secret")
	}
	var envelope output.Envelope
	if jsonErr := json.Unmarshal(buffer.Bytes(), &envelope); jsonErr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jsonErr, buffer.String())
	}
	if envelope.Success {
		t.Fatal("envelope.Success = true for invalid flow")
	}
	if envelope.Error == nil || envelope.Error.Code != "validation" {
		t.Fatalf("error = %+v, want validation code", envelope.Error)
	}
	details, ok := envelope.Error.Details["errors"].([]any)
	if !ok || len(details) == 0 {
		t.Fatalf("details.errors missing: %+v", envelope.Error.Details)
	}
}

func TestFlowValidateCommandMissingFile(t *testing.T) {
	root := newRootCommand()
	buffer := new(bytes.Buffer)
	root.SetOut(buffer)
	root.SetErr(buffer)
	root.SetArgs([]string{"flow", "validate", filepath.Join(t.TempDir(), "nope.yaml")})
	if err := root.Execute(); err == nil {
		t.Fatal("flow validate accepted a missing file")
	}
}

func writeFlowFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestFlowRunCommandDryRun(t *testing.T) {
	path := writeFlowFixture(t, "dryrun", `name: dry-flow
version: 1
domains: ["example.com"]
inputs: [user]
steps:
  - open: { url: "https://example.com" }
  - fill: { label: "User", value: "{{user}}" }
`)
	root := newRootCommand()
	buffer := new(bytes.Buffer)
	root.SetOut(buffer)
	root.SetErr(buffer)
	root.SetArgs([]string{"flow", "run", "--dry-run", "--json", "--input", "user=alice", path})
	if err := root.Execute(); err != nil {
		t.Fatalf("flow run --dry-run returned error: %v", err)
	}
	var envelope output.Envelope
	if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buffer.String())
	}
	if !envelope.Success {
		t.Fatalf("envelope.Success = false: %+v", envelope)
	}
	data, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type = %T", envelope.Data)
	}
	if data["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", data["dry_run"])
	}
	plan, ok := data["plan"].([]any)
	if !ok || len(plan) != 2 {
		t.Fatalf("plan = %v, want 2 items", data["plan"])
	}
}

func TestFlowRunCommandMissingInput(t *testing.T) {
	path := writeFlowFixture(t, "missing-input", `name: needs-input
version: 1
domains: ["example.com"]
inputs: [user]
steps:
  - open: { url: "https://example.com" }
  - fill: { label: "User", value: "{{user}}" }
`)
	root := newRootCommand()
	buffer := new(bytes.Buffer)
	root.SetOut(buffer)
	root.SetErr(buffer)
	root.SetArgs([]string{"flow", "run", path})
	err := root.Execute()
	if err == nil {
		t.Fatal("flow run accepted missing input")
	}
	if !strings.Contains(err.Error(), "missing required inputs") {
		t.Errorf("error = %v, want missing inputs message", err)
	}
}

func TestDiffSnapshotTreesHelper(t *testing.T) {
	result := diffSnapshotTrees("line1\nline2\n", "line2\nline3\n")
	added := result["added"].([]string)
	removed := result["removed"].([]string)
	if len(added) != 1 || added[0] != "line3" {
		t.Errorf("added = %v, want [line3]", added)
	}
	if len(removed) != 1 || removed[0] != "line1" {
		t.Errorf("removed = %v, want [line1]", removed)
	}
	if result["stable"].(int) != 1 {
		t.Errorf("stable = %v, want 1", result["stable"])
	}
	diffLines := result["diff"].([]string)
	if len(diffLines) != 2 || diffLines[0] != "- line1" || diffLines[1] != "+ line3" {
		t.Errorf("diff = %v", diffLines)
	}
}

func TestSplitLines(t *testing.T) {
	lines := splitLines("a\nb\nc")
	if len(lines) != 3 || lines[0] != "a" || lines[2] != "c" {
		t.Errorf("splitLines = %v", lines)
	}
	if empty := splitLines(""); len(empty) != 0 {
		t.Errorf("splitLines('') = %v, want empty", empty)
	}
}
