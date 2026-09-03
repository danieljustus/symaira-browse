package safaribidi

import (
	"context"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// bidiContext is one node of Safari's browsing-context tree.
type bidiContext struct {
	Context  string        `json:"context"`
	URL      string        `json:"url"`
	Parent   string        `json:"parent"`
	Children []bidiContext `json:"children"`
}

func (e *Engine) contextTree(ctx context.Context) ([]bidiContext, error) {
	var tree struct {
		Contexts []bidiContext `json:"contexts"`
	}
	if err := e.call(ctx, "browsingContext.getTree", map[string]any{}, &tree); err != nil {
		return nil, fmt.Errorf("safari-bidi engine: read browsing context tree: %w", err)
	}
	return tree.Contexts, nil
}

// TabList reports the session's top-level browsing contexts.
func (e *Engine) TabList(ctx context.Context, _ engine.Context) ([]engine.TabInfo, error) {
	contexts, err := e.contextTree(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	active := e.context
	e.mu.Unlock()
	tabs := make([]engine.TabInfo, 0, len(contexts))
	for _, node := range contexts {
		tabs = append(tabs, engine.TabInfo{
			ID:       node.Context,
			URL:      node.URL,
			Active:   node.Context == active,
			TargetID: node.Context,
		})
	}
	return tabs, nil
}

// TabNew opens a new top-level browsing context, optionally navigating it.
// The URL passes the same policy gate as any other navigation.
func (e *Engine) TabNew(ctx context.Context, _ engine.Context, _ string, target string) (engine.Page, error) {
	var created struct {
		Context string `json:"context"`
	}
	if err := e.call(ctx, "browsingContext.create", map[string]any{"type": "tab"}, &created); err != nil {
		return engine.Page{}, fmt.Errorf("safari-bidi engine: open tab: %w", err)
	}
	if created.Context == "" {
		return engine.Page{}, errors.New("safari-bidi engine: Safari returned no context for the new tab")
	}
	page := engine.Page{ID: created.Context}
	if target != "" {
		if _, err := e.Navigate(ctx, page, target); err != nil {
			// The tab exists but is unusable for the requested target; close it
			// rather than leaking an automation window the caller did not ask for.
			_ = e.TabClose(ctx, page)
			return engine.Page{}, err
		}
	}
	return page, nil
}

// TabClose closes the browsing context behind a page.
func (e *Engine) TabClose(ctx context.Context, page engine.Page) error {
	contextID, err := e.pageContext(page)
	if err != nil {
		return err
	}
	if err := e.call(ctx, "browsingContext.close", map[string]any{"context": contextID}, nil); err != nil {
		return fmt.Errorf("safari-bidi engine: close tab: %w", err)
	}
	return nil
}

// FrameTree reports the nested frame tree.
//
// This is a capability safari-attach does not have: BiDi models every frame as
// its own browsing context, so a cross-origin iframe is addressable rather
// than a null contentDocument. Measured against the /iframe fixture, the child
// and grandchild frames both appear as contexts.
func (e *Engine) FrameTree(ctx context.Context, page engine.Page) ([]engine.FrameInfo, error) {
	contextID, err := e.pageContext(page)
	if err != nil {
		return nil, err
	}
	contexts, err := e.contextTree(ctx)
	if err != nil {
		return nil, err
	}
	for _, node := range contexts {
		if node.Context == contextID {
			return []engine.FrameInfo{frameInfo(node, "")}, nil
		}
	}
	return nil, fmt.Errorf("safari-bidi engine: browsing context %q is gone", contextID)
}

func frameInfo(node bidiContext, parent string) engine.FrameInfo {
	info := engine.FrameInfo{ID: node.Context, ParentID: parent, URL: node.URL}
	for _, child := range node.Children {
		info.Children = append(info.Children, frameInfo(child, node.Context))
	}
	return info
}

// SetActiveFrame points subsequent operations at a frame. Frames are ordinary
// browsing contexts in BiDi, so selecting one is rebinding the engine's
// context after verifying the frame is really in this page's tree.
func (e *Engine) SetActiveFrame(ctx context.Context, page engine.Page, frameID string) error {
	if frameID == "" {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.rootContext == "" {
			return errors.New("safari-bidi engine: not launched")
		}
		e.context = e.rootContext
		return nil
	}
	contexts, err := e.contextTree(ctx)
	if err != nil {
		return err
	}
	if !containsContext(contexts, frameID) {
		return fmt.Errorf("safari-bidi engine: no frame %q in this session", frameID)
	}
	e.mu.Lock()
	e.context = frameID
	e.mu.Unlock()
	return nil
}

func containsContext(nodes []bidiContext, id string) bool {
	for _, node := range nodes {
		if node.Context == id || containsContext(node.Children, id) {
			return true
		}
	}
	return false
}
