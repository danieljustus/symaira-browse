package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeInspectionEngine struct {
	result InspectionResult
}

func (f *fakeInspectionEngine) Launch(context.Context) error { return nil }
func (f *fakeInspectionEngine) NewContext(context.Context) (Context, error) {
	return Context{ID: "ctx"}, nil
}
func (f *fakeInspectionEngine) NewPage(context.Context, Context, string) (Page, error) {
	return Page{ID: "page"}, nil
}
func (f *fakeInspectionEngine) Navigate(context.Context, Page, string) (NavigationResult, error) {
	return NavigationResult{}, nil
}
func (f *fakeInspectionEngine) Evaluate(context.Context, Page, string) (EvaluationResult, error) {
	return EvaluationResult{}, nil
}
func (f *fakeInspectionEngine) AXTree(context.Context, Page) ([]AXNode, error)   { return nil, nil }
func (f *fakeInspectionEngine) Screenshot(context.Context, Page) ([]byte, error) { return nil, nil }
func (f *fakeInspectionEngine) Close() error                                     { return nil }
func (f *fakeInspectionEngine) Inspect(context.Context, Page, InspectionRequest, *InteractionTarget) (InspectionResult, error) {
	return f.result, nil
}

func TestInspectionResolvesCSSAndPreservesJSONValue(t *testing.T) {
	fake := &fakeInspectionEngine{result: InspectionResult{Value: json.RawMessage(`"Ada"`)}}
	service := NewInspectionService(fake, Page{ID: "page"})
	result, err := service.Inspect(context.Background(), InspectionRequest{Kind: InspectText, Selector: "#name"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != InspectText || result.Selector != "#name" || string(result.Value) != `"Ada"` {
		t.Fatalf("result = %#v", result)
	}
}

func TestInspectionResolvesSnapshotRefAndUnknownRefHintsSnapshot(t *testing.T) {
	fake := &fakeInspectionEngine{result: InspectionResult{Value: json.RawMessage(`true`)}}
	service := NewInspectionService(fake, Page{ID: "page"})
	service.setSnapshotRefs(map[string]SnapshotRef{"e2": {NodeID: "node-2", BackendNodeID: 42}})
	if _, err := service.Inspect(context.Background(), InspectionRequest{Kind: InspectVisible, Selector: "@e2"}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Inspect(context.Background(), InspectionRequest{Kind: InspectVisible, Selector: "@e9"})
	var inspectionErr *InspectionError
	if !errors.As(err, &inspectionErr) || inspectionErr.Code != "unknown_ref" || inspectionErr.Hint == "" {
		t.Fatalf("error = %#v", err)
	}
}

func TestInspectionValidationRequiresAttribute(t *testing.T) {
	fake := &fakeInspectionEngine{}
	service := NewInspectionService(fake, Page{ID: "page"})
	_, err := service.Inspect(context.Background(), InspectionRequest{Kind: InspectAttr, Selector: "#name"})
	if err == nil {
		t.Fatal("missing attribute unexpectedly accepted")
	}
	var inspectionErr *InspectionError
	if !errors.As(err, &inspectionErr) || inspectionErr.Code != "invalid_inspection" {
		t.Fatalf("error = %T %v", err, err)
	}
}
