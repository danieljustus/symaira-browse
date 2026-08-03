package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// handleTabFrame dispatches tab.* protocol commands. Every tab owns its own
// navigation service and ref table; switching tabs preserves refs.
func (r *NavigationRuntime) handleTabFrame(ctx context.Context, frame Frame) (any, error) {
	switch frame.Cmd {
	case "tab.list":
		return r.tabList(ctx, frame.Session)
	case "tab.new":
		var request struct {
			Label string `json:"label,omitempty"`
			URL   string `json:"url,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		return r.tabNew(ctx, frame.Session, request.Label, request.URL)
	case "tab.switch":
		var request struct {
			Tab string `json:"tab"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		return r.tabSwitch(frame.Session, request.Tab)
	case "tab.close":
		var request struct {
			Tab string `json:"tab,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		return r.tabClose(ctx, frame.Session, request.Tab)
	case "window.new":
		return r.tabNew(ctx, frame.Session, "", "about:blank")
	default:
		return nil, fmt.Errorf("unknown tab command %q", frame.Cmd)
	}
}

// tabList returns all tabs of the session with their state.
func (r *NavigationRuntime) tabList(ctx context.Context, session string) (any, error) {
	if _, err := r.service(ctx, session); err != nil {
		return nil, err
	}
	r.mu.Lock()
	tabs := make([]*sessionTab, len(r.tabs[session]))
	copy(tabs, r.tabs[session])
	active := r.activeTab[session]
	r.mu.Unlock()

	result := make([]engine.TabInfo, 0, len(tabs))
	for index, tab := range tabs {
		urlValue := ""
		if tab.Service != nil {
			url, err := tab.Service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectURL})
			if err == nil {
				urlValue = inspectionValue(url)
			}
		}
		result = append(result, engine.TabInfo{
			ID:     fmt.Sprintf("t%d", index+1),
			Label:  tab.Label,
			URL:    urlValue,
			Active: index == active,
		})
	}
	return map[string]any{"tabs": result, "active": fmt.Sprintf("t%d", active+1)}, nil
}

// tabNew opens a new tab and makes it active.
func (r *NavigationRuntime) tabNew(ctx context.Context, session, label, url string) (any, error) {
	browser, contextID, err := r.browserForSession(ctx, session)
	if err != nil {
		return nil, err
	}
	if url == "" {
		url = "about:blank"
	}
	page, err := managerTabNew(browser, ctx, contextID, label, url)
	if err != nil {
		return nil, err
	}
	service := engine.NewNavigationService(browser, page, engine.NavigationOptions{})
	r.mu.Lock()
	tabs := r.tabs[session]
	label = defaultLabel(label, len(tabs))
	r.tabs[session] = append(tabs, &sessionTab{Label: label, Service: service, Page: page})
	r.activeTab[session] = len(r.tabs[session]) - 1
	r.mu.Unlock()
	_ = r.registry.SetActiveTabs(session, len(r.tabs[session]))
	return map[string]any{"tab": fmt.Sprintf("t%d", r.activeTab[session]+1), "label": label}, nil
}

// tabSwitch activates the tab identified by "tN" or its label.
func (r *NavigationRuntime) tabSwitch(session, target string) (any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tabs := r.tabs[session]
	if len(tabs) == 0 {
		return nil, errors.New("session has no tabs")
	}
	target = strings.TrimPrefix(strings.TrimSpace(target), "@")
	index := -1
	if strings.HasPrefix(target, "t") {
		_, _ = fmt.Sscanf(target, "t%d", &index)
		index-- // t1 -> 0
	}
	if index < 0 || index >= len(tabs) {
		for i, tab := range tabs {
			if tab.Label == target {
				index = i
				break
			}
		}
	}
	if index < 0 || index >= len(tabs) {
		return nil, fmt.Errorf("tab %q not found", target)
	}
	r.activeTab[session] = index
	return map[string]any{"tab": fmt.Sprintf("t%d", index+1), "label": tabs[index].Label}, nil
}

// tabClose closes the tab (default: the active one). The last tab cannot be
// closed.
func (r *NavigationRuntime) tabClose(ctx context.Context, session, target string) (any, error) {
	r.mu.Lock()
	tabs := r.tabs[session]
	if len(tabs) == 0 {
		r.mu.Unlock()
		return nil, errors.New("session has no tabs")
	}
	if len(tabs) == 1 {
		r.mu.Unlock()
		return nil, errors.New("cannot close the last tab of a session")
	}
	index := r.activeTab[session]
	if target != "" {
		target = strings.TrimPrefix(target, "@")
		if strings.HasPrefix(target, "t") {
			index = -1
			_, _ = fmt.Sscanf(target, "t%d", &index)
			index--
		} else {
			index = -1
			for i, tab := range tabs {
				if tab.Label == target {
					index = i
					break
				}
			}
		}
	}
	if index < 0 || index >= len(tabs) {
		r.mu.Unlock()
		return nil, fmt.Errorf("tab %q not found", target)
	}
	closing := tabs[index]
	rest := make([]*sessionTab, 0, len(tabs)-1)
	rest = append(rest, tabs[:index]...)
	rest = append(rest, tabs[index+1:]...)
	r.tabs[session] = rest
	if r.activeTab[session] >= len(rest) {
		r.activeTab[session] = len(rest) - 1
	}
	r.mu.Unlock()

	browser, _, err := r.browserForSession(ctx, session)
	if err == nil {
		_ = managerTabClose(browser, ctx, closing.Page)
	}
	_ = r.registry.SetActiveTabs(session, len(rest))
	return map[string]any{"closed": fmt.Sprintf("t%d", index+1), "active": fmt.Sprintf("t%d", r.activeTab[session]+1)}, nil
}

// browserForSession returns the session engine and its browser context,
// starting the session when needed. The engine is returned as the full
// engine.Engine interface; callers type-assert the optional extensions.
func (r *NavigationRuntime) browserForSession(ctx context.Context, session string) (engine.Engine, engine.Context, error) {
	if _, err := r.service(ctx, session); err != nil {
		return nil, engine.Context{}, err
	}
	r.mu.Lock()
	browser := r.engines[session]
	contextID := r.browserContexts[session]
	r.mu.Unlock()
	if browser == nil {
		return nil, engine.Context{}, errors.New("session has no browser engine")
	}
	return browser, contextID, nil
}

// defaultLabel assigns a label to a new tab: the requested label or the
// conventional tN name.
func defaultLabel(requested string, existing int) string {
	if strings.TrimSpace(requested) != "" {
		return requested
	}
	return fmt.Sprintf("t%d", existing+1)
}

// managerTabNew opens a tab via the engine's TabManager extension.
func managerTabNew(browser engine.Engine, ctx context.Context, contextID engine.Context, label, url string) (engine.Page, error) {
	manager, ok := browser.(engine.TabManager)
	if !ok {
		return engine.Page{}, errors.New("browser engine does not support tabs")
	}
	return manager.TabNew(ctx, contextID, label, url)
}

// managerTabClose closes a tab via the engine's TabManager extension.
func managerTabClose(browser engine.Engine, ctx context.Context, page engine.Page) error {
	manager, ok := browser.(engine.TabManager)
	if !ok {
		return errors.New("browser engine does not support tabs")
	}
	return manager.TabClose(ctx, page)
}

// handleFrameFrame dispatches frame.* protocol commands.
func (r *NavigationRuntime) handleFrameFrame(ctx context.Context, frame Frame) (any, error) {
	browser, _, err := r.browserForSession(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	manager, ok := browser.(engine.FrameManager)
	if !ok {
		return nil, errors.New("browser engine does not support frames")
	}
	page, ok := r.tabHandle(frame.Session)
	if !ok {
		return nil, errors.New("session has no active tab")
	}
	switch frame.Cmd {
	case "frame.tree":
		tree, err := manager.FrameTree(ctx, page)
		if err != nil {
			return nil, err
		}
		return map[string]any{"frames": tree}, nil
	case "frame.main":
		if err := manager.SetActiveFrame(ctx, page, ""); err != nil {
			return nil, err
		}
		return map[string]any{"frame": "main"}, nil
	case "frame.select":
		var request struct {
			Frame string `json:"frame"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := manager.SetActiveFrame(ctx, page, request.Frame); err != nil {
			return nil, err
		}
		return map[string]any{"frame": request.Frame}, nil
	default:
		return nil, fmt.Errorf("unknown frame command %q", frame.Cmd)
	}
}

// handleDialogFrame dispatches dialog.* protocol commands.
func (r *NavigationRuntime) handleDialogFrame(ctx context.Context, frame Frame) (any, error) {
	browser, _, err := r.browserForSession(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	controller, ok := browser.(engine.DialogController)
	if !ok {
		return nil, errors.New("browser engine does not support dialogs")
	}
	page, ok := r.tabHandle(frame.Session)
	if !ok {
		return nil, errors.New("session has no active tab")
	}
	switch frame.Cmd {
	case "dialog.status":
		info, err := controller.DialogStatus(ctx, page)
		if err != nil {
			return nil, err
		}
		return info, nil
	case "dialog.accept":
		var request struct {
			Text string `json:"text,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		if err := controller.DialogAccept(ctx, page, request.Text); err != nil {
			return nil, err
		}
		return map[string]any{"handled": true, "action": "accept"}, nil
	case "dialog.dismiss":
		if err := controller.DialogDismiss(ctx, page); err != nil {
			return nil, err
		}
		return map[string]any{"handled": true, "action": "dismiss"}, nil
	case "dialog.auto":
		var request struct {
			Mode string `json:"mode"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := controller.SetDialogAutoMode(request.Mode); err != nil {
			return nil, err
		}
		return map[string]any{"auto_mode": request.Mode}, nil
	default:
		return nil, fmt.Errorf("unknown dialog command %q", frame.Cmd)
	}
}

var _ = json.Marshal
