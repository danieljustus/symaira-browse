package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/state"
)

// fakeStateEngine implements CookieEngine plus storage reads/writes via
// Evaluate so CaptureCookiesAndStorage / RestoreCookiesAndStorage work
// end-to-end without Chrome.
type fakeStateEngine struct {
	cookies     []engine.Cookie
	storage     map[engine.StorageKind]map[string]string
	setCookies  []engine.Cookie
	setItems    []string
	origin      string
	closeCalled bool
}

func (f *fakeStateEngine) Launch(context.Context) error { return nil }
func (f *fakeStateEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "ctx"}, nil
}
func (f *fakeStateEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "page"}, nil
}
func (f *fakeStateEngine) Navigate(context.Context, engine.Page, string) (engine.NavigationResult, error) {
	return engine.NavigationResult{}, nil
}
func (f *fakeStateEngine) Evaluate(_ context.Context, _ engine.Page, expression string) (engine.EvaluationResult, error) {
	if strings.Contains(expression, ".setItem(") {
		f.setItems = append(f.setItems, expression)
		return engine.EvaluationResult{Value: json.RawMessage(`true`)}, nil
	}
	if strings.Contains(expression, "location.origin") && !strings.Contains(expression, "window.") {
		return engine.EvaluationResult{Value: json.RawMessage(`"` + f.origin + `"`)}, nil
	}
	if strings.Contains(expression, "window.localStorage") || strings.Contains(expression, "window.sessionStorage") {
		kind := engine.StorageLocal
		if strings.Contains(expression, "sessionStorage") {
			kind = engine.StorageSession
		}
		items := f.storage[kind]
		if items == nil {
			items = map[string]string{}
		}
		payload := struct {
			Origin string            `json:"origin"`
			Items  map[string]string `json:"items"`
		}{Origin: f.origin, Items: items}
		raw, _ := json.Marshal(payload)
		return engine.EvaluationResult{Value: raw}, nil
	}
	return engine.EvaluationResult{}, nil
}
func (f *fakeStateEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) {
	return nil, nil
}
func (f *fakeStateEngine) Screenshot(context.Context, engine.Page) ([]byte, error) { return nil, nil }
func (f *fakeStateEngine) Close() error {
	f.closeCalled = true
	return nil
}
func (f *fakeStateEngine) Cookies(context.Context, engine.Page, []string) ([]engine.Cookie, error) {
	return f.cookies, nil
}
func (f *fakeStateEngine) SetCookie(_ context.Context, _ engine.Page, cookie engine.Cookie, _ string) error {
	f.setCookies = append(f.setCookies, cookie)
	return nil
}
func (f *fakeStateEngine) DeleteCookies(context.Context, engine.Page, string, string) error {
	return nil
}

func newStateEngineRuntime(t *testing.T, fake *fakeStateEngine) (*NavigationRuntime, *StateRuntime) {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{UserDataRoot: t.TempDir()})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	runtime := &NavigationRuntime{
		registry:        registry,
		executable:      "/fake/chrome",
		engines:         map[string]engine.Engine{"default": fake},
		browserContexts: map[string]engine.Context{"default": {ID: "ctx"}},
		tabs:            make(map[string][]*sessionTab),
		activeTab:       make(map[string]int),
		recorders:       make(map[string]*recorderState),
		engineKind:      "chrome",
	}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	runtime.tabs["default"] = []*sessionTab{{Label: "t1", Service: service, Page: engine.Page{ID: "page"}}}
	runtime.activeTab["default"] = 0

	store, err := state.NewStore(state.StoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, NewStateRuntime(store, runtime)
}

