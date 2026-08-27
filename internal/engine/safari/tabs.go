package safari

import (
	"context"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// TabList returns the open tabs of the live Safari session. It reports the tab
// name (used as the pinned-tab reference) and current URL for each tab so the
// human can choose which tab to pin.
func (e *Engine) TabList(_ context.Context, _ engine.Context) ([]engine.TabInfo, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("safari engine: engine is closed")
	}
	// AppleScript returns the tab name and URL per window; we flatten to a
	// stable, machine-readable list. The live session is single-window for the
	// human, so we read window 1.
	script := `tell application "Safari"
	  set out to ""
	  repeat with t in tabs of window 1
	    set out to out & (name of t) & "\t" & (URL of t) & "\n"
	  end repeat
	  return out
	end tell`
	out, err := e.runner.Run(context.Background(), script)
	if err != nil {
		return nil, err
	}
	var tabs []engine.TabInfo
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		info := engine.TabInfo{Label: parts[0]}
		if len(parts) == 2 {
			info.URL = parts[1]
		}
		tabs = append(tabs, info)
	}
	return tabs, nil
}

// TabNew opens a new named tab in window 1 and navigates it to url. The new tab
// becomes the pinned tab for subsequent operations when PinnedTabName matches.
func (e *Engine) TabNew(_ context.Context, _ engine.Context, label, url string) (engine.Page, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return engine.Page{}, fmt.Errorf("safari engine: engine is closed")
	}
	tell := fmt.Sprintf(`tell application "Safari"
	  set newTab to make new tab with properties {URL:%q} at end of tabs of window 1
	  set name of newTab to %q
	  return name of newTab
	end tell`, url, label)
	out, err := e.runner.Run(context.Background(), tell)
	if err != nil {
		return engine.Page{}, err
	}
	name := strings.Trim(strings.TrimSpace(out), `"`)
	e.mu.Lock()
	e.PinnedTabName = name
	e.mu.Unlock()
	return engine.Page{ID: "safari-live"}, nil
}

// TabClose closes the pinned tab. It refuses to close the human's only tab
// without an explicit target, to avoid destroying the live session state.
func (e *Engine) TabClose(_ context.Context, _ engine.Page) error {
	e.mu.Lock()
	closed := e.closed
	tabRef := e.pinnedTabRef()
	e.mu.Unlock()
	if closed {
		return fmt.Errorf("safari engine: engine is closed")
	}
	script := fmt.Sprintf(`tell application "Safari"
	  close %s
	end tell`, tabRef)
	_, err := e.runner.Run(context.Background(), script)
	return err
}
