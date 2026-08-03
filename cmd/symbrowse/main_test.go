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
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if payload["tool"] != "symbrowse" || payload["schema_version"] != float64(1) {
		t.Fatalf("payload = %#v", payload)
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
	var payload struct {
		Fields map[string]struct {
			Value  string `json:"value"`
			Source string `json:"source"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if got := payload.Fields["log_level"]; got.Value != "error" || got.Source != "flag" {
		t.Fatalf("log_level = %#v", got)
	}
}
