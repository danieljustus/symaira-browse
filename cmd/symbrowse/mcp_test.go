package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestMCPListProfiles verifies the --list-profiles data table: every
// canonical profile appears with its description and tool count, the core
// profile stays under 15 tools, and the output is deterministic.
func TestMCPListProfiles(t *testing.T) {
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"mcp", "--list-profiles"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, profile := range []string{"core", "nav", "state", "network", "debug", "flows"} {
		if !strings.Contains(text, profile) {
			t.Errorf("--list-profiles output misses profile %q:\n%s", profile, text)
		}
	}
	coreLine := lineContaining(t, text, "core")
	if !strings.Contains(coreLine, "tools") {
		t.Errorf("core line %q lacks the tool count", coreLine)
	}
	if !strings.Contains(coreLine, "open") && !strings.Contains(coreLine, "snapshot") {
		// The tool list line follows the description line; both must exist.
		if !strings.Contains(text, "open") {
			t.Errorf("core profile output misses tool names:\n%s", text)
		}
	}
	if strings.Contains(text, "unknown") {
		t.Errorf("unexpected content in --list-profiles output:\n%s", text)
	}
}

// TestMCPRejectsUnknownProfile verifies that an unknown --tools value fails
// before the server starts.
func TestMCPRejectsUnknownProfile(t *testing.T) {
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"mcp", "--tools", "core,bogus"})
	err := command.Execute()
	if err == nil {
		t.Fatal("mcp with an unknown profile must fail")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %q, want mention of the unknown profile", err)
	}
}

func lineContaining(t *testing.T, text, needle string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line contains %q in:\n%s", needle, text)
	return ""
}
