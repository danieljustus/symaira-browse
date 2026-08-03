package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func snapshotNodes(t *testing.T) []AXNode {
	t.Helper()
	raw := []map[string]any{
		{"nodeId": "1", "role": map[string]any{"value": "RootWebArea"}, "childIds": []string{"2", "3", "4", "5"}},
		{"nodeId": "2", "role": map[string]any{"value": "navigation"}, "childIds": []string{"6"}},
		{"nodeId": "3", "role": map[string]any{"value": "heading"}, "name": map[string]any{"value": "Dashboard"}},
		{"nodeId": "4", "role": map[string]any{"value": "Iframe"}, "name": map[string]any{"value": "Billing"}, "isShadowRoot": true, "childIds": []string{"7"}},
		{"nodeId": "5", "role": map[string]any{"value": "generic"}, "childIds": []string{"8"}},
		{"nodeId": "6", "role": map[string]any{"value": "link"}, "name": map[string]any{"value": "Settings"}, "properties": []map[string]any{{"name": "url", "value": map[string]any{"value": "https://example.test/settings"}}}},
		{"nodeId": "7", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Speichern"}},
		{"nodeId": "8", "role": map[string]any{"value": "textbox"}, "name": map[string]any{"value": "Search"}},
	}
	encoded := make([]AXNode, 0, len(raw))
	for _, node := range raw {
		payload, err := json.Marshal(node)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, AXNode{Raw: payload})
	}
	return encoded
}

func TestRenderSnapshotIsDeterministicAndShowsBoundaries(t *testing.T) {
	nodes := snapshotNodes(t)
	first, err := RenderSnapshot(nodes, SnapshotOptions{URLs: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderSnapshot(nodes, SnapshotOptions{URLs: true})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("snapshot is not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	for _, want := range []string{"- document", "button \"Speichern\"", "[iframe]", "[shadow-root]", "https://example.test/settings", "[ref=e"} {
		if !strings.Contains(first.Tree, want) {
			t.Fatalf("tree %q missing %q", first.Tree, want)
		}
	}
	if len(first.Refs) != 8 {
		t.Fatalf("refs = %d, want 8", len(first.Refs))
	}
}

func TestRenderSnapshotInteractiveCompactAndDepth(t *testing.T) {
	nodes := snapshotNodes(t)
	full, err := RenderSnapshot(nodes, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	interactive, err := RenderSnapshot(nodes, SnapshotOptions{Interactive: true, Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(interactive.Tree) >= len(full.Tree) {
		t.Fatalf("interactive tree was not reduced: full=%d interactive=%d", len(full.Tree), len(interactive.Tree))
	}
	if strings.Contains(interactive.Tree, "Dashboard") || strings.Contains(interactive.Tree, "navigation") {
		t.Fatalf("interactive tree retained non-interactive content:\n%s", interactive.Tree)
	}
	if !strings.Contains(interactive.Tree, "Speichern") || !strings.Contains(interactive.Tree, "Search") {
		t.Fatalf("interactive tree omitted controls:\n%s", interactive.Tree)
	}
	shallow, err := RenderSnapshot(nodes, SnapshotOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shallow.Tree, "Speichern") {
		t.Fatalf("depth limit was ignored:\n%s", shallow.Tree)
	}
}

func TestRenderSnapshotSelectorRoot(t *testing.T) {
	result, err := RenderSnapshot(snapshotNodes(t), SnapshotOptions{RootNodeID: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Tree, "Dashboard") || !strings.Contains(result.Tree, "Billing") || !strings.Contains(result.Tree, "Speichern") {
		t.Fatalf("selector root was not isolated:\n%s", result.Tree)
	}
}
