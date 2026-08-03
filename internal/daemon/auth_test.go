package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestParseVaultOutputJSON(t *testing.T) {
	creds, err := parseVaultOutput(`{"username":"ada","password":"p@ssw0rd"}`)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Username != "ada" || creds.Password != "p@ssw0rd" {
		t.Fatalf("creds = %#v", creds)
	}
}

func TestParseVaultOutputLines(t *testing.T) {
	creds, err := parseVaultOutput("username: ada\npassword = p@ssw0rd\n")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Username != "ada" || creds.Password != "p@ssw0rd" {
		t.Fatalf("creds = %#v", creds)
	}
}

func TestParseVaultOutputRejectsIncomplete(t *testing.T) {
	for _, raw := range []string{"", "hello world", `{"username":"ada"}`, "username: ada"} {
		if _, err := parseVaultOutput(raw); err == nil {
			t.Fatalf("incomplete entry accepted: %q", raw)
		}
	}
}

func TestVaultResolverMissingBinary(t *testing.T) {
	resolver := &VaultResolver{
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Run:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
	if _, err := resolver.Resolve(context.Background(), "x"); !errors.Is(err, ErrVaultUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestVaultResolverDelegates(t *testing.T) {
	var called []string
	resolver := &VaultResolver{
		LookPath: func(string) (string, error) { return "/bin/symvault", nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			called = append(called, name)
			called = append(called, args...)
			return []byte(`{"username":"ada","password":"s3cret"}`), nil
		},
	}
	creds, err := resolver.Resolve(context.Background(), "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Password != "s3cret" {
		t.Fatalf("creds = %#v", creds)
	}
	if len(called) != 3 || called[0] != "symvault" || called[1] != "get" || called[2] != "myapp" {
		t.Fatalf("called = %#v", called)
	}
}

func TestRedactSecrets(t *testing.T) {
	redacted := redactSecrets("failed to fill field with p@ssw0rd for ada", "p@ssw0rd", "ada")
	if strings.Contains(redacted, "p@ssw0rd") || strings.Contains(redacted, "ada") {
		t.Fatalf("secret leaked: %q", redacted)
	}
}

// fakeAuthEngine records interaction calls and reports a login form.
type fakeAuthEngine struct {
	fillCalls []string
}

func (f *fakeAuthEngine) Launch(context.Context) error { return nil }
func (f *fakeAuthEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "ctx"}, nil
}
func (f *fakeAuthEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "page"}, nil
}
func (f *fakeAuthEngine) Navigate(context.Context, engine.Page, string) (engine.NavigationResult, error) {
	return engine.NavigationResult{}, nil
}
func (f *fakeAuthEngine) Evaluate(_ context.Context, _ engine.Page, expression string) (engine.EvaluationResult, error) {
	if strings.Contains(expression, "input[type=\"password\"]") {
		return engine.EvaluationResult{Value: json.RawMessage(`{"found":true,"username_selector":"#user","password_selector":"#pass"}`)}, nil
	}
	if strings.Contains(expression, "location.origin") {
		return engine.EvaluationResult{Value: json.RawMessage(`"https://app.example.com"`)}, nil
	}
	return engine.EvaluationResult{}, errors.New("unexpected expression")
}
func (f *fakeAuthEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) {
	return nil, nil
}
func (f *fakeAuthEngine) Screenshot(context.Context, engine.Page) ([]byte, error) { return nil, nil }
func (f *fakeAuthEngine) Close() error                                            { return nil }

func (f *fakeAuthEngine) ResolveElement(context.Context, engine.Page, string) (engine.InteractionTarget, error) {
	return engine.InteractionTarget{NodeID: "node-1"}, nil
}
func (f *fakeAuthEngine) ScrollIntoView(context.Context, engine.Page, engine.InteractionTarget) error {
	return nil
}
func (f *fakeAuthEngine) PerformInteraction(_ context.Context, _ engine.Page, _ engine.InteractionTarget, request engine.InteractionRequest) error {
	f.fillCalls = append(f.fillCalls, request.Selector+"="+request.Value)
	return nil
}

// TestAuthLoginEndToEnd verifies the full login flow: vault resolution,
// form detection, field filling, and that the password never appears in the
// result or any error channel.
func TestAuthLoginEndToEnd(t *testing.T) {
	fake := &fakeAuthEngine{}
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	runtime := &NavigationRuntime{
		registry:   registry,
		executable: "/fake/chrome",
		tabs:       make(map[string][]*sessionTab),
		activeTab:  make(map[string]int),
		engines:    make(map[string]engine.Engine),
	}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	runtime.tabs["default"] = []*sessionTab{{Label: "t1", Service: service, Page: engine.Page{ID: "page"}}}
	runtime.activeTab["default"] = 0
	runtime.engines["default"] = fake

	vault := &VaultResolver{
		LookPath: func(string) (string, error) { return "/bin/symvault", nil },
		Run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(`{"username":"ada","password":"hunter2-secret"}`), nil
		},
	}
	auth := NewAuthRuntime(runtime, vault)
	result, err := auth.Login(context.Background(), "default", "myapp", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "logged_in" || !result.UsernameSet || !result.PasswordSet {
		t.Fatalf("result = %#v", result)
	}
	if len(fake.fillCalls) != 2 || fake.fillCalls[0] != "#user=ada" || fake.fillCalls[1] != "#pass=hunter2-secret" {
		t.Fatalf("fillCalls = %#v", fake.fillCalls)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "hunter2-secret") {
		t.Fatal("password leaked into login result")
	}
}

// TestAuthLoginRedactsErrors ensures interaction failures never carry the
// secret back to the caller.
func TestAuthLoginRedactsErrors(t *testing.T) {
	fake := &fakeAuthEngine{}
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1})
	_, _ = registry.Ensure("default")
	runtime := &NavigationRuntime{
		registry:   registry,
		executable: "/fake/chrome",
		tabs:       make(map[string][]*sessionTab),
		activeTab:  make(map[string]int),
		engines:    make(map[string]engine.Engine),
	}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	runtime.tabs["default"] = []*sessionTab{{Label: "t1", Service: service, Page: engine.Page{ID: "page"}}}
	runtime.activeTab["default"] = 0
	runtime.engines["default"] = fake

	failing := &failingFillEngine{fakeAuthEngine: fake}
	runtime.tabs["default"] = []*sessionTab{{Label: "t1", Service: engine.NewNavigationService(failing, engine.Page{ID: "page"}, engine.NavigationOptions{})}}
	runtime.activeTab["default"] = 0
	runtime.engines["default"] = failing

	vault := &VaultResolver{
		LookPath: func(string) (string, error) { return "/bin/symvault", nil },
		Run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(`{"username":"ada","password":"hunter2-secret"}`), nil
		},
	}
	auth := NewAuthRuntime(runtime, vault)
	_, err := auth.Login(context.Background(), "default", "myapp", "")
	if err == nil {
		t.Fatal("expected login failure")
	}
	if strings.Contains(err.Error(), "hunter2-secret") || strings.Contains(err.Error(), "ada") {
		t.Fatalf("secret leaked into error: %q", err.Error())
	}
}

// failingFillEngine fails the password fill with a message that embeds the
// value, simulating a CDP error string that includes the typed text.
type failingFillEngine struct {
	*fakeAuthEngine
}

func (f *failingFillEngine) PerformInteraction(_ context.Context, _ engine.Page, _ engine.InteractionTarget, request engine.InteractionRequest) error {
	if strings.Contains(request.Selector, "pass") {
		return errors.New("failed to type " + request.Value + " into field")
	}
	return f.fakeAuthEngine.PerformInteraction(context.Background(), engine.Page{}, engine.InteractionTarget{}, request)
}
