package main

import (
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
)

// TestEscalationCommandResolves verifies the command an escalation hint
// suggests is a command this binary actually has. A tier-0 fetch that tells
// an agent to run something unrunnable is worse than staying silent, so the
// hint's argv is checked against the real command tree (docs/tiers.md).
func TestEscalationCommandResolves(t *testing.T) {
	fields := strings.Fields(pipeline.EscalationCommandPrefix)
	if len(fields) < 2 {
		t.Fatalf("escalation command prefix %q has no subcommand", pipeline.EscalationCommandPrefix)
	}
	if fields[0] != "symbrowse" {
		t.Errorf("escalation command invokes %q, want symbrowse", fields[0])
	}
	argv := fields[1:]

	root := newRootCommand()
	resolved, _, err := root.Find(argv)
	if err != nil {
		t.Fatalf("escalation command %q does not resolve: %v", pipeline.EscalationCommandPrefix, err)
	}
	if resolved == root {
		t.Fatalf("escalation command %q resolves to the root command, not a subcommand", pipeline.EscalationCommandPrefix)
	}
	if resolved.Name() != argv[len(argv)-1] {
		t.Errorf("escalation command resolved to %q, want %q", resolved.Name(), argv[len(argv)-1])
	}
	// The suggested command takes the URL as a positional argument.
	if err := resolved.Args(resolved, []string{"https://example.com"}); err != nil {
		t.Errorf("escalation command %q rejects a URL argument: %v", pipeline.EscalationCommandPrefix, err)
	}
}
