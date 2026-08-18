package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	cdproto "github.com/chromedp/cdproto"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// ---- TabManager -----------------------------------------------------------

// TabList returns all page targets of the browser context.
func (e *Engine) TabList(ctx context.Context, browserContext engine.Context) ([]engine.TabInfo, error) {
	var result struct {
		TargetInfos []struct {
			TargetID         string `json:"targetId"`
			Type             string `json:"type"`
			URL              string `json:"url"`
			BrowserContextID string `json:"browserContextId,omitempty"`
			Attached         bool   `json:"attached"`
		} `json:"targetInfos"`
	}
	if err := e.call(ctx, "", cdproto.CommandTargetGetTargets, struct{}{}, &result); err != nil {
		return nil, err
	}
	var tabs []engine.TabInfo
	for _, info := range result.TargetInfos {
		if info.Type != "page" {
			continue
		}
		if browserContext.ID != "" && info.BrowserContextID != browserContext.ID {
			continue
		}
		tabs = append(tabs, engine.TabInfo{ID: info.TargetID, URL: info.URL, TargetID: info.TargetID})
	}
	return tabs, nil
}

// TabNew creates a new tab (page target) and attaches a CDP session to it.
// The label is stored client-side by the runtime; the engine returns the page
// handle used for all subsequent operations.
func (e *Engine) TabNew(ctx context.Context, browserContext engine.Context, label, url string) (engine.Page, error) {
	return e.NewPage(ctx, browserContext, url)
}

// TabClose closes the target behind the given page.
func (e *Engine) TabClose(ctx context.Context, page engine.Page) error {
	params := struct {
		TargetID string `json:"targetId"`
	}{page.ID}
	return e.call(ctx, "", cdproto.CommandTargetCloseTarget, params, nil)
}

var _ engine.TabManager = (*Engine)(nil)

// ---- FrameManager ---------------------------------------------------------

