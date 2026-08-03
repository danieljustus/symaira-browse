package chrome

import (
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestFrameInfoFromRaw(t *testing.T) {
	frame := frameInfoFromRaw("frame-1", "frame-0", "main", "https://example.com")
	if frame.ID != "frame-1" || frame.ParentID != "frame-0" || frame.Name != "main" || frame.URL != "https://example.com" {
		t.Fatalf("frame = %+v", frame)
	}
	// The parent id is normalized to empty when the frame has no parent.
	root := frameInfoFromRaw("frame-0", "", "", "about:blank")
	if root.ParentID != "" {
		t.Fatalf("root parent = %q", root.ParentID)
	}
}

func TestFrameChildrenParsesRawFrames(t *testing.T) {
	engine := &Engine{}
	children := engine.frameChildren([]json.RawMessage{
		json.RawMessage(`{"frame": {"id": "a", "parentId": "", "url": "https://example.com", "name": "main"}, "childFrames": []}`),
		json.RawMessage(`{"frame": {"id": "b", "parentId": "a", "url": "https://example.com/sub", "name": "sub"}, "childFrames": []}`),
	})
	if len(children) != 2 {
		t.Fatalf("children = %d, want 2", len(children))
	}
	if children[0].ID != "a" || children[1].ParentID != "a" || children[1].Name != "sub" {
		t.Fatalf("children = %+v", children)
	}
	// Malformed raw frames are skipped without panicking.
	children = engine.frameChildren([]json.RawMessage{json.RawMessage(`{"frame":`)})
	if len(children) != 0 {
		t.Fatalf("malformed children = %d, want 0", len(children))
	}
}

func TestSameTarget(t *testing.T) {
	if !sameTarget(engine.InteractionTarget{NodeID: "n1", BackendNodeID: 7}, 0, 7) {
		t.Fatal("backend match failed")
	}
	if sameTarget(engine.InteractionTarget{NodeID: "n1", BackendNodeID: 7}, 0, 8) {
		t.Fatal("different backend matched")
	}
}

func TestAttributeValue(t *testing.T) {
	attributes := []string{"class", "btn primary", "disabled", "", "aria-label", "Close"}
	if value := attributeValue(attributes, "class"); value != "btn primary" {
		t.Fatalf("class = %q", value)
	}
	if value := attributeValue(attributes, "aria-label"); value != "Close" {
		t.Fatalf("aria-label = %q", value)
	}
	if value := attributeValue(attributes, "missing"); value != "" {
		t.Fatalf("missing = %q", value)
	}
}
