package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// fakeCookieEngine implements the protocol-neutral engine plus the cookie
// extension used by the navigation runtime tests.
type fakeCookieEngine struct {
	cookies  []engine.Cookie
	setCalls []engine.Cookie
}

func (f *fakeCookieEngine) Launch(context.Context) error { return nil }
func (f *fakeCookieEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "ctx"}, nil
}
func (f *fakeCookieEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "page"}, nil
}
func (f *fakeCookieEngine) Navigate(context.Context, engine.Page, string) (engine.NavigationResult, error) {
	return engine.NavigationResult{}, nil
}
func (f *fakeCookieEngine) Evaluate(context.Context, engine.Page, string) (engine.EvaluationResult, error) {
	return engine.EvaluationResult{Value: json.RawMessage(`"https://example.com"`)}, nil
}
func (f *fakeCookieEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) {
	return nil, nil
}
func (f *fakeCookieEngine) Screenshot(context.Context, engine.Page) ([]byte, error) { return nil, nil }
func (f *fakeCookieEngine) Close() error                                            { return nil }

func (f *fakeCookieEngine) Cookies(context.Context, engine.Page, []string) ([]engine.Cookie, error) {
	return f.cookies, nil
}
func (f *fakeCookieEngine) SetCookie(_ context.Context, _ engine.Page, cookie engine.Cookie, _ string) error {
	f.setCalls = append(f.setCalls, cookie)
	return nil
}
func (f *fakeCookieEngine) DeleteCookies(context.Context, engine.Page, string, string) error {
	return nil
}

func (f *fakeCookieEngine) Inspect(_ context.Context, _ engine.Page, request engine.InspectionRequest, _ *engine.InteractionTarget) (engine.InspectionResult, error) {
	if request.Kind == engine.InspectHTML {
		return engine.InspectionResult{Kind: engine.InspectHTML, Value: json.RawMessage(`""`)}, nil
	}
	return engine.InspectionResult{Kind: request.Kind}, nil
}

// newCookieRuntime wires a NavigationRuntime around a fake cookie engine so
// cookie and storage frames can be exercised without Chrome.
func newCookieRuntime(t *testing.T) (*NavigationRuntime, *fakeCookieEngine) {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCookieEngine{}
	runtime := &NavigationRuntime{
		registry:   registry,
		executable: "/fake/chrome",
		tabs:       make(map[string][]*sessionTab),
		activeTab:  make(map[string]int),
		engines:    make(map[string]engine.Engine),
	}
	// Pre-seed the service so no real Chrome launch happens.
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	runtime.tabs["default"] = []*sessionTab{{Label: "t1", Service: service, Page: engine.Page{ID: "page"}}}
	runtime.activeTab["default"] = 0
	runtime.engines["default"] = fake
	return runtime, fake
}

func TestCookiesListFrameReturnsOriginAndCookies(t *testing.T) {
	runtime, fake := newCookieRuntime(t)
	fake.cookies = []engine.Cookie{{Name: "session", Value: "s3cret", Domain: "example.com"}}
	raw, _ := json.Marshal(map[string]any{})
	data, warnings, err := runtime.Handle(context.Background(), Frame{Cmd: "cookies.list", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	payload, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("data = %T", data)
	}
	if payload["origin"] != "https://example.com" {
		t.Fatalf("origin = %#v", payload["origin"])
	}
}

func TestCookiesSetFrameDelegatesToEngine(t *testing.T) {
	runtime, fake := newCookieRuntime(t)
	request := map[string]any{"cookie": engine.Cookie{Name: "theme", Value: "dark", Domain: "example.com"}, "url": "https://example.com/"}
	raw, _ := json.Marshal(request)
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "cookies.set", Session: "default", Args: raw}); err != nil {
		t.Fatal(err)
	}
	if len(fake.setCalls) != 1 || fake.setCalls[0].Name != "theme" || fake.setCalls[0].Value != "dark" {
		t.Fatalf("setCalls = %#v", fake.setCalls)
	}
}

func TestCookiesSetRequiresName(t *testing.T) {
	runtime, _ := newCookieRuntime(t)
	request := map[string]any{"cookie": engine.Cookie{Value: "x"}, "url": ""}
	raw, _ := json.Marshal(request)
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "cookies.set", Session: "default", Args: raw}); err == nil {
		t.Fatal("cookie without name accepted")
	}
}

func TestStorageFrameRejectsInvalidKind(t *testing.T) {
	runtime, _ := newCookieRuntime(t)
	request := map[string]any{"kind": "cookie"}
	raw, _ := json.Marshal(request)
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "storage.list", Session: "default", Args: raw}); err == nil {
		t.Fatal("invalid storage kind accepted")
	}
}
