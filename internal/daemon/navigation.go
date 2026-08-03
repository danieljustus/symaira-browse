package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/chrome"
)

// NavigationRuntime lazily owns one protocol-neutral navigation service and
// Chrome engine per session. CDP details remain confined to engine/chrome.
type NavigationRuntime struct {
	mu         sync.Mutex
	registry   *SessionRegistry
	executable string
	services   map[string]*engine.NavigationService
	engines    map[string]engine.Engine
}

// NewNavigationRuntime creates a runtime. Chrome is not started until the
// first navigation or wait operation for a session.
func NewNavigationRuntime(registry *SessionRegistry, executable string) *NavigationRuntime {
	if executable == "" {
		executable = os.Getenv("SYMBROWSE_EXECUTABLE_PATH")
	}
	return &NavigationRuntime{registry: registry, executable: executable, services: make(map[string]*engine.NavigationService), engines: make(map[string]engine.Engine)}
}

// Handle executes one navigation frame and returns JSON-serializable data.
func (r *NavigationRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, nil, err
	}
	switch frame.Cmd {
	case "open", "goto":
		var request struct {
			URL string `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if frame.Cmd == "open" {
			outcome, err := service.Open(ctx, request.URL)
			return outcome, nil, err
		}
		outcome, err := service.Goto(ctx, request.URL)
		return outcome, nil, err
	case "back":
		outcome, err := service.Back(ctx)
		return outcome, nil, err
	case "forward":
		outcome, err := service.Forward(ctx)
		return outcome, nil, err
	case "reload":
		outcome, err := service.Reload(ctx)
		return outcome, nil, err
	case "wait":
		var condition engine.WaitCondition
		if err := decodeArgs(frame, &condition); err != nil {
			return nil, nil, err
		}
		result, err := service.Wait(ctx, condition)
		return result, nil, err
	case "snapshot":
		var options engine.SnapshotOptions
		if err := decodeArgs(frame, &options); err != nil {
			return nil, nil, err
		}
		if options.Diff || options.Since != "" {
			result, err := service.SnapshotDiff(ctx, options)
			return result, nil, err
		}
		result, err := service.Snapshot(ctx, options)
		return result, nil, err
	case string(engine.ActionClick), string(engine.ActionDoubleClick), string(engine.ActionFill), string(engine.ActionType), string(engine.ActionPress), string(engine.ActionHover), string(engine.ActionFocus), string(engine.ActionSelect), string(engine.ActionCheck), string(engine.ActionUncheck), string(engine.ActionScroll), string(engine.ActionScrollIntoView):
		var request engine.InteractionRequest
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if request.Action == "" {
			request.Action = engine.InteractionAction(frame.Cmd)
		}
		result, err := service.Interact(ctx, request)
		var interactionErr *engine.InteractionError
		if errors.As(err, &interactionErr) {
			return nil, nil, &Error{Code: interactionErr.Code, Message: interactionErr.Message, Hint: interactionErr.Hint}
		}
		return result, nil, err
	case "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked":
		var request engine.InspectionRequest
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if request.Kind == "" {
			request.Kind = engine.InspectionKind(strings.TrimPrefix(frame.Cmd, "get."))
			if strings.HasPrefix(frame.Cmd, "is.") {
				request.Kind = engine.InspectionKind(strings.TrimPrefix(frame.Cmd, "is."))
			}
		}
		result, err := service.Inspect(ctx, request)
		var inspectionErr *engine.InspectionError
		if errors.As(err, &inspectionErr) {
			return nil, nil, &Error{Code: inspectionErr.Code, Message: inspectionErr.Message, Hint: inspectionErr.Hint}
		}
		return result, nil, err
	case "find":
		var request engine.FindRequest
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		result, err := service.Find(ctx, request)
		var findErr *engine.FindError
		if errors.As(err, &findErr) {
			return nil, nil, &Error{Code: findErr.Code, Message: findErr.Message, Details: map[string]any{"matches": findErr.Matches}}
		}
		return result, nil, err
	case "cookies.list":
		var request struct {
			URLs []string `json:"urls,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		cookies, err := service.CookiesForURLs(ctx, request.URLs)
		if err != nil {
			return nil, nil, err
		}
		origin, originErr := service.Origin(ctx)
		if originErr != nil {
			origin = ""
		}
		return map[string]any{"origin": origin, "cookies": cookies}, nil, nil
	case "cookies.set":
		var request struct {
			Cookie engine.Cookie `json:"cookie"`
			URL    string        `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if err := service.SetCookie(ctx, request.Cookie, request.URL); err != nil {
			return nil, nil, err
		}
		return map[string]any{"set": request.Cookie.Name}, nil, nil
	case "cookies.clear":
		var request struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if err := service.DeleteCookie(ctx, request.Name, request.URL); err != nil {
			return nil, nil, err
		}
		return map[string]any{"cleared": request.Name}, nil, nil
	case "storage.list":
		var request struct {
			Kind engine.StorageKind `json:"kind"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		items, err := service.Storage().StorageItems(ctx, request.Kind)
		if err != nil {
			return nil, nil, err
		}
		origin, originErr := service.Origin(ctx)
		if originErr != nil {
			origin = ""
		}
		return map[string]any{"origin": origin, "kind": request.Kind, "items": items}, nil, nil
	case "storage.set":
		var request struct {
			Kind  engine.StorageKind `json:"kind"`
			Key   string             `json:"key"`
			Value string             `json:"value"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if err := service.Storage().SetStorageItem(ctx, request.Kind, request.Key, request.Value); err != nil {
			return nil, nil, err
		}
		return map[string]any{"set": request.Key}, nil, nil
	case "storage.clear":
		var request struct {
			Kind engine.StorageKind `json:"kind"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if err := service.Storage().ClearStorage(ctx, request.Kind); err != nil {
			return nil, nil, err
		}
		return map[string]any{"cleared": request.Kind}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown navigation command %q", frame.Cmd)
	}
}

// decodeOptionalArgs decodes args when present, leaving target zero-valued for
// commands with fully optional payloads.
func decodeOptionalArgs(frame Frame, target any) error {
	if len(frame.Args) == 0 || string(frame.Args) == "null" {
		return nil
	}
	if err := json.Unmarshal(frame.Args, target); err != nil {
		return fmt.Errorf("decode %s arguments: %w", frame.Cmd, err)
	}
	return nil
}

func (r *NavigationRuntime) service(ctx context.Context, session string) (*engine.NavigationService, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if service := r.services[session]; service != nil {
		return service, nil
	}
	if r.registry == nil {
		return nil, errors.New("session registry is required")
	}
	info, err := r.registry.Get(session)
	if err != nil {
		return nil, err
	}
	if r.executable == "" {
		return nil, errors.New("browser executable is not configured; set SYMBROWSE_EXECUTABLE_PATH")
	}
	browser := chrome.New(chrome.Options{ExecutablePath: r.executable, UserDataDir: info.UserDataDir})
	if err := browser.Launch(ctx); err != nil {
		return nil, err
	}
	browserContext, err := browser.NewContext(ctx)
	if err != nil {
		_ = browser.Close()
		return nil, err
	}
	page, err := browser.NewPage(ctx, browserContext, "about:blank")
	if err != nil {
		_ = browser.Close()
		return nil, err
	}
	service := engine.NewNavigationService(browser, page, engine.NavigationOptions{})
	r.engines[session] = browser
	r.services[session] = service
	_ = r.registry.SetActiveTabs(session, 1)
	return service, nil
}

// Close releases all per-session browser engines.
func (r *NavigationRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for session, browser := range r.engines {
		if err := browser.Close(); err != nil && first == nil {
			first = fmt.Errorf("close session %q: %w", session, err)
		}
	}
	r.engines = make(map[string]engine.Engine)
	r.services = make(map[string]*engine.NavigationService)
	return first
}

func decodeArgs(frame Frame, target any) error {
	if len(frame.Args) == 0 || string(frame.Args) == "null" {
		return errors.New("command arguments are required")
	}
	if err := json.Unmarshal(frame.Args, target); err != nil {
		return fmt.Errorf("decode %s arguments: %w", frame.Cmd, err)
	}
	return nil
}
