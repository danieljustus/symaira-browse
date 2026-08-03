package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionJSON(t *testing.T) {
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"version", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Tool          string `json:"tool"`
			SchemaVersion int    `json:"schema_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if !envelope.Success {
		t.Fatalf("envelope = %#v: expected success", envelope)
	}
	if envelope.Data.Tool != "symbrowse" || envelope.Data.SchemaVersion != 1 {
		t.Fatalf("payload = %#v", envelope.Data)
	}
}

func TestConfigShowUsesCommandWriterAndFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SYMBROWSE_LOG_LEVEL", "debug")
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"config", "show", "--json", "--log-level", "error"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("config show produced no output")
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Fields map[string]struct {
				Value  string `json:"value"`
				Source string `json:"source"`
			} `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if !envelope.Success {
		t.Fatalf("envelope = %#v: expected success", envelope)
	}
	if got := envelope.Data.Fields["log_level"]; got.Value != "error" || got.Source != "flag" {
		t.Fatalf("log_level = %#v", got)
	}
}

// TestGlobalJSONErrorEnvelope verifies that a failing command produces the
// unified failure envelope with a documented error code when --json is set.
func TestGlobalJSONErrorEnvelope(t *testing.T) {
	var output, errOutput bytes.Buffer
	exitCode := runCLI([]string{"open", "--json"}, &output, &errOutput)
	if exitCode == 0 {
		t.Fatal("expected open without a URL to fail")
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if envelope.Success {
		t.Fatalf("envelope = %#v: expected failure", envelope)
	}
	if envelope.Error.Code == "" {
		t.Fatalf("envelope = %#v: missing error code", envelope)
	}
}

// TestHumanErrorGoesToStderr verifies that without --json the error text is
// printed to stderr and stdout stays clean.
func TestHumanErrorGoesToStderr(t *testing.T) {
	var output, errOutput bytes.Buffer
	exitCode := runCLI([]string{"open"}, &output, &errOutput)
	if exitCode == 0 {
		t.Fatal("expected open without a URL to fail")
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", output.String())
	}
	if errOutput.Len() == 0 {
		t.Fatal("expected an error message on stderr")
	}
}
