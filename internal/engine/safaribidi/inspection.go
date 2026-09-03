package safaribidi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// Inspect answers one get/is query by evaluating a bounded expression in the
// page. Safari implements no browsingContext.locateNodes, so every query is
// resolved through script.evaluate against a CSS selector.
//
// Snapshot refs are resolved by their DOM path rather than by node handle:
// script.disown exists but Safari's BiDi exposes no way to hand a node handle
// back into a later evaluate, so the selector is the only stable address.
func (e *Engine) Inspect(ctx context.Context, page engine.Page, request engine.InspectionRequest, target *engine.InteractionTarget) (engine.InspectionResult, error) {
	contextID, err := e.pageContext(page)
	if err != nil {
		return engine.InspectionResult{}, err
	}
	selector := strings.TrimSpace(request.Selector)
	if target != nil && strings.HasPrefix(selector, "@") {
		return engine.InspectionResult{}, &engine.InspectionError{
			Code:    "unsupported_ref",
			Message: "safari-bidi resolves inspections by CSS selector; snapshot refs are not addressable in Safari's BiDi",
			Hint:    "pass the element's CSS selector instead of its @ref",
		}
	}

	expression, err := inspectionExpression(request, selector)
	if err != nil {
		return engine.InspectionResult{}, err
	}
	var result struct {
		Type   string `json:"type"`
		Result remote `json:"result"`
	}
	if err := e.call(ctx, "script.evaluate", map[string]any{
		"expression":   expression,
		"target":       map[string]any{"context": contextID},
		"awaitPromise": false,
	}, &result); err != nil {
		return engine.InspectionResult{}, err
	}
	if result.Type == "exception" {
		return engine.InspectionResult{}, &engine.InspectionError{
			Code:    "inspection_failed",
			Message: fmt.Sprintf("inspection %s failed in the page", request.Kind),
		}
	}
	value, err := decodeInspection(request.Kind, result.Result)
	if err != nil {
		return engine.InspectionResult{}, err
	}
	return engine.InspectionResult{Kind: request.Kind, Selector: request.Selector, Value: value}, nil
}

// inspectionExpression builds the in-page expression for one query. The
// selector is embedded as a JSON string literal so a hostile selector cannot
// break out of the expression.
func inspectionExpression(request engine.InspectionRequest, selector string) (string, error) {
	quoted, err := json.Marshal(selector)
	if err != nil {
		return "", fmt.Errorf("safari-bidi engine: encode selector: %w", err)
	}
	quotedAttr, err := json.Marshal(request.Attribute)
	if err != nil {
		return "", fmt.Errorf("safari-bidi engine: encode attribute: %w", err)
	}
	quotedProps, err := json.Marshal(request.Properties)
	if err != nil {
		return "", fmt.Errorf("safari-bidi engine: encode properties: %w", err)
	}

	switch request.Kind {
	case engine.InspectTitle:
		return "document.title", nil
	case engine.InspectURL:
		return "location.href", nil
	case engine.InspectCount:
		return fmt.Sprintf("document.querySelectorAll(%s).length", quoted), nil
	}
	if selector == "" {
		return "", &engine.InspectionError{
			Code:    "invalid_inspection",
			Message: fmt.Sprintf("inspection %s requires a selector", request.Kind),
		}
	}

	const preamble = "const el = document.querySelector(%s); if (!el) throw new Error('no such element');"
	body := ""
	switch request.Kind {
	case engine.InspectText:
		body = "return (el.innerText || el.textContent || '');"
	case engine.InspectHTML:
		body = "return el.outerHTML;"
	case engine.InspectValue:
		body = "return el.value === undefined ? '' : String(el.value);"
	case engine.InspectAttr:
		body = fmt.Sprintf("const v = el.getAttribute(%s); return v === null ? '' : v;", quotedAttr)
	case engine.InspectBox:
		body = "const r = el.getBoundingClientRect(); return JSON.stringify({x: r.x, y: r.y, width: r.width, height: r.height});"
	case engine.InspectStyles:
		body = fmt.Sprintf("const s = window.getComputedStyle(el); const out = {}; for (const p of %s) out[p] = s.getPropertyValue(p); return JSON.stringify(out);", quotedProps)
	case engine.InspectVisible:
		body = "const s = window.getComputedStyle(el); const r = el.getBoundingClientRect(); return s.display !== 'none' && s.visibility !== 'hidden' && parseFloat(s.opacity || '1') > 0 && r.width > 0 && r.height > 0;"
	case engine.InspectEnabled:
		body = "return !(el.disabled === true || el.getAttribute('aria-disabled') === 'true');"
	case engine.InspectChecked:
		body = "return el.checked === true || el.getAttribute('aria-checked') === 'true';"
	default:
		return "", &engine.InspectionError{
			Code:    "invalid_inspection",
			Message: fmt.Sprintf("unsupported inspection %q", request.Kind),
		}
	}
	return fmt.Sprintf("(() => { %s %s })()", fmt.Sprintf(preamble, quoted), body), nil
}

// decodeInspection converts a BiDi RemoteValue to the raw JSON the inspection
// boundary returns. Box and styles are produced as JSON strings in the page,
// so they are unwrapped back into objects here.
func decodeInspection(kind engine.InspectionKind, value remote) (json.RawMessage, error) {
	if kind == engine.InspectBox || kind == engine.InspectStyles {
		var encoded string
		if err := json.Unmarshal(value.Value, &encoded); err != nil {
			return nil, fmt.Errorf("safari-bidi engine: decode %s payload: %w", kind, err)
		}
		return json.RawMessage(encoded), nil
	}
	if len(value.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return value.Value, nil
}
