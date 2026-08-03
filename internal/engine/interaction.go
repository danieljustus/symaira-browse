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
	if err := interactor.PerformInteraction(ctx, s.page, target, request); err != nil {
		return InteractionResult{}, fmt.Errorf("%s %q: %w", request.Action, request.Selector, err)
	}
	return InteractionResult{Action: request.Action, Selector: request.Selector, Ref: ref}, nil
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
