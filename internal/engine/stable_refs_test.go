package engine

import (
	"strings"
	"testing"
)

func TestStableRefRegistryPreservesNumbersAndTombstones(t *testing.T) {
	registry := newStableRefRegistry()
	first := registry.apply(SnapshotResult{
		Tree: "- button [ref=e1]",
		Refs: map[string]SnapshotRef{"e1": {Role: "button", Name: "Save", RefKey: "key-save"}},
	})
	if _, ok := first.Refs["e1"]; !ok {
		t.Fatalf("first refs = %#v", first.Refs)
	}

	second := registry.apply(SnapshotResult{
		Tree: "- button [ref=e1]",
		Refs: map[string]SnapshotRef{"e7": {Role: "button", Name: "Save", RefKey: "key-save"}},
	})
	if _, ok := second.Refs["e1"]; !ok {
		t.Fatalf("stable ref was not preserved: %#v", second.Refs)
	}
	if !strings.Contains(second.Tree, "[ref=e1]") {
		t.Fatalf("tree did not rewrite ref: %q", second.Tree)
	}

	registry.apply(SnapshotResult{Refs: map[string]SnapshotRef{"e2": {Role: "link", Name: "Home", RefKey: "key-home"}}})
	_, tombstone, ok := registry.resolve("e1")
	if !ok || tombstone == nil || tombstone.Reason != "removed" || tombstone.Role != "button" || tombstone.Name != "Save" {
		t.Fatalf("tombstone = %#v, ok=%t", tombstone, ok)
	}
}

func TestStableRefRegistryNeverReusesNumberAfterNavigation(t *testing.T) {
	registry := newStableRefRegistry()
	registry.apply(SnapshotResult{Refs: map[string]SnapshotRef{"e1": {Role: "button", RefKey: "key"}}})
	registry.invalidate("navigated")
	result := registry.apply(SnapshotResult{Refs: map[string]SnapshotRef{"e1": {Role: "button", RefKey: "key"}}})
	if _, ok := result.Refs["e2"]; !ok {
		t.Fatalf("navigation reused a ref number: %#v", result.Refs)
	}
	if _, ok := result.Refs["e1"]; ok {
		t.Fatalf("old ref was recycled: %#v", result.Refs)
	}
}

func TestRefKeyIsDeterministicAndPathSensitive(t *testing.T) {
	key := RefKey("button", " Save ", "/document/button", 0)
	if key != RefKey("button", "Save", "/document/button", 0) {
		t.Fatal("whitespace normalization is not deterministic")
	}
	if key == RefKey("button", "Save", "/document/button", 1) {
		t.Fatal("sibling ordinal was ignored")
	}
	if len(key) != 64 {
		t.Fatalf("key length = %d", len(key))
	}
}
