package daemon

import (
	"context"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// fakeTabEngine implements the engine interfaces needed by the tab handler.
type fakeTabEngine struct {
	engine.Engine
	tabs     []engine.TabInfo
	lastPage engine.Page
	closed   []engine.Page
}

func (f *fakeTabEngine) TabList(context.Context, engine.Context) ([]engine.TabInfo, error) {
	return f.tabs, nil
}
func (f *fakeTabEngine) TabNew(_ context.Context, _ engine.Context, _, _ string) (engine.Page, error) {
	page := engine.Page{ID: "target-new", SessionID: "session-new"}
	f.lastPage = page
	f.tabs = append(f.tabs, engine.TabInfo{ID: "target-new", URL: "about:blank"})
	return page, nil
}
func (f *fakeTabEngine) TabClose(_ context.Context, page engine.Page) error {
	f.closed = append(f.closed, page)
	return nil
}
func (f *fakeTabEngine) Close() error { return nil }

func newTabRuntime(t *testing.T) (*NavigationRuntime, *fakeTabEngine) {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{UserDataRoot: t.TempDir()})
	if _, err := registry.Ensure("tabs"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	fake := &fakeTabEngine{}
	runtime := &NavigationRuntime{
		registry:        registry,
		executable:      "fake",
		engines:         map[string]engine.Engine{"tabs": fake},
		browserContexts: map[string]engine.Context{"tabs": {ID: "ctx"}},
		tabs:            map[string][]*sessionTab{"tabs": {}},
		activeTab:       map[string]int{"tabs": 0},
		recorders:       map[string]*recorderState{},
	}
	// Seed the first tab with a stub service.
	runtime.tabs["tabs"] = []*sessionTab{{Label: "t1", Service: nil, Page: engine.Page{ID: "target-1", SessionID: "session-1"}}}
	return runtime, fake
}

func TestTabNewActivatesNewTab(t *testing.T) {
	runtime, fake := newTabRuntime(t)
	result, err := runtime.tabNew(context.Background(), "tabs", "second", "https://example.com")
	if err != nil {
		t.Fatalf("tabNew: %v", err)
	}
	payload := result.(map[string]any)
	if payload["label"] != "second" {
		t.Errorf("label = %v, want second", payload["label"])
	}
	if fake.lastPage.ID != "target-new" {
		t.Errorf("engine created page %v, want target-new", fake.lastPage)
	}
	runtime.mu.Lock()
	active := runtime.activeTab["tabs"]
	count := len(runtime.tabs["tabs"])
	runtime.mu.Unlock()
	if active != 1 || count != 2 {
		t.Errorf("active = %d, tabs = %d; want 1 and 2", active, count)
	}
}

func TestTabSwitchByIndexAndLabel(t *testing.T) {
	runtime, _ := newTabRuntime(t)
	runtime.tabs["tabs"] = append(runtime.tabs["tabs"], &sessionTab{Label: "second", Service: nil, Page: engine.Page{ID: "target-2", SessionID: "session-2"}})

	if _, err := runtime.tabSwitch("tabs", "t2"); err != nil {
		t.Fatalf("tabSwitch t2: %v", err)
	}
	runtime.mu.Lock()
	active := runtime.activeTab["tabs"]
	runtime.mu.Unlock()
	if active != 1 {
		t.Errorf("active after t2 = %d, want 1", active)
	}

	if _, err := runtime.tabSwitch("tabs", "t1"); err != nil {
		t.Fatalf("tabSwitch t1: %v", err)
	}
	if _, err := runtime.tabSwitch("tabs", "second"); err != nil {
		t.Fatalf("tabSwitch by label: %v", err)
	}
	runtime.mu.Lock()
	active = runtime.activeTab["tabs"]
	runtime.mu.Unlock()
	if active != 1 {
		t.Errorf("active after label switch = %d, want 1", active)
	}
}

func TestTabSwitchUnknownTarget(t *testing.T) {
	runtime, _ := newTabRuntime(t)
	if _, err := runtime.tabSwitch("tabs", "nope"); err == nil {
		t.Fatal("tabSwitch accepted an unknown tab")
	}
}

func TestTabCloseLastTabRejected(t *testing.T) {
	runtime, _ := newTabRuntime(t)
	if _, err := runtime.tabClose(context.Background(), "tabs", ""); err == nil {
		t.Fatal("tabClose accepted closing the last tab")
	}
}

func TestTabCloseRemovesAndKeepsActiveValid(t *testing.T) {
	runtime, fake := newTabRuntime(t)
	runtime.tabs["tabs"] = append(runtime.tabs["tabs"],
		&sessionTab{Label: "second", Service: nil, Page: engine.Page{ID: "target-2", SessionID: "session-2"}},
		&sessionTab{Label: "third", Service: nil, Page: engine.Page{ID: "target-3", SessionID: "session-3"}},
	)
	runtime.activeTab["tabs"] = 2
	result, err := runtime.tabClose(context.Background(), "tabs", "t2")
	if err != nil {
		t.Fatalf("tabClose: %v", err)
	}
	payload := result.(map[string]any)
	if payload["closed"] != "t2" {
		t.Errorf("closed = %v, want t2", payload["closed"])
	}
	runtime.mu.Lock()
	count := len(runtime.tabs["tabs"])
	active := runtime.activeTab["tabs"]
	runtime.mu.Unlock()
	if count != 2 {
		t.Errorf("tabs after close = %d, want 2", count)
	}
	if active != 1 {
		t.Errorf("active after close = %d, want 1 (was closing the active tab)", active)
	}
	if len(fake.closed) != 1 || fake.closed[0].ID != "target-2" {
		t.Errorf("engine closed %+v, want target-2", fake.closed)
	}
}

func TestDefaultLabel(t *testing.T) {
	if got := defaultLabel("", 2); got != "t3" {
		t.Errorf("defaultLabel(_,2) = %q, want t3", got)
	}
	if got := defaultLabel("work", 0); got != "work" {
		t.Errorf("defaultLabel(work,0) = %q, want work", got)
	}
}
