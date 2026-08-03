package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// InspectionKind identifies a get or is query.
type InspectionKind string

const (
	InspectText    InspectionKind = "text"
	InspectHTML    InspectionKind = "html"
	InspectValue   InspectionKind = "value"
	InspectAttr    InspectionKind = "attr"
	InspectTitle   InspectionKind = "title"
	InspectURL     InspectionKind = "url"
	InspectCount   InspectionKind = "count"
	InspectBox     InspectionKind = "box"
	InspectStyles  InspectionKind = "styles"
	InspectVisible InspectionKind = "visible"
	InspectEnabled InspectionKind = "enabled"
	InspectChecked InspectionKind = "checked"
)

// InspectionRequest describes one protocol-neutral DOM or page inspection.
// Selector may be a CSS selector or a ref from the most recent snapshot. Title
// and URL also work without a selector and then inspect the current page.
type InspectionRequest struct {
	Kind       InspectionKind `json:"kind"`
	Selector   string         `json:"selector,omitempty"`
	Attribute  string         `json:"attribute,omitempty"`
	Properties []string       `json:"properties,omitempty"`
}

// InspectionResult is the stable result shape for get and is commands. Value is
// intentionally raw JSON so strings, booleans, numbers, and objects retain
// their natural JSON types.
type InspectionResult struct {
	Kind     InspectionKind  `json:"kind"`
	Selector string          `json:"selector,omitempty"`
	Value    json.RawMessage `json:"value"`
}

// InspectionEngine is the optional engine capability used by inspection
// commands. The target is nil for CSS/page queries and populated for snapshot
// refs. It deliberately contains no CDP types.
type InspectionEngine interface {
	Inspect(context.Context, Page, InspectionRequest, *InteractionTarget) (InspectionResult, error)
}

// InspectionError is a structured, protocol-neutral inspection failure.
type InspectionError struct {
	Code    string
	Message string
	Hint    string
}

func (e *InspectionError) Error() string {
	if e == nil {
		return "inspection failed"
	}
	return e.Message
}

// NewInspectionService creates an inspection-capable navigation service. The
// service shares the snapshot ref map with Snapshot and interaction calls.
func NewInspectionService(browser Engine, page Page) *NavigationService {
	return NewNavigationService(browser, page, NavigationOptions{})
}

// Inspect executes one get or is query.
func (s *NavigationService) Inspect(ctx context.Context, request InspectionRequest) (InspectionResult, error) {
	if err := validateInspectionRequest(request); err != nil {
		return InspectionResult{}, err
	}
	inspector, ok := s.engine.(InspectionEngine)
	if !ok {
		return InspectionResult{}, fmt.Errorf("browser engine does not support inspection %s", request.Kind)
	}

	request.Selector = strings.TrimSpace(request.Selector)
	var target *InteractionTarget
	if strings.HasPrefix(request.Selector, "@") {
		ref := strings.TrimPrefix(request.Selector, "@")
		s.refMu.RLock()
		snapshotRef, found := s.refs[ref]
		s.refMu.RUnlock()
		if !found {
			return InspectionResult{}, &InspectionError{
				Code:    "unknown_ref",
				Message: fmt.Sprintf("unknown element ref %q", request.Selector),
				Hint:    "run snapshot first to refresh the element ref map",
			}
		}
		target = &InteractionTarget{NodeID: snapshotRef.NodeID, BackendNodeID: snapshotRef.BackendNodeID}
	}
	result, err := inspector.Inspect(ctx, s.page, request, target)
	if err != nil {
		return InspectionResult{}, err
	}
	if result.Kind == "" {
		result.Kind = request.Kind
	}
	if result.Selector == "" && request.Selector != "" {
		result.Selector = request.Selector
	}
	if len(result.Value) == 0 {
		return InspectionResult{}, errors.New("inspection engine returned an empty value")
	}
	return result, nil
}

func validateInspectionRequest(request InspectionRequest) error {
	valid := map[InspectionKind]bool{
		InspectText: true, InspectHTML: true, InspectValue: true, InspectAttr: true,
		InspectTitle: true, InspectURL: true, InspectCount: true, InspectBox: true,
		InspectStyles: true, InspectVisible: true, InspectEnabled: true, InspectChecked: true,
	}
	if !valid[request.Kind] {
		return &InspectionError{Code: "invalid_inspection", Message: fmt.Sprintf("unsupported inspection kind %q", request.Kind)}
	}
	selector := strings.TrimSpace(request.Selector)
	if request.Kind == InspectTitle || request.Kind == InspectURL {
		if selector == "" {
			return nil
		}
	}
	if selector == "" {
		return &InspectionError{Code: "invalid_inspection", Message: fmt.Sprintf("get %s requires a selector", request.Kind)}
	}
	if request.Kind == InspectAttr && strings.TrimSpace(request.Attribute) == "" {
		return &InspectionError{Code: "invalid_inspection", Message: "get attr requires an attribute name"}
	}
	return nil
}
