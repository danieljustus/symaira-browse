package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/chrome"
	"github.com/danieljustus/symaira-browse/internal/profiles"
	"github.com/danieljustus/symaira-browse/internal/state"
)

// NavigationRuntime lazily owns one protocol-neutral navigation service and
// Chrome engine per session. CDP details remain confined to engine/chrome.
type NavigationRuntime struct {
	mu             sync.Mutex
	registry       *SessionRegistry
	executable     string
	profile        string
	services       map[string]*engine.NavigationService
	engines        map[string]engine.Engine
	autosave       *AutosaveConfig
	stateStore     *state.Store
	lastAutosave   map[string]time.Time
	restoreOnStart map[string]string // session -> state name to restore
}

// NavigationRuntimeOptions configures the browser engines created per session.
type NavigationRuntimeOptions struct {
	// Autosave enables automatic state persistence (issue B-36).
	Autosave *AutosaveConfig
	// StateStore is the store used by autosave and restore-on-start.
	StateStore *state.Store
	// RestoreOnStart maps a session name to the state to restore when the
	// session's browser is first launched.
	RestoreOnStart map[string]string
	// Profile is an existing Chrome profile directory to reuse instead of a
	// private session profile (issue B-38). The daemon emits a warning when
	// set, because a running Chrome locks the profile and the domain
	// allowlist cannot be enforced for a human-owned profile.
	Profile string
}

// NewNavigationRuntime creates a runtime. Chrome is not started until the
// first navigation or wait operation for a session.
func NewNavigationRuntime(registry *SessionRegistry, executable string, options NavigationRuntimeOptions) *NavigationRuntime {
	if executable == "" {
		executable = os.Getenv("SYMBROWSE_EXECUTABLE_PATH")
	}
	return &NavigationRuntime{
		registry:       registry,
		executable:     executable,
		profile:        options.Profile,
		services:       make(map[string]*engine.NavigationService),
		engines:        make(map[string]engine.Engine),
		autosave:       options.Autosave,
		stateStore:     options.StateStore,
		lastAutosave:   make(map[string]time.Time),
		restoreOnStart: options.RestoreOnStart,
	}
}

// SetAutosave updates the autosave configuration at runtime (used by
// `symbrowse daemon --restore` wiring and tests).
func (r *NavigationRuntime) SetAutosave(config *AutosaveConfig, store *state.Store) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autosave = config
	r.stateStore = store
}

// AutosaveConfig returns the active autosave configuration.
func (r *NavigationRuntime) AutosaveConfig() *AutosaveConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.autosave
}

// Handle executes one navigation frame and returns JSON-serializable data.
// When autosave is active and the frame changed session state, a save is
// scheduled asynchronously so interactive commands never pay for I/O.
func (r *NavigationRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	data, err := r.dispatch(ctx, frame)
	if err != nil {
		return nil, nil, err
	}
	r.maybeAutosave(ctx, frame)
	return data, nil, nil
}

