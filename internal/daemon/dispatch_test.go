package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// TestDispatchRejectsUnknownCommands verifies unknown frames fail cleanly.
// The static engine runtime lets the dispatch reach the command table
// without a Chrome binary.
func TestDispatchRejectsUnknownCommands(t *testing.T) {
	runtime := staticTestRuntime(t)
	_, _, err := runtime.Handle(context.Background(), Frame{Cmd: "no.such.command", Args: marshalArgsForTest(map[string]any{}), Session: "s"})
	if err == nil || !strings.Contains(err.Error(), "unknown navigation command") {
		t.Fatalf("err = %v", err)
	}
}

// TestDispatchRequiresArguments verifies frames without args fail with a
// clear decode error instead of running with zero values.
func TestDispatchRequiresArguments(t *testing.T) {
	runtime := staticTestRuntime(t)
	_, _, err := runtime.Handle(context.Background(), Frame{Cmd: "open", Args: nil, Session: "s"})
	if err == nil || !strings.Contains(err.Error(), "command arguments are required") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runtime.Handle(context.Background(), Frame{Cmd: "wait", Args: []byte("{not json"), Session: "s"})
	if err == nil || !strings.Contains(err.Error(), "decode wait arguments") {
		t.Fatalf("err = %v", err)
	}
}

// TestDecodeOptionalArgsToleratesAbsentArgs verifies optional-arg frames
// work without payloads.
func TestDecodeOptionalArgsToleratesAbsentArgs(t *testing.T) {
	var target struct {
		Value string `json:"value"`
	}
	if err := decodeOptionalArgs(Frame{Cmd: "x", Args: nil}, &target); err != nil {
		t.Fatal(err)
	}
	if err := decodeOptionalArgs(Frame{Cmd: "x", Args: []byte("null")}, &target); err != nil {
		t.Fatal(err)
	}
	if err := decodeOptionalArgs(Frame{Cmd: "x", Args: []byte(`{"value":"v"}`)}, &target); err != nil {
		t.Fatal(err)
	}
	if target.Value != "v" {
		t.Fatalf("value = %q", target.Value)
	}
}

// TestNetworkPolicyWarnings verifies the warning rendering: totals, per-URL
// entries and the cap for long block lists.
func TestNetworkPolicyWarnings(t *testing.T) {
	reporter := fakePolicyReporter{blocked: []engine.BlockedRequest{
		{URL: "https://evil.example/x.js", ResourceType: "Script", Count: 3},
		{URL: "https://evil.example/y.png", ResourceType: "Image", Count: 1},
	}}
	warnings := networkPolicyWarnings(reporter)
	if len(warnings) < 2 {
		t.Fatalf("warnings = %v", warnings)
	}
	if !strings.Contains(warnings[0].Message, "blocked 4 request(s)") {
		t.Fatalf("total warning = %q", warnings[0].Message)
	}
	if !strings.Contains(warnings[1].Message, "evil.example/x.js") {
		t.Fatalf("per-url warning = %q", warnings[1].Message)
	}
	if len(warnings) != 3 { // total + 2 URLs
		t.Fatalf("warnings = %d, want 3", len(warnings))
	}
	// Long block lists are capped with an "and N more" entry.
	many := make([]engine.BlockedRequest, maxBlockedURLWarnings+5)
	for i := range many {
		many[i] = engine.BlockedRequest{URL: "https://x.example/n", ResourceType: "Other", Count: 1}
	}
	warnings = networkPolicyWarnings(fakePolicyReporter{blocked: many})
	if len(warnings) != maxBlockedURLWarnings+2 {
		t.Fatalf("capped warnings = %d, want %d", len(warnings), maxBlockedURLWarnings+2)
	}
	if !strings.Contains(warnings[len(warnings)-1].Message, "more blocked URL") {
		t.Fatalf("cap message = %q", warnings[len(warnings)-1].Message)
	}
}

// fakePolicyReporter implements engine.NetworkPolicyReporter for tests.
type fakePolicyReporter struct {
	blocked []engine.BlockedRequest
}

func (f fakePolicyReporter) BlockedRequests() []engine.BlockedRequest { return f.blocked }
func (f fakePolicyReporter) Limitations() []string                    { return nil }
