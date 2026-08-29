package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// fakeFrameDialogEngine implements FrameManager and DialogController so the
// frame.* and dialog.* daemon handlers can be exercised without Chrome.
type fakeFrameDialogEngine struct {
	engine.Engine
	tree       []engine.FrameInfo
	active     string
	autoMode   string
	dialog     engine.DialogInfo
	acceptText string
}

func (f *fakeFrameDialogEngine) FrameTree(context.Context, engine.Page) ([]engine.FrameInfo, error) {
	return f.tree, nil
}
func (f *fakeFrameDialogEngine) SetActiveFrame(_ context.Context, _ engine.Page, frame string) error {
	if frame == "missing" {
		return errors.New("frame not found")
	}
	f.active = frame
	return nil
}
func (f *fakeFrameDialogEngine) DialogStatus(context.Context, engine.Page) (engine.DialogInfo, error) {
	return f.dialog, nil
}
func (f *fakeFrameDialogEngine) DialogAccept(_ context.Context, _ engine.Page, text string) error {
	if text == "fail" {
		return errors.New("accept failed")
	}
	f.acceptText = text
	return nil
}
func (f *fakeFrameDialogEngine) DialogDismiss(context.Context, engine.Page) error { return nil }
func (f *fakeFrameDialogEngine) SetDialogAutoMode(mode string) error {
	if mode == "bogus" {
		return errors.New("invalid mode")
	}
	f.autoMode = mode
	return nil
}
func (f *fakeFrameDialogEngine) Close() error { return nil }

func newFrameDialogRuntime(t *testing.T) (*NavigationRuntime, *fakeFrameDialogEngine) {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{UserDataRoot: t.TempDir()})
	if _, err := registry.Ensure("fd"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeFrameDialogEngine{}
	runtime := &NavigationRuntime{
		registry:        registry,
		executable:      "fake",
		engines:         map[string]engine.Engine{"fd": fake},
		browserContexts: map[string]engine.Context{"fd": {ID: "ctx"}},
		tabs:            map[string][]*sessionTab{"fd": {}},
		activeTab:       map[string]int{"fd": 0},
		recorders:       map[string]*recorderState{},
	}
	runtime.tabs["fd"] = []*sessionTab{{Label: "t1", Service: nil, Page: engine.Page{ID: "p1", SessionID: "fd"}}}
	return runtime, fake
}

func TestFrameTreeAndSelect(t *testing.T) {
	runtime, fake := newFrameDialogRuntime(t)
	fake.tree = []engine.FrameInfo{{ID: "main", Name: "main", URL: "https://example.com"}}

	raw, _ := json.Marshal(map[string]any{})
	data, _, err := runtime.Handle(context.Background(), Frame{Cmd: "frame.tree", Session: "fd", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	payload := data.(map[string]any)
	if len(payload["frames"].([]engine.FrameInfo)) != 1 {
		t.Fatalf("frame tree = %+v", payload)
	}

	selectRaw, _ := json.Marshal(map[string]any{"frame": "main"})
	_, _, err = runtime.Handle(context.Background(), Frame{Cmd: "frame.select", Session: "fd", Args: selectRaw})
	if err != nil {
		t.Fatal(err)
	}
	if fake.active != "main" {
		t.Fatalf("active frame = %q", fake.active)
	}

	_, _, err = runtime.Handle(context.Background(), Frame{Cmd: "frame.main", Session: "fd", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	if fake.active != "" {
		t.Fatalf("main frame selection = %q, want empty", fake.active)
	}

	// Unknown frame id -> engine error surfaces.
	badRaw, _ := json.Marshal(map[string]any{"frame": "missing"})
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "frame.select", Session: "fd", Args: badRaw}); err == nil {
		t.Fatal("expected frame-not-found error")
	}
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "frame.bogus", Session: "fd", Args: raw}); err == nil {
		t.Fatal("expected unknown frame command error")
	}
}

func TestDialogHandlers(t *testing.T) {
	runtime, fake := newFrameDialogRuntime(t)
	fake.dialog = engine.DialogInfo{Type: "alert", Message: "hello"}

	raw, _ := json.Marshal(map[string]any{})
	data, _, err := runtime.Handle(context.Background(), Frame{Cmd: "dialog.status", Session: "fd", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	if data.(engine.DialogInfo).Message != "hello" {
		t.Fatalf("dialog = %+v", data)
	}

	acceptRaw, _ := json.Marshal(map[string]any{"text": "ok"})
	_, _, err = runtime.Handle(context.Background(), Frame{Cmd: "dialog.accept", Session: "fd", Args: acceptRaw})
	if err != nil {
		t.Fatal(err)
	}
	if fake.acceptText != "ok" {
		t.Fatalf("accept text = %q", fake.acceptText)
	}

	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "dialog.dismiss", Session: "fd", Args: raw}); err != nil {
		t.Fatal(err)
	}

	autoRaw, _ := json.Marshal(map[string]any{"mode": "accept"})
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "dialog.auto", Session: "fd", Args: autoRaw}); err != nil {
		t.Fatal(err)
	}
	if fake.autoMode != "accept" {
		t.Fatalf("auto mode = %q", fake.autoMode)
	}

	badAuto, _ := json.Marshal(map[string]any{"mode": "bogus"})
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "dialog.auto", Session: "fd", Args: badAuto}); err == nil {
		t.Fatal("expected invalid auto-mode error")
	}
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "dialog.bogus", Session: "fd", Args: raw}); err == nil {
		t.Fatal("expected unknown dialog command error")
	}
}

