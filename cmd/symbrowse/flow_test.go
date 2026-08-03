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
