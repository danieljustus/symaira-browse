package engine

import "context"

// TabInfo describes one open tab of a session.
type TabInfo struct {
	ID       string `json:"id"`
	Label    string `json:"label,omitempty"`
	URL      string `json:"url,omitempty"`
	Active   bool   `json:"active"`
	TargetID string `json:"target_id,omitempty"`
}

// FrameInfo describes one frame of the active page (nested iframes included).
type FrameInfo struct {
	ID       string      `json:"id"`
	ParentID string      `json:"parent_id,omitempty"`
	Name     string      `json:"name,omitempty"`
	URL      string      `json:"url,omitempty"`
	Children []FrameInfo `json:"children,omitempty"`
}

// DialogInfo describes a pending JavaScript dialog.
type DialogInfo struct {
	Type     string `json:"type,omitempty"` // alert, confirm, prompt, beforeunload
	Message  string `json:"message,omitempty"`
	Default  string `json:"default,omitempty"`
	Handled  bool   `json:"handled"`
	AutoMode string `json:"auto_mode,omitempty"` // accept | dismiss | off
}

// TabManager is an optional engine extension for multi-tab sessions. A tab is
// one page (target) with its own ref table; switching tabs preserves refs.
type TabManager interface {
	// TabList returns all tabs of the browser context with their state.
	TabList(context.Context, Context) ([]TabInfo, error)
	// TabNew opens a new tab with an optional label and URL.
	TabNew(context.Context, Context, string, string) (Page, error)
	// TabClose closes the tab behind the given page.
	TabClose(context.Context, Page) error
}

// FrameManager is an optional engine extension for nested-frame addressing.
// The selected frame scopes snapshot/inspection/interaction of the service.
type FrameManager interface {
	// FrameTree returns the nested frame tree of the page.
	FrameTree(context.Context, Page) ([]FrameInfo, error)
	// SetActiveFrame selects the frame that subsequent operations address.
	// An empty frame ID addresses the main frame.
	SetActiveFrame(context.Context, Page, string) error
}

// DialogController is an optional engine extension for JavaScript dialogs.
// beforeunload dialogs must never block automation: they are auto-dismissed
// unless the caller explicitly opts out.
type DialogController interface {
	// DialogStatus reports the current pending dialog.
	DialogStatus(context.Context, Page) (DialogInfo, error)
	// DialogAccept accepts the pending dialog, optionally typing text into
	// prompt dialogs.
	DialogAccept(context.Context, Page, string) error
	// DialogDismiss dismisses the pending dialog.
	DialogDismiss(context.Context, Page) error
	// SetDialogAutoMode configures automatic dialog handling: accept,
	// dismiss or off. Default is dismiss (beforeunload never blocks).
	SetDialogAutoMode(string) error
}
