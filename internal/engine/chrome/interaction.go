package chrome

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cdproto "github.com/chromedp/cdproto"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

// ResolveElement resolves a CSS selector through the DOM agent.
func (e *Engine) ResolveElement(ctx context.Context, page engine.Page, selector string) (engine.InteractionTarget, error) {
	var document struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMGetDocument, struct{}{}, &document); err != nil {
		return engine.InteractionTarget{}, err
	}
	if document.Root.NodeID == 0 {
		return engine.InteractionTarget{}, errors.New("DOM returned an empty document root")
	}
	var queried struct {
		NodeID int64 `json:"nodeId"`
	}
	params := struct {
		NodeID   int64  `json:"nodeId"`
		Selector string `json:"selector"`
	}{document.Root.NodeID, selector}
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMQuerySelector, params, &queried); err != nil {
		return engine.InteractionTarget{}, fmt.Errorf("query selector %q: %w", selector, err)
	}
	if queried.NodeID == 0 {
		return engine.InteractionTarget{}, fmt.Errorf("selector %q did not match an element", selector)
	}
	return e.describeTarget(ctx, page, queried.NodeID)
}

func (e *Engine) describeTarget(ctx context.Context, page engine.Page, nodeID int64) (engine.InteractionTarget, error) {
	var described struct {
		Node struct {
			NodeID        int64 `json:"nodeId"`
			BackendNodeID int64 `json:"backendNodeId"`
		} `json:"node"`
	}
	params := struct {
		NodeID int64 `json:"nodeId"`
	}{nodeID}
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMDescribeNode, params, &described); err != nil {
		return engine.InteractionTarget{}, err
	}
	if described.Node.BackendNodeID == 0 {
		return engine.InteractionTarget{}, errors.New("DOM returned an empty backend node id")
	}
	return engine.InteractionTarget{NodeID: fmt.Sprint(described.Node.NodeID), BackendNodeID: described.Node.BackendNodeID}, nil
}

func (e *Engine) ScrollIntoView(ctx context.Context, page engine.Page, target engine.InteractionTarget) error {
	params := struct {
		BackendNodeID int64 `json:"backendNodeId"`
	}{target.BackendNodeID}
	return e.call(ctx, page.SessionID, cdproto.CommandDOMScrollIntoViewIfNeeded, params, nil)
}

func (e *Engine) PerformInteraction(ctx context.Context, page engine.Page, target engine.InteractionTarget, request engine.InteractionRequest) error {
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMFocus, struct {
		BackendNodeID int64 `json:"backendNodeId"`
	}{target.BackendNodeID}, nil); err != nil {
		return fmt.Errorf("focus target: %w", err)
	}
	if request.Action == engine.ActionScrollIntoView || request.Action == engine.ActionFocus {
		return nil
	}
	if request.Action == engine.ActionFill {
		if err := e.key(ctx, page, "Control", "a", "KeyA", 65); err != nil {
			return err
		}
		if err := e.key(ctx, page, "", "Backspace", "Backspace", 8); err != nil {
			return err
		}
		return e.insertText(ctx, page, request.Value)
	}
	if request.Action == engine.ActionType {
		return e.insertText(ctx, page, request.Value)
	}
	if request.Action == engine.ActionPress {
		return e.key(ctx, page, "", request.Key, keyCode(request.Key), keyVK(request.Key))
	}
	if request.Action == engine.ActionHover {
		return e.mouse(ctx, page, target, "mouseMoved", 0)
	}
	if request.Action == engine.ActionScroll {
		amount := request.Amount
		if amount == 0 {
			amount = 480
		}
		return e.mouse(ctx, page, target, "mouseWheel", amount)
	}
	if request.Action == engine.ActionSelect {
		return e.insertText(ctx, page, request.Value)
	}
	clicks := int64(1)
	if request.Action == engine.ActionDoubleClick {
		clicks = 2
	}
	if request.Action == engine.ActionCheck || request.Action == engine.ActionUncheck || request.Action == engine.ActionClick || request.Action == engine.ActionDoubleClick {
		return e.mouse(ctx, page, target, "mousePressed", clicks)
	}
	return nil
}

func (e *Engine) insertText(ctx context.Context, page engine.Page, value string) error {
	return e.call(ctx, page.SessionID, cdproto.CommandInputInsertText, struct {
		Text string `json:"text"`
	}{value}, nil)
}

func (e *Engine) key(ctx context.Context, page engine.Page, modifier, key, code string, vk int64) error {
	params := struct {
		Type                  string `json:"type"`
		Modifiers             int64  `json:"modifiers"`
		Key                   string `json:"key"`
		Code                  string `json:"code"`
		Text                  string `json:"text,omitempty"`
		WindowsVirtualKeyCode int64  `json:"windowsVirtualKeyCode,omitempty"`
	}{"keyDown", 0, key, code, "", vk}
	if modifier == "Control" {
		params.Modifiers = 2
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandInputDispatchKeyEvent, params, nil); err != nil {
		return err
	}
	params.Type = "keyUp"
	return e.call(ctx, page.SessionID, cdproto.CommandInputDispatchKeyEvent, params, nil)
}

func (e *Engine) mouse(ctx context.Context, page engine.Page, target engine.InteractionTarget, event string, value int64) error {
	x, y, err := e.center(ctx, page, target)
	if err != nil {
		return err
	}
	params := struct {
		Type       string  `json:"type"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		Button     string  `json:"button,omitempty"`
		ClickCount int64   `json:"clickCount,omitempty"`
		DeltaY     float64 `json:"deltaY,omitempty"`
	}{Type: event, X: x, Y: y}
	if event == "mouseWheel" {
		params.DeltaY = float64(value)
	} else if event != "mouseMoved" {
		params.Button = "left"
		params.ClickCount = value
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandInputDispatchMouseEvent, params, nil); err != nil {
		return err
	}
	if event == "mousePressed" {
		params.Type = "mouseReleased"
		return e.call(ctx, page.SessionID, cdproto.CommandInputDispatchMouseEvent, params, nil)
	}
	return nil
}

func (e *Engine) center(ctx context.Context, page engine.Page, target engine.InteractionTarget) (float64, float64, error) {
	var result struct {
		Quads [][]float64 `json:"quads"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMGetContentQuads, struct {
		BackendNodeID int64 `json:"backendNodeId"`
	}{target.BackendNodeID}, &result); err != nil {
		return 0, 0, err
	}
	if len(result.Quads) == 0 || len(result.Quads[0]) < 8 {
		return 0, 0, errors.New("target has no visible content quad")
	}
	var x, y float64
	for i := 0; i < 8; i += 2 {
		x += result.Quads[0][i]
		y += result.Quads[0][i+1]
	}
	return x / 4, y / 4, nil
}

func keyCode(key string) string {
	if len(key) == 1 {
		return "Key" + strings.ToUpper(key)
	}
	return key
}
func keyVK(key string) int64 {
	if len(key) == 1 {
		return int64(strings.ToUpper(key)[0])
	}
	switch strings.ToLower(key) {
	case "enter":
		return 13
	case "backspace":
		return 8
	case "tab":
		return 9
	case "escape":
		return 27
	}
	return 0
}

var _ engine.InteractionEngine = (*Engine)(nil)
