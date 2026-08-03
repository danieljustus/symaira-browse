package engine

import (
	"context"
	"fmt"
	"strings"
)

// InteractionAction names the trusted browser actions supported by B-10.
type InteractionAction string

const (
	ActionClick          InteractionAction = "click"
	ActionDoubleClick    InteractionAction = "dblclick"
	ActionFill           InteractionAction = "fill"
	ActionType           InteractionAction = "type"
	ActionPress          InteractionAction = "press"
	ActionHover          InteractionAction = "hover"
	ActionFocus          InteractionAction = "focus"
	ActionSelect         InteractionAction = "select"
	ActionCheck          InteractionAction = "check"
	ActionUncheck        InteractionAction = "uncheck"
	ActionScroll         InteractionAction = "scroll"
	ActionScrollIntoView InteractionAction = "scrollintoview"
)

// InteractionRequest describes one page interaction. Selector accepts either a
// CSS selector or a ref returned by the most recent snapshot for this page.
type InteractionRequest struct {
	Action   InteractionAction `json:"action"`
	Selector string            `json:"selector"`
	Value    string            `json:"value,omitempty"`
	Values   []string          `json:"values,omitempty"`
	Key      string            `json:"key,omitempty"`
	Amount   int64             `json:"amount,omitempty"`
}

// InteractionTarget is the protocol-neutral identity of one DOM element.
// Concrete engines translate these IDs to their protocol-specific types.
type InteractionTarget struct {
	NodeID        string
	BackendNodeID int64
}

// InteractionResult is the stable result returned by interaction commands.
type InteractionResult struct {
	Action   InteractionAction `json:"action"`
	Selector string            `json:"selector"`
	Ref      string            `json:"ref,omitempty"`
}

// ClickDiagnostic describes the element that receives a click at the target's
// center. It deliberately contains no CDP types so the interaction boundary
// stays protocol-neutral.
type ClickDiagnostic struct {
	Target          InteractionTarget
	Targeted        bool
	Role            string
	Name            string
	SuggestedAction string
}

// ClickDiagnosticEngine is an optional engine capability used to reject a
// click that would be intercepted by an overlay or modal.
type ClickDiagnosticEngine interface {
	DiagnoseClick(context.Context, Page, InteractionTarget) (ClickDiagnostic, error)
}

// InteractionEngine is the optional engine capability used by the interaction
// service. It deliberately contains no CDP types so tests can inject a fake.
type InteractionEngine interface {
	ResolveElement(context.Context, Page, string) (InteractionTarget, error)
	ScrollIntoView(context.Context, Page, InteractionTarget) error
	PerformInteraction(context.Context, Page, InteractionTarget, InteractionRequest) error
}

// InteractionError is a structured, protocol-neutral interaction failure.
type InteractionError struct {
	Code    string
	Message string
	Hint    string
}

func (e *InteractionError) Error() string {
	if e == nil {
		return "interaction failed"
	}
	return e.Message
}

func (s *NavigationService) Interact(ctx context.Context, request InteractionRequest) (InteractionResult, error) {
	if err := validateInteractionRequest(request); err != nil {
		return InteractionResult{}, err
	}
	interactor, ok := s.engine.(InteractionEngine)
	if !ok {
		return InteractionResult{}, fmt.Errorf("browser engine does not support %s", request.Action)
	}

	target, ref, err := s.resolveInteractionTarget(ctx, interactor, request.Selector)
	if err != nil {
		return InteractionResult{}, err
	}
	if err := interactor.ScrollIntoView(ctx, s.page, target); err != nil {
		return InteractionResult{}, fmt.Errorf("scroll %s target %q into view: %w", request.Action, request.Selector, err)
	}
	if isClickInteraction(request.Action) {
		if diagnosticEngine, ok := interactor.(ClickDiagnosticEngine); ok {
			diagnostic, err := diagnosticEngine.DiagnoseClick(ctx, s.page, target)
			if err != nil {
				return InteractionResult{}, fmt.Errorf("diagnose %s target %q: %w", request.Action, request.Selector, err)
			}
			if !diagnostic.Targeted {
				return InteractionResult{}, s.obstructedClickError(request, diagnostic)
			}
		}
	}
	if err := interactor.PerformInteraction(ctx, s.page, target, request); err != nil {
		return InteractionResult{}, fmt.Errorf("%s %q: %w", request.Action, request.Selector, err)
	}
	return InteractionResult{Action: request.Action, Selector: request.Selector, Ref: ref}, nil
}