// dispatch runs one frame against the session service.
func (r *NavigationRuntime) dispatch(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	switch frame.Cmd {
	case "open", "goto":
		var request struct {
			URL string `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if frame.Cmd == "open" {
			outcome, err := service.Open(ctx, request.URL)
			return outcome, err
		}
		outcome, err := service.Goto(ctx, request.URL)
		return outcome, err
	case "back":
		outcome, err := service.Back(ctx)
		return outcome, err
	case "forward":
		outcome, err := service.Forward(ctx)
		return outcome, err
	case "reload":
		outcome, err := service.Reload(ctx)
		return outcome, err
	case "wait":
		var condition engine.WaitCondition
		if err := decodeArgs(frame, &condition); err != nil {
			return nil, err
		}
		result, err := service.Wait(ctx, condition)
		return result, err
	case "snapshot":
		var options engine.SnapshotOptions
		if err := decodeArgs(frame, &options); err != nil {
			return nil, err
		}
		if options.Diff || options.Since != "" {
			result, err := service.SnapshotDiff(ctx, options)
			return result, err
		}
		result, err := service.Snapshot(ctx, options)
		return result, err
	case string(engine.ActionClick), string(engine.ActionDoubleClick), string(engine.ActionFill), string(engine.ActionType), string(engine.ActionPress), string(engine.ActionHover), string(engine.ActionFocus), string(engine.ActionSelect), string(engine.ActionCheck), string(engine.ActionUncheck), string(engine.ActionScroll), string(engine.ActionScrollIntoView):
		var request engine.InteractionRequest
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if request.Action == "" {
			request.Action = engine.InteractionAction(frame.Cmd)
		}
		result, err := service.Interact(ctx, request)
		var interactionErr *engine.InteractionError
		if errors.As(err, &interactionErr) {
			return nil, &Error{Code: interactionErr.Code, Message: interactionErr.Message, Hint: interactionErr.Hint}
		}
		return result, err
	case "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked":
		var request engine.InspectionRequest
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
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
			return nil, &Error{Code: inspectionErr.Code, Message: inspectionErr.Message, Hint: inspectionErr.Hint}
		}
		return result, err
	case "find":
		var request engine.FindRequest
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		result, err := service.Find(ctx, request)
		var findErr *engine.FindError
		if errors.As(err, &findErr) {
			return nil, &Error{Code: findErr.Code, Message: findErr.Message, Details: map[string]any{"matches": findErr.Matches}}
		}
		return result, err
	case "cookies.list":
		var request struct {
			URLs []string `json:"urls,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		cookies, err := service.CookiesForURLs(ctx, request.URLs)
		if err != nil {
			return nil, err
		}
		origin, originErr := service.Origin(ctx)
		if originErr != nil {
			origin = ""
		}
		return map[string]any{"origin": origin, "cookies": cookies}, nil
	case "cookies.set":
		var request struct {
			Cookie engine.Cookie `json:"cookie"`
			URL    string        `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetCookie(ctx, request.Cookie, request.URL); err != nil {
			return nil, err
		}
		return map[string]any{"set": request.Cookie.Name}, nil
	case "cookies.clear":
		var request struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.DeleteCookie(ctx, request.Name, request.URL); err != nil {
			return nil, err
		}
		return map[string]any{"cleared": request.Name}, nil
	case "storage.list":
		var request struct {
			Kind engine.StorageKind `json:"kind"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		items, err := service.Storage().StorageItems(ctx, request.Kind)
		if err != nil {
			return nil, err
		}
		origin, originErr := service.Origin(ctx)
		if originErr != nil {
			origin = ""
		}
		return map[string]any{"origin": origin, "kind": request.Kind, "items": items}, nil
	case "storage.set":
		var request struct {
			Kind  engine.StorageKind `json:"kind"`
			Key   string             `json:"key"`
			Value string             `json:"value"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.Storage().SetStorageItem(ctx, request.Kind, request.Key, request.Value); err != nil {
			return nil, err
		}
		return map[string]any{"set": request.Key}, nil
	case "storage.clear":
		var request struct {
			Kind engine.StorageKind `json:"kind"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.Storage().ClearStorage(ctx, request.Kind); err != nil {
			return nil, err
		}
		return map[string]any{"cleared": request.Kind}, nil
	case "set.viewport":
		var request struct {
			Width  int64   `json:"width"`
			Height int64   `json:"height"`
			Scale  float64 `json:"scale,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetViewport(ctx, request.Width, request.Height, request.Scale); err != nil {
			return nil, err
		}
		return map[string]any{"viewport": []int64{request.Width, request.Height}}, nil
	case "set.device":
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetDevice(ctx, request.Name); err != nil {
			return nil, err
		}
		return map[string]any{"device": request.Name}, nil
	case "set.geo":
		var request struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetGeo(ctx, request.Latitude, request.Longitude); err != nil {
			return nil, err
		}
		return map[string]any{"geo": []float64{request.Latitude, request.Longitude}}, nil
	case "set.offline":
		var request struct {
			Offline bool `json:"offline"`
		}
		_ = decodeOptionalArgs(frame, &request)
		if err := service.SetOffline(ctx, request.Offline); err != nil {
			return nil, err
		}
		return map[string]any{"offline": request.Offline}, nil
	case "set.headers":
		var request struct {
			Headers map[string]string `json:"headers"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetHeaders(ctx, request.Headers); err != nil {
			return nil, err
		}
		return map[string]any{"headers_set": len(request.Headers)}, nil
	case "set.media":
		var request struct {
			Dark bool `json:"dark"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetMedia(ctx, request.Dark); err != nil {
			return nil, err
		}
		return map[string]any{"media": map[bool]string{true: "dark", false: "light"}[request.Dark]}, nil
	case "set.user-agent":
		var request struct {
			UserAgent string `json:"user_agent"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetUserAgent(ctx, request.UserAgent); err != nil {
			return nil, err
		}
		return map[string]any{"user_agent_set": true}, nil
	default:
		return nil, fmt.Errorf("unknown navigation command %q", frame.Cmd)
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

// serviceIfReady returns the session service without launching a browser.
func (r *NavigationRuntime) serviceIfReady(session string) (*engine.NavigationService, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	service := r.services[session]
	if service == nil {
		return nil, errors.New("session has no live browser")
	}
	return service, nil
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
	userDataDir := info.UserDataDir
	if r.profile != "" {
		userDataDir = r.profile
		slog.Warn("chrome profile reuse", "session", session, "profile", r.profile, "warning", profiles.Warning)
	}
	browser := chrome.New(chrome.Options{ExecutablePath: r.executable, UserDataDir: userDataDir})
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
	// Restore a named state into the fresh browser when the daemon was
	// started with --restore for this session.
	if name := r.restoreOnStart[session]; name != "" && r.stateStore != nil {
		stateRuntime := NewStateRuntime(r.stateStore, r)
		if _, _, err := stateRuntime.Load(context.Background(), session, name); err != nil {
			slog.Warn("restore state on start failed", "session", session, "state", name, "error", err)
		} else {
			slog.Info("restored state on start", "session", session, "state", name)
		}
	}
	return service, nil
}

// maybeAutosave persists session state after state-changing frames when an
// autosave policy is active. The save runs asynchronously so interactive
// commands never pay for storage I/O, and the minimum interval throttles
// rapid command sequences (issue B-36: "no measurable latency").
func (r *NavigationRuntime) maybeAutosave(ctx context.Context, frame Frame) {
	r.mu.Lock()
	config := r.autosave
	store := r.stateStore
	r.mu.Unlock()
	if config == nil || store == nil || config.Key == "" || config.Policy == AutosaveNever {
		return
	}
	if !isStateChangingFrame(frame.Cmd) {
		return
	}
	r.mu.Lock()
	last := r.lastAutosave[frame.Session]
	now := time.Now()
	if config.Policy == AutosaveAuto && !last.IsZero() && now.Sub(last) < config.Interval {
		r.mu.Unlock()
		return
	}
	r.lastAutosave[frame.Session] = now
	r.mu.Unlock()

	go func() {
		saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		stateRuntime := NewStateRuntime(store, r)
		if _, err := stateRuntime.Save(saveCtx, frame.Session, config.Key); err != nil {
			slog.Warn("autosave failed", "session", frame.Session, "state", config.Key, "error", err)
		}
	}()
}

// isStateChangingFrame reports whether a frame can mutate cookies or storage.
func isStateChangingFrame(command string) bool {
	switch command {
	case "open", "goto", "back", "forward", "reload", "click", "dblclick", "fill", "type", "press", "hover", "focus", "select", "check", "uncheck", "scroll", "scrollintoview", "cookies.set", "cookies.clear", "storage.set", "storage.clear":
		return true
	default:
		return false
	}
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
