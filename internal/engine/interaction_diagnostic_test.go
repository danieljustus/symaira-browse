package engine

import (
	"strings"
	"testing"
)

func TestObstructedClickErrorIncludesCoveringRefAndHint(t *testing.T) {
	service := NewNavigationService(nil, Page{ID: "page"}, NavigationOptions{})
	service.setSnapshotRefs(map[string]SnapshotRef{
		"e1": {NodeID: "target", BackendNodeID: 10},
		"e2": {NodeID: "overlay", BackendNodeID: 20, Role: "dialog", Name: "Cookie banner"},
	})
	err := service.obstructedClickError(InteractionRequest{Action: ActionClick, Selector: "@e1"}, ClickDiagnostic{
		Target: InteractionTarget{NodeID: "overlay", BackendNodeID: 20},
		Role:   "dialog", Name: "Cookie banner", SuggestedAction: "close cookie banner",
	})
	if err == nil || !strings.Contains(err.Error(), "dialog \"Cookie banner\"") || !strings.Contains(err.Error(), "ref=@e2") {
		t.Fatalf("obstructed error = %v", err)
	}
	var interactionErr *InteractionError
	if !asInteractionError(err, &interactionErr) || interactionErr.Code != "click_obstructed" || interactionErr.Hint != "close cookie banner" {
		t.Fatalf("structured error = %#v", err)
	}
}

func asInteractionError(err error, target **InteractionError) bool {
	value, ok := err.(*InteractionError)
	if ok {
		*target = value
	}
	return ok
}
