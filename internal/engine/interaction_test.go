package engine

import (
	"context"
	"errors"
	"testing"
)

type fakeInteractionEngine struct {
	actions []string
	target  InteractionTarget
}

func (f *fakeInteractionEngine) Launch(context.Context) error { return nil }
func (f *fakeInteractionEngine) NewContext(context.Context) (Context, error) {
	return Context{ID: "ctx"}, nil
}
func (f *fakeInteractionEngine) NewPage(context.Context, Context, string) (Page, error) {
	return Page{ID: "page"}, nil
}
func (f *fakeInteractionEngine) Navigate(context.Context, Page, string) (NavigationResult, error) {
	return NavigationResult{}, nil
}
func (f *fakeInteractionEngine) Evaluate(context.Context, Page, string) (EvaluationResult, error) {
	return EvaluationResult{}, nil
}
func (f *fakeInteractionEngine) AXTree(context.Context, Page) ([]AXNode, error)   { return nil, nil }
func (f *fakeInteractionEngine) Screenshot(context.Context, Page) ([]byte, error) { return nil, nil }
func (f *fakeInteractionEngine) Close() error                                     { return nil }
func (f *fakeInteractionEngine) ResolveElement(context.Context, Page, string) (InteractionTarget, error) {
	f.actions = append(f.actions, "resolve")
	return f.target, nil
}
func (f *fakeInteractionEngine) ScrollIntoView(context.Context, Page, InteractionTarget) error {
	f.actions = append(f.actions, "scroll")
	return nil
}
func (f *fakeInteractionEngine) PerformInteraction(_ context.Context, _ Page, _ InteractionTarget, request InteractionRequest) error {
	f.actions = append(f.actions, string(request.Action)+":"+request.Value)
	return nil
}

func TestNavigationServiceInteractionResolvesSnapshotRefAndScrollsFirst(t *testing.T) {
	fake := &fakeInteractionEngine{target: InteractionTarget{NodeID: "node-2", BackendNodeID: 42}}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{})
	service.setSnapshotRefs(map[string]SnapshotRef{"e2": {NodeID: "node-2", BackendNodeID: 42}})

	result, err := service.Interact(context.Background(), InteractionRequest{Action: ActionClick, Selector: "@e2"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ref != "e2" || result.Action != ActionClick {
		t.Fatalf("result = %#v", result)
	}
	if got := fake.actions; len(got) != 2 || got[0] != "scroll" || got[1] != "click:" {
		t.Fatalf("actions = %#v", got)
	}
}

func TestNavigationServiceUnknownRefHintsSnapshot(t *testing.T) {
	fake := &fakeInteractionEngine{}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{})
	_, err := service.Interact(context.Background(), InteractionRequest{Action: ActionClick, Selector: "@e2"})
	if err == nil {
		t.Fatal("unknown ref unexpectedly succeeded")
	}
	var interactionErr *InteractionError
	if !errors.As(err, &interactionErr) || interactionErr.Code != "unknown_ref" {
		t.Fatalf("error = %T %v", err, err)
	}
	if interactionErr.Hint == "" || !containsString(interactionErr.Hint, "snapshot") {
		t.Fatalf("hint = %q", interactionErr.Hint)
	}
}

func TestNavigationServiceFillAndTypePreserveDistinctSemantics(t *testing.T) {
	fake := &fakeInteractionEngine{}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{})
	for _, request := range []InteractionRequest{
		{Action: ActionFill, Selector: "#name", Value: "Ada"},
		{Action: ActionType, Selector: "#name", Value: " Lovelace"},
	} {
		if _, err := service.Interact(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"resolve", "scroll", "fill:Ada", "resolve", "scroll", "type: Lovelace"}
	if len(fake.actions) != len(want) {
		t.Fatalf("actions = %#v", fake.actions)
	}
	for index := range want {
		if fake.actions[index] != want[index] {
			t.Fatalf("actions = %#v, want %#v", fake.actions, want)
		}
	}
}

func containsString(value, want string) bool {
	for index := 0; index+len(want) <= len(value); index++ {
		if value[index:index+len(want)] == want {
			return true
		}
	}
	return false
}
