package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzRefKeyNormalization hammers the stable-ref key pipeline (issue #67):
// the key must never be empty and must stay deterministic for the same
// input, regardless of pathological role/name/path values.
func FuzzRefKeyNormalization(f *testing.F) {
	f.Add("button", "Save Changes", "/html/body/main/form[1]/button[2]", 3)
	f.Add("", "", "", 0)
	f.Add("a", strings.Repeat("x", 4096), strings.Repeat("/div", 256), 7)
	f.Add("link", "\x00\x01\x02", "/", -1)
	f.Fuzz(func(t *testing.T, role, name, path string, ordinal int) {
		first := RefKey(role, name, path, ordinal)
		second := RefKey(role, name, path, ordinal)
		if first == "" {
			t.Fatalf("empty ref key for role=%q name=%q", role, name)
		}
		if first != second {
			t.Fatalf("ref key not deterministic: %q vs %q", first, second)
		}
		if strings.ContainsAny(first, "\x00\n\r") {
			t.Fatalf("ref key contains control characters: %q", first)
		}
	})
}

// FuzzSnapshotSerialization feeds arbitrary node payloads through the
// snapshot renderer (issue #67): rendering must never panic and must stay
// deterministic for identical inputs.
func FuzzSnapshotSerialization(f *testing.F) {
	f.Add(`{"role":"button","name":"Save","nodeId":"1","backendDOMNodeId":2,"childIds":[],"ignored":false}`)
	f.Add(`{"role":"","name":"","nodeId":"","backendDOMNodeId":0,"childIds":null,"ignored":true}`)
	f.Add(`not json at all`)
	f.Add(``)
	f.Fuzz(func(t *testing.T, raw string) {
		nodes := []AXNode{{Raw: json.RawMessage(raw)}}
		first, err := RenderSnapshot(nodes, SnapshotOptions{})
		if err != nil {
			return // invalid input must error, not panic
		}
		second, err := RenderSnapshot(nodes, SnapshotOptions{})
		if err != nil {
			t.Fatalf("second render failed after first succeeded: %v", err)
		}
		if first.Tree != second.Tree {
			t.Fatalf("snapshot render not deterministic")
		}
	})
}
