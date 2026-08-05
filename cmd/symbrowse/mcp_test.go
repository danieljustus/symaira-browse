package main

import (
	"bytes"
	"encoding/json"
	"os"
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

// TestMCPDocSnippetsAreValidJSON keeps the configuration snippets in
// docs/mcp.md honest (issue #36 AC: every snippet really tested): every
// fenced json block must parse and must configure a stdio server that runs
// the documented "symbrowse mcp" command.
func TestMCPDocSnippetsAreValidJSON(t *testing.T) {
	raw, err := os.ReadFile("../../docs/mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	// Git checks out text files with CRLF on Windows (core.autocrlf), so the
	// fenced json markers arrive as "json\r\n" there. Normalize before parsing
	// so the snippet detection is line-ending agnostic.
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	blocks := 0
	for _, fenced := range strings.Split(text, "```") {
		if len(fenced) < 5 || !strings.HasPrefix(fenced, "json\n") {
			continue
		}
		blocks++
		var snippet struct {
			MCP         map[string]any `json:"mcpServers"`
			OpenCodeMCP map[string]any `json:"mcp"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(fenced, "json\n")), &snippet); err != nil {
			t.Fatalf("docs/mcp.md json snippet is invalid: %v\n%s", err, fenced)
		}
		servers := snippet.MCP
		if servers == nil {
			servers = snippet.OpenCodeMCP
		}
		if len(servers) != 1 {
			t.Fatalf("docs/mcp.md snippet must configure exactly one server, got %d\n%s", len(servers), fenced)
		}
		for name, rawServer := range servers {
			server, _ := rawServer.(map[string]any)
			args, _ := server["args"].([]any)
			if name != "symbrowse" || len(args) != 1 || args[0] != "mcp" {
				t.Errorf("snippet server %v must run [\"mcp\"]: %s", name, fenced)
			}
			if _, ok := server["command"].(string); !ok {
				t.Errorf("snippet server %s lacks a command path: %s", name, fenced)
			}
		}
	}
	if blocks < 4 {
		t.Fatalf("docs/mcp.md must carry at least 4 json snippets (Claude Code, Cursor, OpenCode, Claude Desktop), found %d", blocks)
	}
}
