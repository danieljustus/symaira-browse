package safari

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// InteractionError is the structured failure returned when a click would be
// intercepted by an overlay or modal. It mirrors the chrome engine's
// interception error so callers cannot tell engines apart by accident.
type InteractionError struct {
	Code    string
	Message string
	Hint    string
}

func (e *InteractionError) Error() string {
	if e == nil {
		return "safari interaction failed"
	}
	return e.Message
}

// ResolveElement maps a CSS selector to an opaque interaction target. Safari
// has no stable backend node id over Apple Events, so the target carries the
// selector itself; PerformInteraction re-resolves it at click time.
func (e *Engine) ResolveElement(_ context.Context, _ engine.Page, selector string) (engine.InteractionTarget, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return engine.InteractionTarget{}, errors.New("safari engine: resolve requires a non-empty selector")
	}
	return engine.InteractionTarget{NodeID: selector}, nil
}

// ScrollIntoView is a no-op: the live Safari session is already on screen, and
// Apple Events expose no reliable scroll primitive without side effects.
func (e *Engine) ScrollIntoView(context.Context, engine.Page, engine.InteractionTarget) error {
	return nil
}

// DiagnoseClick reports whether the element at the target's location is the
// targeted element, or whether an overlay intercepts the click. JavaScript
// click() bypasses hit-testing, so a naive implementation would report success
// for an action a human could not perform. The engine hit-tests first and
// refuses covered targets with the same kind of error chrome raises.
func (e *Engine) DiagnoseClick(ctx context.Context, _ engine.Page, target engine.InteractionTarget) (engine.ClickDiagnostic, error) {
	selector := target.NodeID
	if selector == "" {
		return engine.ClickDiagnostic{}, errors.New("safari engine: diagnose requires a resolved selector")
	}
	expr := fmt.Sprintf(
		`(function(){var el=document.querySelector(%q);if(!el)return JSON.stringify({targeted:false,reason:'no-match'});`+
			`var r=el.getBoundingClientRect();var cx=r.left+r.width/2,cy=r.top+r.height/2;`+
			`var top=document.elementFromPoint(cx,cy);`+
			`if(!top)return JSON.stringify({targeted:false,reason:'offscreen'});`+
			`var covered=!el.contains(top)&&top!==el;`+
			`return JSON.stringify({targeted:!covered,nodename:top?top.nodeName:null});})()`,
		selector)
	out, err := e.evaluateTab(ctx, expr)
	if err != nil {
		return engine.ClickDiagnostic{}, err
	}
	return parseClickDiagnostic(out, selector)
}

func parseClickDiagnostic(raw, selector string) (engine.ClickDiagnostic, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "\"")
	raw = strings.TrimSuffix(raw, "\"")
	var diag struct {
		Targeted bool   `json:"targeted"`
		Reason   string `json:"reason"`
		Nodename string `json:"nodename"`
	}
	if err := json.Unmarshal([]byte(raw), &diag); err != nil {
		// The expression returned something that is not our JSON envelope (for
		// example a thrown error string). Treat as a resolve failure.
		return engine.ClickDiagnostic{
			Target:   engine.InteractionTarget{NodeID: selector},
			Targeted: false,
		}, &engine.InteractionError{Code: "click_diagnostic", Message: "safari engine: could not compute click diagnostic: " + raw}
	}
	return engine.ClickDiagnostic{
		Target:   engine.InteractionTarget{NodeID: selector},
		Targeted: diag.Targeted,
		Role:     diag.Nodename,
	}, nil
}