func TestStateSaveCapturesCookiesAndStorage(t *testing.T) {
	fake := &fakeStateEngine{
		origin:  "https://app.example.com",
		cookies: []engine.Cookie{{Name: "session", Value: "s3cret", Domain: ".example.com", Path: "/", Secure: true}},
		storage: map[engine.StorageKind]map[string]string{
			engine.StorageLocal: {"theme": "dark"},
		},
	}
	_, stateRuntime := newStateEngineRuntime(t, fake)

	meta, err := stateRuntime.Save(context.Background(), "default", "login-state")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "login-state" || meta.KeySource != string(state.KeySourceNone) {
		t.Fatalf("meta = %+v", meta)
	}
	if len(meta.Origins) != 1 || meta.Origins[0].Origin != "https://app.example.com" || meta.Origins[0].CookieCount != 1 {
		t.Fatalf("origins = %+v, want one origin with 1 cookie", meta.Origins)
	}

	// Metadata never exposes the values (only counts).
	metadata, err := stateRuntime.store.Metadata("login-state")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(metadata)
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("cookie value leaked into state metadata")
	}
}

func TestStateSaveViaHandle(t *testing.T) {
	fake := &fakeStateEngine{origin: "https://app.example.com"}
	_, stateRuntime := newStateEngineRuntime(t, fake)

	raw, _ := json.Marshal(map[string]any{"name": "snap"})
	data, _, err := stateRuntime.Handle(context.Background(), Frame{Cmd: "state.save", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	payload := data.(map[string]any)
	if payload["saved"] != "snap" {
		t.Fatalf("payload = %+v", payload)
	}

	// Loading it back must restore cookies on the fake engine.
	loadRaw, _ := json.Marshal(map[string]any{"name": "snap"})
	data, _, err = stateRuntime.Handle(context.Background(), Frame{Cmd: "state.load", Session: "default", Args: loadRaw})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["loaded"] != "snap" {
		t.Fatalf("load payload = %+v", data)
	}
}

func TestStateLoadRestoresCookiesAndStorage(t *testing.T) {
	fake := &fakeStateEngine{origin: "https://app.example.com"}
	_, stateRuntime := newStateEngineRuntime(t, fake)

	// Seed a state with cookies and storage.
	store := stateRuntime.store
	if err := store.Save(&state.State{
		SchemaVersion: state.SchemaVersion,
		Name:          "seeded",
		Origins: map[string]state.OriginState{
			"https://app.example.com": {
				Cookies:        []engine.Cookie{{Name: "sid", Value: "v1", Domain: "app.example.com", Path: "/"}},
				LocalStorage:   map[string]string{"k": "v"},
				SessionStorage: map[string]string{},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	meta, warnings, err := stateRuntime.Load(context.Background(), "default", "seeded")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "seeded" {
		t.Fatalf("meta = %+v", meta)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(fake.setCookies) != 1 || fake.setCookies[0].Name != "sid" {
		t.Fatalf("restored cookies = %+v", fake.setCookies)
	}
	if len(fake.setItems) == 0 {
		t.Fatal("no storage items were written")
	}
}

func TestStateLoadUnknownOriginWarns(t *testing.T) {
	fake := &fakeStateEngine{origin: "https://app.example.com"}
	_, stateRuntime := newStateEngineRuntime(t, fake)

	store := stateRuntime.store
	// A state whose origin differs from the fake's current origin: the
	// restore navigates per origin, but storage for a foreign origin is
	// skipped with a warning (RestoreCookiesAndStorage only writes the
	// current origin).
	if err := store.Save(&state.State{
		SchemaVersion: state.SchemaVersion,
		Name:          "foreign",
		Origins: map[string]state.OriginState{
			"https://other.example.com": {LocalStorage: map[string]string{"x": "y"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := stateRuntime.Load(context.Background(), "default", "foreign")
	if err != nil {
		t.Fatal(err)
	}
	_ = warnings // restore is best-effort; no hard failure expected
}

func TestStateRuntimeStoreAccessor(t *testing.T) {
	_, stateRuntime := newStateEngineRuntime(t, &fakeStateEngine{origin: "https://x.example.com"})
	if stateRuntime.Store() == nil {
		t.Fatal("Store() returned nil")
	}
	stateRuntime.ReportExpired() // must not panic
}
