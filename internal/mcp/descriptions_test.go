package mcp

import (
	"strings"
	"testing"
)

// TestEveryToolHasSelectionGuidance is the description contract (issue #4,
// AC): every registered tool carries compact guidance for when to use it
// and what it returns.
func TestEveryToolHasSelectionGuidance(t *testing.T) {
	for _, tool := range tools {
		if !strings.Contains(tool.Description, "Use when:") {
			t.Errorf("tool %q lacks \"Use when:\" guidance", tool.Name)
		}
		if !strings.Contains(tool.Description, "Returns:") {
			t.Errorf("tool %q lacks \"Returns:\" guidance", tool.Name)
		}
	}
}

// TestReadOpenSnapshotEscalationBoundary is the Fetch-to-Browse escalation
// contract (issue #4; see docs/tiers.md): the read, open and snapshot
// descriptions distinguish ordinary Fetch usage from JavaScript/browser-state
// escalation.
func TestReadOpenSnapshotEscalationBoundary(t *testing.T) {
	descriptions := map[string]string{}
	for _, tool := range tools {
		descriptions[tool.Name] = tool.Description
	}
	for _, name := range []string{"read", "open", "snapshot"} {
		description, ok := descriptions[name]
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		if !strings.Contains(description, "fetch") {
			t.Errorf("%s description does not mention the fetch boundary", name)
		}
		if !strings.Contains(description, "JavaScript") {
			t.Errorf("%s description does not mention JavaScript escalation", name)
		}
	}
}

// TestDefaultProfileContextBudget keeps the rendered default-profile
// descriptions within a practical context budget (issue #4 AC): the full
// default (core) profile stays below 8k characters.
func TestDefaultProfileContextBudget(t *testing.T) {
	const budget = 8000
	total := 0
	for _, tool := range tools {
		if tool.Profile != ProfileCore {
			continue
		}
		total += len(tool.Description)
	}
	if total > budget {
		t.Fatalf("default profile descriptions total %d chars, budget %d", total, budget)
	}
}