// PerformInteraction executes a single interaction against the pinned tab. It
// refuses when interactions are not opt-in enabled, refuses click actions whose
// target is covered by an overlay, and returns the typed unsupported error for
// operations Safari cannot perform (upload, cross-origin, press with modifiers
// beyond plain keys handled by do JavaScript).
func (e *Engine) PerformInteraction(ctx context.Context, _ engine.Page, target engine.InteractionTarget, req engine.InteractionRequest) error {
	e.mu.Lock()
	optIn := e.OptInInteractions
	e.mu.Unlock()
	if !optIn {
		return engine.UnsupportedOperation(EngineKind, "interaction (opt-in required)")
	}
	selector := target.NodeID
	if selector == "" {
		return errors.New("safari engine: interaction requires a resolved selector")
	}

	switch req.Action {
	case engine.ActionClick, engine.ActionDoubleClick:
		diag, err := e.DiagnoseClick(ctx, engine.Page{}, target)
		if err != nil {
			if ie, ok := err.(*engine.InteractionError); ok {
				return ie
			}
			return err
		}
		if !diag.Targeted {
			return &InteractionError{
				Code:    "click_intercepted",
				Message: fmt.Sprintf("safari engine: click on %q is intercepted by an overlay; the human cannot hit this element either", selector),
				Hint:    "dismiss the overlay or target an element that is actually clickable",
			}
		}
		return e.clickJS(ctx, selector, req.Action == engine.ActionDoubleClick)
	case engine.ActionFill, engine.ActionType:
		return e.setValueJS(ctx, selector, req.Value)
	case engine.ActionPress:
		return e.pressJS(ctx, selector, req.Key)
	case engine.ActionCheck, engine.ActionUncheck:
		return e.toggleJS(ctx, selector, req.Action == engine.ActionCheck)
	case engine.ActionSelect:
		return e.selectJS(ctx, selector, req.Value)
	default:
		return engine.UnsupportedOperation(EngineKind, string(req.Action))
	}
}

func (e *Engine) clickJS(ctx context.Context, selector string, dbl bool) error {
	expr := fmt.Sprintf(
		`(function(){var el=document.querySelector(%q);if(!el)throw new Error('no-match');`+
			`el.click();%s;return 'ok';})()`,
		selector, ternary(dbl, "el.click();", ""))
	_, err := e.evaluateTab(ctx, expr)
	return err
}

func (e *Engine) setValueJS(ctx context.Context, selector, value string) error {
	// Setting .value does not fire input events; dispatch them so frameworks see
	// the change, matching what chrome's native setter path triggers.
	expr := fmt.Sprintf(
		`(function(){var el=document.querySelector(%q);if(!el)throw new Error('no-match');`+
			`el.value=%q;el.dispatchEvent(new Event('input',{bubbles:true}));`+
			`el.dispatchEvent(new Event('change',{bubbles:true}));return 'ok';})()`,
		selector, value)
	_, err := e.evaluateTab(ctx, expr)
	return err
}

func (e *Engine) pressJS(ctx context.Context, selector, key string) error {
	expr := fmt.Sprintf(
		`(function(){var el=document.querySelector(%q);if(!el)throw new Error('no-match');`+
			`el.dispatchEvent(new KeyboardEvent('keydown',{key:%q,bubbles:true}));`+
			`el.dispatchEvent(new KeyboardEvent('keyup',{key:%q,bubbles:true}));return 'ok';})()`,
		selector, key, key)
	_, err := e.evaluateTab(ctx, expr)
	return err
}

func (e *Engine) toggleJS(ctx context.Context, selector string, check bool) error {
	expr := fmt.Sprintf(
		`(function(){var el=document.querySelector(%q);if(!el)throw new Error('no-match');`+
			`if(el.type!=='checkbox'&&el.type!=='radio')throw new Error('not-toggleable');`+
			`if(el.checked!==%v){el.checked=%v;el.dispatchEvent(new Event('change',{bubbles:true}));}return 'ok';})()`,
		selector, check, check)
	_, err := e.evaluateTab(ctx, expr)
	return err
}

func (e *Engine) selectJS(ctx context.Context, selector, value string) error {
	expr := fmt.Sprintf(
		`(function(){var el=document.querySelector(%q);if(!el)throw new Error('no-match');`+
			`el.value=%q;el.dispatchEvent(new Event('change',{bubbles:true}));return 'ok';})()`,
		selector, value)
	_, err := e.evaluateTab(ctx, expr)
	return err
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
