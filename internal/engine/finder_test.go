package engine

import (
	"encoding/json"
	"testing"
)

func TestFindMatchesSemanticAttributesAndExact(t *testing.T) {
	nodes := []AXNode{
		{Raw: mustJSON(t, map[string]any{"nodeId": "root", "role": map[string]any{"value": "RootWebArea"}, "childIds": []string{"a", "b"}})},
		{Raw: mustJSON(t, map[string]any{"nodeId": "a", "parentId": "root", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Save"}, "attributes": map[string]string{"testid": "save", "title": "Save record"}})},
		{Raw: mustJSON(t, map[string]any{"nodeId": "b", "parentId": "root", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Save as"}, "attributes": map[string]string{"testid": "save-as"}})},
	}
	snapshot, err := RenderSnapshot(nodes, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	matches := findMatches(snapshot.Refs, FindRequest{Kind: FindTestID, Query: "save", Action: FindRef, Exact: true})
	if len(matches) != 1 || matches[0].Name != "Save" {
		t.Fatalf("testid exact matches = %#v", matches)
	}
	matches = findMatches(snapshot.Refs, FindRequest{Kind: FindRole, Query: "button", Action: FindRef})
	if len(matches) != 2 {
		t.Fatalf("role matches = %d, want 2", len(matches))
	}
	if matches[0].Ref >= matches[1].Ref {
		t.Fatalf("matches not ordered: %#v", matches)
	}
}

func TestSelectFindMatchReportsAmbiguityAndNthBounds(t *testing.T) {
	matches := []FindMatch{{Ref: "e1"}, {Ref: "e2"}}
	selected, err := selectFindMatch(matches, FindRequest{Action: FindNth, Index: 1})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Ref != "e2" {
		t.Fatalf("selected ref = %q, want e2", selected.Ref)
	}
	if _, err := selectFindMatch(matches, FindRequest{Action: FindNth, Index: 2}); err == nil {
		t.Fatal("expected nth bounds error")
	}
	if err := validateFindRequest(FindRequest{Kind: FindRole, Query: "button", Action: FindFill}); err == nil {
		t.Fatal("expected fill value validation error")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