func TestTabFrameRoutingViaHandle(t *testing.T) {
	runtime, _ := newTabRuntime(t)

	raw, _ := json.Marshal(map[string]any{})
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "tab.list", Session: "tabs", Args: raw}); err != nil {
		t.Fatal(err)
	}

	newRaw, _ := json.Marshal(map[string]any{"label": "x", "url": "https://example.com"})
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "tab.new", Session: "tabs", Args: newRaw}); err != nil {
		t.Fatal(err)
	}

	badArgs := json.RawMessage(`{not json`)
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "tab.new", Session: "tabs", Args: badArgs}); err == nil {
		t.Fatal("expected decode error for malformed args")
	}
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "tab.bogus", Session: "tabs", Args: raw}); err == nil {
		t.Fatal("expected unknown tab command error")
	}
}

func TestWindowNewCreatesTab(t *testing.T) {
	runtime, fake := newTabRuntime(t)
	raw, _ := json.Marshal(map[string]any{})
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "window.new", Session: "tabs", Args: raw}); err != nil {
		t.Fatal(err)
	}
	if fake.lastPage.ID != "target-new" {
		t.Fatalf("window.new created %+v", fake.lastPage)
	}
}

func TestTabCloseByLabel(t *testing.T) {
	runtime, fake := newTabRuntime(t)
	runtime.tabs["tabs"] = append(runtime.tabs["tabs"], &sessionTab{Label: "second", Service: nil, Page: engine.Page{ID: "target-2", SessionID: "session-2"}})
	result, err := runtime.tabClose(context.Background(), "tabs", "second")
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["closed"] != "t2" {
		t.Fatalf("closed = %+v", result)
	}
	if len(fake.closed) != 1 || fake.closed[0].ID != "target-2" {
		t.Fatalf("closed pages = %+v", fake.closed)
	}
}

func TestTabHandlersWithoutTabs(t *testing.T) {
	runtime, _ := newTabRuntime(t)
	runtime.tabs["tabs"] = nil

	if _, err := runtime.tabSwitch("tabs", "t1"); err == nil {
		t.Fatal("expected no-tabs error on switch")
	}
	if _, err := runtime.tabClose(context.Background(), "tabs", ""); err == nil {
		t.Fatal("expected no-tabs error on close")
	}
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "frame.tree", Session: "tabs", Args: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("expected frame error without browser")
	}
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "dialog.status", Session: "tabs", Args: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("expected dialog error without browser")
	}
}

func TestNewEngineSelectsStaticOrChrome(t *testing.T) {
	runtime, _ := newCookieRuntime(t)
	runtime.engineKind = "static"
	engineInstance := runtime.newEngine("/tmp/unused", runtime.urlGuard)
	if _, ok := engineInstance.(interface{ Close() error }); !ok {
		t.Fatalf("static engine = %T", engineInstance)
	}
	runtime.engineKind = "chrome"
	engineInstance = runtime.newEngine("/tmp/unused", runtime.urlGuard)
	if engineInstance == nil {
		t.Fatal("chrome engine must not be nil")
	}
}

func TestServiceExecutableRequired(t *testing.T) {
	// Deterministic: stub discovery to find nothing, so the test does not
	// depend on whether a Chrome install exists on the host (with a Chrome
	// present, discovery would find it and the runtime would attempt a real
	// launch instead of erroring).
	orig := resolveBrowserExecutable
	resolveBrowserExecutable = stubResolver("", errors.New("no browser"))
	t.Cleanup(func() { resolveBrowserExecutable = orig })
	registry := NewSessionRegistry(SessionRegistryOptions{UserDataRoot: t.TempDir()})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	runtime := &NavigationRuntime{
		registry:     registry,
		executable:   "",
		tabs:         make(map[string][]*sessionTab),
		activeTab:    make(map[string]int),
		engines:      make(map[string]engine.Engine),
		recorders:    make(map[string]*recorderState),
		engineKind:   "chrome",
		uploadDirs:   []string{},
		autosave:     &AutosaveConfig{},
		stateStore:   nil,
		lastAutosave: make(map[string]time.Time),
	}
	if _, err := runtime.service(context.Background(), "default"); err == nil {
		t.Fatal("expected executable-required error")
	}
}

func TestDecodeArgsHelpers(t *testing.T) {
	var target struct {
		A string `json:"a"`
	}
	if err := decodeArgs(Frame{Args: json.RawMessage(`{"a":"b"}`)}, &target); err != nil {
		t.Fatal(err)
	}
	if target.A != "b" {
		t.Fatalf("target = %+v", target)
	}
	if err := decodeArgs(Frame{Args: json.RawMessage(`bad`)}, &target); err == nil {
		t.Fatal("expected decode error")
	}
	if err := decodeOptionalArgs(Frame{Args: json.RawMessage(`null`)}, &target); err != nil {
		t.Fatal(err)
	}
	if err := decodeOptionalArgs(Frame{Args: nil}, &target); err != nil {
		t.Fatal(err)
	}
}