func isClickInteraction(action InteractionAction) bool {
	switch action {
	case ActionClick, ActionDoubleClick, ActionCheck, ActionUncheck:
		return true
	default:
		return false
	}
}

func (s *NavigationService) obstructedClickError(request InteractionRequest, diagnostic ClickDiagnostic) error {
	role := diagnostic.Role
	if role == "" {
		role = "element"
	}
	name := diagnostic.Name
	if name == "" {
		name = "unnamed"
	}
	ref := s.refForTarget(diagnostic.Target)
	if ref == "" {
		ref = "unavailable"
	} else {
		ref = "@" + ref
	}
	hint := diagnostic.SuggestedAction
	if hint == "" {
		hint = "close the covering element and retry the click"
	}
	return &InteractionError{
		Code:    "click_obstructed",
		Message: fmt.Sprintf("%s %q was obstructed by %s %q (ref=%s)", request.Action, request.Selector, role, name, ref),
		Hint:    hint,
	}
}

func (s *NavigationService) refForTarget(target InteractionTarget) string {
	s.refMu.RLock()
	defer s.refMu.RUnlock()
	for ref, snapshotRef := range s.refs {
		if target.BackendNodeID != 0 && snapshotRef.BackendNodeID == target.BackendNodeID {
			return ref
		}
		if target.NodeID != "" && snapshotRef.NodeID == target.NodeID {
			return ref
		}
	}
	return ""
}

func (s *NavigationService) resolveInteractionTarget(ctx context.Context, interactor InteractionEngine, selector string) (InteractionTarget, string, error) {
	if strings.HasPrefix(strings.TrimSpace(selector), "@") {
		ref := strings.TrimPrefix(strings.TrimSpace(selector), "@")
		s.refMu.RLock()
		snapshotRef, found := s.refs[ref]
		var tombstone *RefTombstone
		if s.refRegistry != nil {
			if registryRef, registryTombstone, registryFound := s.refRegistry.resolve(ref); registryFound {
				snapshotRef, tombstone, found = registryRef, registryTombstone, true
			}
		}
		s.refMu.RUnlock()
		if tombstone != nil {
			return InteractionTarget{}, "", &InteractionError{Code: "stale_ref", Message: staleRefMessage(selector, tombstone), Hint: staleRefHint}
		}
		if !found {
			return InteractionTarget{}, "", &InteractionError{
				Code:    "unknown_ref",
				Message: fmt.Sprintf("unknown element ref %q", selector),
				Hint:    "run snapshot first to refresh the element ref map",
			}
		}
		return InteractionTarget{NodeID: snapshotRef.NodeID, BackendNodeID: snapshotRef.BackendNodeID}, ref, nil
	}
	target, err := interactor.ResolveElement(ctx, s.page, selector)
	return target, "", err
}

func validateInteractionRequest(request InteractionRequest) error {
	if strings.TrimSpace(request.Selector) == "" {
		return &InteractionError{Code: "invalid_interaction", Message: "interaction selector is required"}
	}
	switch request.Action {
	case ActionClick, ActionDoubleClick, ActionFill, ActionType, ActionPress, ActionHover, ActionFocus, ActionSelect, ActionCheck, ActionUncheck, ActionScroll, ActionScrollIntoView:
	default:
		return &InteractionError{Code: "invalid_interaction", Message: fmt.Sprintf("unsupported interaction action %q", request.Action)}
	}
	if request.Action == ActionPress && strings.TrimSpace(request.Key) == "" {
		return &InteractionError{Code: "invalid_interaction", Message: "press key is required"}
	}
	if request.Action == ActionFill || request.Action == ActionType {
		if request.Value == "" {
			return &InteractionError{Code: "invalid_interaction", Message: fmt.Sprintf("%s value is required", request.Action)}
		}
	}
	if request.Action == ActionSelect && request.Value == "" && len(request.Values) == 0 {
		return &InteractionError{Code: "invalid_interaction", Message: "select value is required"}
	}
	return nil
}

func (s *NavigationService) setSnapshotRefs(refs map[string]SnapshotRef) {
	s.refMu.Lock()
	s.refs = make(map[string]SnapshotRef, len(refs))
	for ref, snapshotRef := range refs {
		s.refs[ref] = snapshotRef
	}
	s.refMu.Unlock()
}

// LookupRef resolves a session ref to its snapshot metadata. It is used by
// flow recording to convert session-bound @eN refs back into semantic
// selectors.
func (s *NavigationService) LookupRef(ref string) (SnapshotRef, bool) {
	s.refMu.RLock()
	defer s.refMu.RUnlock()
	snapshotRef, found := s.refs[ref]
	return snapshotRef, found
}
