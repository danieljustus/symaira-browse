package engine

import "testing"

func TestDiffSnapshotsReportsAddedRemovedAndChanged(t *testing.T) {
	before := SnapshotResult{Refs: map[string]SnapshotRef{
		"e1": {RefKey: "key-1", Role: "button", Name: "Save", Value: "old", Visible: true},
		"e2": {RefKey: "key-2", Role: "link", Name: "Old", Visible: true},
	}}
	after := SnapshotResult{Refs: map[string]SnapshotRef{
		"e1": {RefKey: "key-1", Role: "button", Name: "Save", Value: "new", Visible: true},
		"e3": {RefKey: "key-3", Role: "textbox", Name: "Search", Visible: true},
	}}
	added, removed, changed := diffSnapshots(before, after, true)
	if len(added) != 1 || added[0].RefKey != "key-3" {
		t.Fatalf("added = %#v", added)
	}
	if len(removed) != 1 || removed[0].RefKey != "key-2" {
		t.Fatalf("removed = %#v", removed)
	}
	if len(changed) != 1 || changed[0].Ref != "e1" || changed[0].Before.Value != "old" || changed[0].After.Value != "new" {
		t.Fatalf("changed = %#v", changed)
	}
}

func TestDiffSnapshotsMatchesRenamedNodeStructurally(t *testing.T) {
	before := SnapshotResult{Refs: map[string]SnapshotRef{"e1": {RefKey: "old-key", Role: "button", Name: "Old", DOMPath: "/document/button[Old]", SiblingOrdinal: 0}}}
	after := SnapshotResult{Refs: map[string]SnapshotRef{"e2": {RefKey: "new-key", Role: "button", Name: "New", DOMPath: "/document/button[New]", SiblingOrdinal: 0}}}
	added, removed, changed := diffSnapshots(before, after, true)
	if len(added) != 0 || len(removed) != 0 || len(changed) != 1 {
		t.Fatalf("diff = added %#v removed %#v changed %#v", added, removed, changed)
	}
	if changed[0].Before.Name != "Old" || changed[0].After.Name != "New" {
		t.Fatalf("changed = %#v", changed[0])
	}
}