// FrameTree returns the nested frame tree of the page.
func (e *Engine) FrameTree(ctx context.Context, page engine.Page) ([]engine.FrameInfo, error) {
	var result struct {
		FrameTree struct {
			Frame struct {
				ID       string `json:"id"`
				ParentID string `json:"parentId,omitempty"`
				Name     string `json:"name,omitempty"`
				URL      string `json:"url,omitempty"`
			} `json:"frame"`
			ChildFrames []json.RawMessage `json:"childFrames"`
		} `json:"frameTree"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandPageGetFrameTree, struct{}{}, &result); err != nil {
		return nil, err
	}
	root := frameInfoFromRaw(result.FrameTree.Frame.ID, result.FrameTree.Frame.ParentID, result.FrameTree.Frame.Name, result.FrameTree.Frame.URL)
	if len(result.FrameTree.ChildFrames) > 0 {
		root.Children = e.frameChildren(result.FrameTree.ChildFrames)
	}
	return []engine.FrameInfo{root}, nil
}

// frameChildren recursively converts raw CDP child-frame payloads.
func (e *Engine) frameChildren(raw []json.RawMessage) []engine.FrameInfo {
	var children []engine.FrameInfo
	for _, item := range raw {
		var node struct {
			Frame struct {
				ID       string `json:"id"`
				ParentID string `json:"parentId,omitempty"`
				Name     string `json:"name,omitempty"`
				URL      string `json:"url,omitempty"`
			} `json:"frame"`
			ChildFrames []json.RawMessage `json:"childFrames"`
		}
		if err := json.Unmarshal(item, &node); err != nil {
			continue
		}
		info := frameInfoFromRaw(node.Frame.ID, node.Frame.ParentID, node.Frame.Name, node.Frame.URL)
		if len(node.ChildFrames) > 0 {
			info.Children = e.frameChildren(node.ChildFrames)
		}
		children = append(children, info)
	}
	return children
}

func frameInfoFromRaw(id, parentID, name, url string) engine.FrameInfo {
	return engine.FrameInfo{ID: id, ParentID: parentID, Name: name, URL: url}
}

// SetActiveFrame selects the frame that subsequent snapshot/inspection
// operations address. The frame is resolved to its execution context via an
// isolated world, so nested iframes (same- and cross-origin) are addressable.
// An empty frame ID selects the main frame.
func (e *Engine) SetActiveFrame(ctx context.Context, page engine.Page, frameID string) error {
	if frameID == "" {
		e.mu.Lock()
		e.activeFrame = ""
		e.activeFrameContext = 0
		e.mu.Unlock()
		return nil
	}
	// Create an isolated world in the target frame; its executionContextId
	// becomes the addressing scope for later Runtime calls. The CDP call must
	// not run while holding e.mu (e.call re-locks it).
	var result struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}
	params := struct {
		FrameID           string `json:"frameId"`
		WorldName         string `json:"worldName,omitempty"`
		GrantUniveralAcce bool   `json:"grantUniveralAccess,omitempty"`
	}{FrameID: frameID}
	if err := e.call(ctx, page.SessionID, cdproto.CommandPageCreateIsolatedWorld, params, &result); err != nil {
		return fmt.Errorf("create isolated world for frame %s: %w", frameID, err)
	}
	if result.ExecutionContextID == 0 {
		return errors.New("chrome returned an empty execution context for the frame")
	}
	e.mu.Lock()
	e.activeFrame = frameID
	e.activeFrameContext = result.ExecutionContextID
	e.mu.Unlock()
	return nil
}

// ActiveFrameContext returns the execution context of the selected frame
// (0 when the main frame is selected).
func (e *Engine) ActiveFrameContext() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activeFrameContext
}

var _ engine.FrameManager = (*Engine)(nil)

// ---- DialogController -----------------------------------------------------

// dialogState tracks the pending dialog and the automatic handling mode.
type dialogState struct {
	pending *engine.DialogInfo
	auto    string // accept | dismiss | off
}

// DialogStatus reports the current pending dialog.
func (e *Engine) DialogStatus(ctx context.Context, page engine.Page) (engine.DialogInfo, error) {
	e.dialogMu.Lock()
	defer e.dialogMu.Unlock()
	if e.dialog.pending == nil {
		return engine.DialogInfo{Handled: true, AutoMode: e.dialog.auto}, nil
	}
	info := *e.dialog.pending
	info.Handled = false
	info.AutoMode = e.dialog.auto
	return info, nil
}

// DialogAccept accepts the pending dialog, optionally providing prompt text.
func (e *Engine) DialogAccept(ctx context.Context, page engine.Page, text string) error {
	e.dialogMu.Lock()
	pending := e.dialog.pending
	if pending == nil {
		e.dialogMu.Unlock()
		return errors.New("no dialog is pending")
	}
	e.dialogMu.Unlock()
	params := struct {
		Accept     bool   `json:"accept"`
		PromptText string `json:"promptText,omitempty"`
	}{Accept: true, PromptText: text}
	if err := e.call(ctx, page.SessionID, cdproto.CommandPageHandleJavaScriptDialog, params, nil); err != nil {
		return err
	}
	e.clearPendingDialog()
	return nil
}

// DialogDismiss dismisses the pending dialog.
func (e *Engine) DialogDismiss(ctx context.Context, page engine.Page) error {
	e.dialogMu.Lock()
	pending := e.dialog.pending
	e.dialogMu.Unlock()
	if pending == nil {
		return errors.New("no dialog is pending")
	}
	params := struct {
		Accept bool `json:"accept"`
	}{Accept: false}
	if err := e.call(ctx, page.SessionID, cdproto.CommandPageHandleJavaScriptDialog, params, nil); err != nil {
		return err
	}
	e.clearPendingDialog()
	return nil
}

// SetDialogAutoMode configures automatic dialog handling. Default is dismiss
// so beforeunload dialogs never block automation.
func (e *Engine) SetDialogAutoMode(mode string) error {
	switch mode {
	case "accept", "dismiss", "off":
		e.dialogMu.Lock()
		e.dialog.auto = mode
		e.dialogMu.Unlock()
		return nil
	default:
		return fmt.Errorf("invalid dialog auto mode %q (use accept, dismiss or off)", mode)
	}
}

func (e *Engine) clearPendingDialog() {
	e.dialogMu.Lock()
	e.dialog.pending = nil
	e.dialogMu.Unlock()
}

// handleDialogEvent processes Page.javascriptDialogOpening/Closed events and
// applies the auto mode (default: dismiss — beforeunload must never block).
func (e *Engine) handleDialogEvent(sessionID, method string, params json.RawMessage) {
	switch method {
	case "Page.javascriptDialogOpening":
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Default string `json:"defaultPrompt"`
		}
		if err := json.Unmarshal(params, &event); err != nil {
			return
		}
		e.dialogMu.Lock()
		e.dialog.pending = &engine.DialogInfo{Type: event.Type, Message: event.Message, Default: event.Default, Handled: false, AutoMode: e.dialog.auto}
		auto := e.dialog.auto
		e.dialogMu.Unlock()
		// beforeunload is always auto-dismissed unless explicitly set to
		// accept: automation must never be blocked by a leave-page prompt.
		if auto == "dismiss" || (auto == "" && event.Type == "beforeunload") {
			params := struct {
				Accept bool `json:"accept"`
			}{Accept: false}
			_ = e.call(context.Background(), sessionID, cdproto.CommandPageHandleJavaScriptDialog, params, nil)
			e.clearPendingDialog()
		}
	case "Page.javascriptDialogClosed":
		e.clearPendingDialog()
	}
}

// dialogMu guards the dialog state; it is separate from e.mu because event
// handlers run on the connection read loop.
