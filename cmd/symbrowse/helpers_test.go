package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

func TestFrameURLParsing(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"url": "https://example.com/x"})
	if got := frameURL(daemon.Frame{Args: raw}); got != "https://example.com/x" {
		t.Fatalf("frameURL = %q", got)
	}
	if got := frameURL(daemon.Frame{Args: json.RawMessage(`bad`)}); got != "" {
		t.Fatalf("frameURL(bad) = %q, want empty", got)
	}
	if got := frameURL(daemon.Frame{}); got != "" {
		t.Fatalf("frameURL(empty) = %q, want empty", got)
	}
}

func TestApprovalTimeoutEnv(t *testing.T) {
	_ = os.Setenv("SYMBROWSE_APPROVAL_TIMEOUT", "7")
	defer func() { _ = os.Unsetenv("SYMBROWSE_APPROVAL_TIMEOUT") }()
	if got := approvalTimeout(); got != 7*time.Second {
		t.Fatalf("approvalTimeout = %v, want 7s", got)
	}
	_ = os.Setenv("SYMBROWSE_APPROVAL_TIMEOUT", "bogus")
	if got := approvalTimeout(); got != 60*time.Second {
		t.Fatalf("approvalTimeout(bogus) = %v, want 60s", got)
	}
	_ = os.Unsetenv("SYMBROWSE_APPROVAL_TIMEOUT")
	if got := approvalTimeout(); got != 60*time.Second {
		t.Fatalf("approvalTimeout(unset) = %v, want 60s", got)
	}
}

func TestPolicyModeEnv(t *testing.T) {
	_ = os.Setenv("SYMBROWSE_MCP", "1")
	defer func() { _ = os.Unsetenv("SYMBROWSE_MCP") }()
	if policyMode() != policy.ModeMCP {
		t.Fatal("policyMode(MCP) = TTY, want MCP")
	}
	_ = os.Unsetenv("SYMBROWSE_MCP")
	if policyMode() != policy.ModeTTY {
		t.Fatal("policyMode(unset) = MCP, want TTY")
	}
}

func TestAutosaveFromEnv(t *testing.T) {
	cmd := newRootCommand()
	_ = os.Setenv("SYMBROWSE_AUTOSAVE", "always")
	_ = os.Setenv("SYMBROWSE_AUTOSAVE_INTERVAL", "5")
	_ = os.Setenv("SYMBROWSE_AUTOSAVE_KEY", "auto-state")
	defer func() {
		_ = os.Unsetenv("SYMBROWSE_AUTOSAVE")
		_ = os.Unsetenv("SYMBROWSE_AUTOSAVE_INTERVAL")
		_ = os.Unsetenv("SYMBROWSE_AUTOSAVE_KEY")
	}()
	config, err := autosaveFromEnv(cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	if config.Policy != daemon.AutosaveAlways || config.Interval != 5*time.Second || config.Key != "auto-state" {
		t.Fatalf("config = %+v", config)
	}

	_ = os.Setenv("SYMBROWSE_AUTOSAVE_INTERVAL", "-3")
	if _, err := autosaveFromEnv(cmd, ""); err == nil {
		t.Fatal("expected negative-interval error")
	}
	_ = os.Setenv("SYMBROWSE_AUTOSAVE_INTERVAL", "bogus")
	if _, err := autosaveFromEnv(cmd, ""); err == nil {
		t.Fatal("expected invalid-interval error")
	}
	_ = os.Setenv("SYMBROWSE_AUTOSAVE", "bogus")
	_ = os.Setenv("SYMBROWSE_AUTOSAVE_INTERVAL", "5")
	if _, err := autosaveFromEnv(cmd, ""); err == nil {
		t.Fatal("expected invalid-policy error")
	}
}

func TestFieldHelpers(t *testing.T) {
	fields := map[string]any{"name": "value", "count": 3.5, "missing": nil}
	if got := fieldString(fields, "name"); got != "value" {
		t.Fatalf("fieldString = %q", got)
	}
	if got := fieldString(fields, "absent"); got != "" {
		t.Fatalf("fieldString(absent) = %q", got)
	}
	if got := fieldFloat(fields, "count"); got != 3.5 {
		t.Fatalf("fieldFloat = %v", got)
	}
	if got := fieldFloat(fields, "absent"); got != 0 {
		t.Fatalf("fieldFloat(absent) = %v", got)
	}
}

func TestFirstTokenAndHasFlag(t *testing.T) {
	if got := firstToken("one two three"); got != "one" {
		t.Fatalf("firstToken = %q", got)
	}
	if got := firstToken(""); got != "" {
		t.Fatalf("firstToken(empty) = %q", got)
	}
}

func TestCaptureOutputFormats(t *testing.T) {
	if got := captureOutput("line1\nline2\n", []string{}); got != "line1\nline2" {
		t.Fatalf("captureOutput = %q", got)
	}
	// JSON flag parses the payload.
	payload := captureOutput(`{"ok":true}`, []string{"--json"})
	if data, ok := payload.(map[string]any); !ok || data["ok"] != true {
		t.Fatalf("captureOutput(json) = %#v", payload)
	}
	// Invalid JSON with --json falls back to the trimmed text.
	if got := captureOutput("not json", []string{"--json"}); got != "not json" {
		t.Fatalf("captureOutput(bad json) = %q", got)
	}
}

func TestTokenize(t *testing.T) {
	tokens, err := tokenize(`open "https://example.com/a b" --json`)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 3 || tokens[0] != "open" || tokens[1] != "https://example.com/a b" || tokens[2] != "--json" {
		t.Fatalf("tokens = %v", tokens)
	}
	if _, err := tokenize(`open "unbalanced`); err == nil {
		t.Fatal("expected unbalanced-quote error")
	}
	if got := firstToken(`open "https://example.com/x y"`); got != "open" {
		t.Fatalf("firstToken = %q", got)
	}
}

// newOutputCommand returns a bare cobra command whose stdout/stderr are
// captured in a buffer. The global --json flag is registered so
// jsonOutputFlag works on the command.
func newOutputCommand(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	command := &cobra.Command{}
	command.Flags().Bool("json", false, "machine-readable output envelope")
	buffer := new(bytes.Buffer)
	command.SetOut(buffer)
	command.SetErr(buffer)
	return command, buffer
}

// failingWriter fails every write; it exercises the write-error branches of
// the print/write helpers.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
