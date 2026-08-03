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
	mu              sync.Mutex
	registry        *SessionRegistry
	executable      string
	profile         string
	allowedDomains  []string
	ssrfEnabled     bool
	allowPrivate    bool
	headless        bool
	engines         map[string]engine.Engine
	browserContexts map[string]engine.Context
	tabs            map[string][]*sessionTab
	activeTab       map[string]int
	autosave        *AutosaveConfig
	stateStore      *state.Store
	lastAutosave    map[string]time.Time
	restoreOnStart  map[string]string // session -> state name to restore
	uploadDirs      []string          // allowed upload roots (issue #63)
	recorders       map[string]*recorderState
}

// sessionTab is one tab of a session. Every tab owns its own navigation
// service (and therefore its own ref table), so refs stay valid per tab and
// survive tab switches.
type sessionTab struct {
	Label   string
	Service *engine.NavigationService
	Page    engine.Page
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
	// AllowedDomains activates the domain allowlist network policy for every
	// session engine (see chrome.Options.AllowedDomains).
	AllowedDomains []string
	// SSRFEnabled activates the SSRF guard for every session engine (see
	// chrome.Options.SSRFEnabled). It is the MCP-mode default.
	SSRFEnabled bool
	// AllowPrivate relaxes the SSRF guard (--allow-private).
	AllowPrivate bool
	// Headless launches Chrome headless (no GUI session); used in CI and
	// agent contexts.
	Headless bool
	// UploadDirs are the allowed roots for file uploads (issue #63);
	// paths outside are rejected by the path guard.
	UploadDirs []string
}

// NewNavigationRuntime creates a runtime. Chrome is not started until the
// first navigation or wait operation for a session.
func NewNavigationRuntime(registry *SessionRegistry, executable string, options NavigationRuntimeOptions) *NavigationRuntime {
	if executable == "" {
		executable = os.Getenv("SYMBROWSE_EXECUTABLE_PATH")
	}
	return &NavigationRuntime{
		registry:        registry,
		executable:      executable,
		profile:         options.Profile,
		allowedDomains:  options.AllowedDomains,
		ssrfEnabled:     options.SSRFEnabled,
		allowPrivate:    options.AllowPrivate,
		headless:        options.Headless,
		engines:         make(map[string]engine.Engine),
		browserContexts: make(map[string]engine.Context),
		tabs:            make(map[string][]*sessionTab),
		activeTab:       make(map[string]int),
		uploadDirs:      options.UploadDirs,
		autosave:        options.Autosave,
		stateStore:      options.StateStore,
		lastAutosave:    make(map[string]time.Time),
		restoreOnStart:  options.RestoreOnStart,
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

// Handle executes one navigation frame and returns JSON-serializable data
// together with network-policy warnings collected from the session engine.
// When autosave is active and the frame changed session state, a save is
// scheduled asynchronously so interactive commands never pay for I/O.
func (r *NavigationRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	if strings.HasPrefix(frame.Cmd, "flow.record.") {
		data, err := r.handleRecordFrame(ctx, frame)
		return data, nil, err
	}
	if strings.HasPrefix(frame.Cmd, "tab.") || frame.Cmd == "window.new" {
		data, err := r.handleTabFrame(ctx, frame)
		return data, nil, err
	}
	if strings.HasPrefix(frame.Cmd, "frame.") {
		data, err := r.handleFrameFrame(ctx, frame)
		return data, nil, err
	}
	if strings.HasPrefix(frame.Cmd, "dialog.") {
		data, err := r.handleDialogFrame(ctx, frame)
		return data, nil, err
	}
	data, err := r.dispatch(ctx, frame)
	if err != nil {
		return nil, nil, err
	}
	r.maybeAutosave(ctx, frame)
	r.recordFrame(ctx, frame.Session, frame)
	return data, r.policyWarnings(frame.Session), nil
}

// dispatch runs one frame against the session service.
func (r *NavigationRuntime) dispatch(ctx context.Context, frame Frame) (any, error) {
	switch frame.Cmd {
	case "console.list", "console.clear", "errors.list", "errors.clear":
		return r.handleRuntimeEventsFrame(ctx, frame)
	case "eval":
		return r.handleEvalFrame(ctx, frame)
	case "network.requests", "network.request":
		return r.handleNetworkReadFrame(ctx, frame)
	case "network.route", "network.unroute", "network.har":
		return r.handleNetworkControlFrame(ctx, frame)
	case "upload":
		return r.handleUploadFrame(ctx, frame)
	case "downloads.list", "download.setdir":
		return r.handleDownloadFrame(ctx, frame)
	}
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
	case "read":
		var request struct {
			URL        string `json:"url,omitempty"`
			EngineHint bool   `json:"engine_hint,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if request.URL != "" {
			if _, err := service.Open(ctx, request.URL); err != nil {
				return nil, err
			}
		}
		readData, err := readPage(ctx, service)
		if err != nil {
			return nil, err
		}
		if !request.EngineHint {
			return readData, nil
		}
		// The engine hint (issue #35) compares the rendered page with a
		// JavaScript-disabled probe load. The page must be fully settled
		// first so delayed hydration (SPA fixtures) counts as JS-needed,
		// and the comparison capture must happen after settling.
		if _, err := service.Wait(ctx, engine.WaitCondition{Kind: engine.WaitLoad, LoadState: engine.LoadNetworkIdle}); err != nil {
			return nil, err
		}
		settledData, err := readPage(ctx, service)
		if err != nil {
			return nil, err
		}
		settledMap, ok := settledData.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("read material has an unexpected shape")
		}
		urlValue, err := service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectURL})
		if err != nil {
			return nil, err
		}
		htmlValue, _ := settledMap["html"].(string)
		hint, err := service.JSRequired(ctx, inspectionValue(urlValue), htmlValue)
		if err != nil {
			return nil, err
		}
		settledMap["js_required"] = hint.Required
		settledMap["js_required_reason"] = hint.Reason
		return settledMap, nil

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
	case "a11y":
		var options engine.A11yOptions
		if err := decodeOptionalArgs(frame, &options); err != nil {
			return nil, err
		}
		result, err := service.Audit(ctx, options)
		if err != nil {
			return nil, err
		}
		return result, nil
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

// serviceIfReady returns the active tab's session service without launching a
// browser. It reports an error when the session has no live browser yet.
func (r *NavigationRuntime) serviceIfReady(session string) (*engine.NavigationService, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tabs := r.tabs[session]
	if len(tabs) == 0 {
		return nil, errors.New("session has no live browser")
	}
	index := r.activeTab[session]
	if index < 0 || index >= len(tabs) {
		index = 0
	}
	return tabs[index].Service, nil
}

// policyWarnings converts the session engine's network-policy state into the
// protocol warnings[] payload: denied requests are counted per URL, known
// enforcement limitations are reported once per response.
func (r *NavigationRuntime) policyWarnings(session string) []Warning {
	r.mu.Lock()
	browser := r.engines[session]
	r.mu.Unlock()
	reporter, ok := browser.(engine.NetworkPolicyReporter)
	if !ok {
		return nil
	}
	return networkPolicyWarnings(reporter)
}

const maxBlockedURLWarnings = 10

func networkPolicyWarnings(reporter engine.NetworkPolicyReporter) []Warning {
	var warnings []Warning
	blocked := reporter.BlockedRequests()
	if len(blocked) > 0 {
		total := 0
		for _, entry := range blocked {
			total += entry.Count
		}
		warnings = append(warnings, Warning{Kind: "network_policy", Severity: "warning", Message: fmt.Sprintf("domain allowlist blocked %d request(s)", total)})
		for _, entry := range blocked {
			if len(warnings) >= maxBlockedURLWarnings+1 {
				warnings = append(warnings, Warning{Kind: "network_policy.blocked", Severity: "warning", Message: fmt.Sprintf("and %d more blocked URL(s)", len(blocked)-maxBlockedURLWarnings)})
				break
			}
			warnings = append(warnings, Warning{Kind: "network_policy.blocked", Severity: "warning", Message: fmt.Sprintf("blocked %s %s (%d requests)", entry.ResourceType, entry.URL, entry.Count)})
		}
	}
	for _, limitation := range reporter.Limitations() {
		warnings = append(warnings, Warning{Kind: "network_policy.limitation", Severity: "warning", Message: limitation})
	}
	return warnings
}

func (r *NavigationRuntime) service(ctx context.Context, session string) (*engine.NavigationService, error) {
	r.mu.Lock()
	if tabs := r.tabs[session]; len(tabs) > 0 {
		index := r.activeTab[session]
		if index < 0 || index >= len(tabs) {
			index = 0
		}
		service := tabs[index].Service
		r.mu.Unlock()
		return service, nil
	}
	if r.registry == nil {
		r.mu.Unlock()
		return nil, errors.New("session registry is required")
	}
	info, err := r.registry.Get(session)
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if r.executable == "" {
		r.mu.Unlock()
		return nil, errors.New("browser executable is not configured; set SYMBROWSE_EXECUTABLE_PATH")
	}
	userDataDir := info.UserDataDir
	if r.profile != "" {
		userDataDir = r.profile
		slog.Warn("chrome profile reuse", "session", session, "profile", r.profile, "warning", profiles.Warning)
	}
	executable := r.executable
	allowedDomains := r.allowedDomains
	ssrfEnabled := r.ssrfEnabled
	allowPrivate := r.allowPrivate
	headless := r.headless
	restoreOnStart := r.restoreOnStart[session]
	stateStore := r.stateStore
	r.mu.Unlock()

	browser := chrome.New(chrome.Options{ExecutablePath: executable, UserDataDir: userDataDir, AllowedDomains: allowedDomains, SSRFEnabled: ssrfEnabled, AllowPrivate: allowPrivate, Headless: headless})
	if err := browser.Launch(ctx); err != nil {
		return nil, err
	}
	if reporter, ok := any(browser).(engine.NetworkPolicyReporter); ok {
		for _, limitation := range reporter.Limitations() {
			slog.Warn("network policy limitation", "session", session, "message", limitation)
		}
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
	service := engine.NewNavigationService(browser, page, engine.NavigationOptions{ProbeContext: browserContext})
	r.mu.Lock()
	r.engines[session] = browser
	r.browserContexts[session] = browserContext
	r.tabs[session] = []*sessionTab{{Label: "t1", Service: service, Page: page}}
	r.activeTab[session] = 0
	r.mu.Unlock()
	_ = r.registry.SetActiveTabs(session, 1)
	// Restore a named state into the fresh browser when the daemon was
	// started with --restore for this session. The restore runs with its own
	// context so a slow browser start can never time out the first request.
	if restoreOnStart != "" && stateStore != nil {
		go func() {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			stateRuntime := NewStateRuntime(stateStore, r)
			if _, _, err := stateRuntime.Load(restoreCtx, session, restoreOnStart); err != nil {
				slog.Warn("restore state on start failed", "session", session, "state", restoreOnStart, "error", err)
			} else {
				slog.Info("restored state on start", "session", session, "state", restoreOnStart)
			}
		}()
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
	r.tabs = make(map[string][]*sessionTab)
	r.activeTab = make(map[string]int)
	r.browserContexts = make(map[string]engine.Context)
	return first
}

// readPage fetches the raw page material for the read command: the rendered
// HTML, the document title and the current URL. Rendering into the fetch
// output schema happens on the CLI side (internal/render).
func readPage(ctx context.Context, service *engine.NavigationService) (any, error) {
	html, err := service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectHTML})
	if err != nil {
		return nil, err
	}
	title, err := service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectTitle})
	if err != nil {
		return nil, err
	}
	url, err := service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectURL})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"html":  inspectionValue(html),
		"title": inspectionValue(title),
		"url":   inspectionValue(url),
	}, nil
}

func inspectionValue(result engine.InspectionResult) string {
	var value string
	if err := json.Unmarshal(result.Value, &value); err != nil {
		return ""
	}
	return value
}

// tabHandle returns the current tab's page handle (for tab/frame/dialog
// engine operations).
func (r *NavigationRuntime) tabHandle(session string) (engine.Page, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tabs := r.tabs[session]
	if len(tabs) == 0 {
		return engine.Page{}, false
	}
	index := r.activeTab[session]
	if index < 0 || index >= len(tabs) {
		index = 0
	}
	return tabs[index].Page, true
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

// handleRuntimeEventsFrame serves the console and errors buffers (issue
// #60). Without a running session engine the buffers are empty; engines
// without the RuntimeEvents capability fail with an explicit capability
// error instead of fabricated results.
func (r *NavigationRuntime) handleRuntimeEventsFrame(ctx context.Context, frame Frame) (any, error) {
	r.mu.Lock()
	browser := r.engines[frame.Session]
	r.mu.Unlock()
	if browser == nil {
		return emptyRuntimePayload(frame.Cmd), nil
	}
	events, ok := browser.(engine.RuntimeEvents)
	if !ok {
		return nil, fmt.Errorf("browser engine does not support %s", strings.TrimSuffix(frame.Cmd, ".list"))
	}
	service, err := r.serviceIfReady(frame.Session)
	if err != nil || service == nil {
		return emptyRuntimePayload(frame.Cmd), nil
	}
	page := service.Page()
	switch frame.Cmd {
	case "console.list":
		if err := events.EnableRuntimeEvents(ctx, page); err != nil {
			return nil, err
		}
		entries := events.ConsoleEvents(page)
		return map[string]any{"entries": entries, "count": len(entries)}, nil
	case "console.clear":
		events.ClearConsole(page)
		return map[string]any{"cleared": true}, nil
	case "errors.list":
		if err := events.EnableRuntimeEvents(ctx, page); err != nil {
			return nil, err
		}
		entries := events.ErrorEvents(page)
		return map[string]any{"entries": entries, "count": len(entries)}, nil
	case "errors.clear":
		events.ClearErrors(page)
		return map[string]any{"cleared": true}, nil
	default:
		return nil, fmt.Errorf("unknown runtime events command %q", frame.Cmd)
	}
}

func emptyRuntimePayload(command string) any {
	switch command {
	case "console.list", "errors.list":
		return map[string]any{"entries": []any{}, "count": 0}
	default:
		return map[string]any{"cleared": true}
	}
}

// handleEvalFrame executes one JavaScript expression on the active page
// (issue #60). The result is returned protocol-neutral; exceptions carry
// their text.
func (r *NavigationRuntime) handleEvalFrame(ctx context.Context, frame Frame) (any, error) {
	var request struct {
		Expression string `json:"expression"`
	}
	if err := decodeArgs(frame, &request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Expression) == "" {
		return nil, errors.New("eval requires a non-empty expression")
	}
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	result, err := service.Evaluate(ctx, request.Expression)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":           result.Type,
		"value":          result.Value,
		"description":    result.Description,
		"exception_text": result.ExceptionText,
	}, nil
}

// handleNetworkReadFrame serves the read-only network commands: the
// request list (masked headers) and one request by id (issue #59).
func (r *NavigationRuntime) handleNetworkReadFrame(ctx context.Context, frame Frame) (any, error) {
	r.mu.Lock()
	browser := r.engines[frame.Session]
	r.mu.Unlock()
	events, ok := browser.(engine.NetworkEvents)
	if !ok {
		if browser == nil {
			return map[string]any{"requests": []any{}, "count": 0}, nil
		}
		return nil, fmt.Errorf("browser engine does not support network inspection")
	}
	service, err := r.serviceIfReady(frame.Session)
	if err != nil || service == nil {
		return map[string]any{"requests": []any{}, "count": 0}, nil
	}
	page := service.Page()
	switch frame.Cmd {
	case "network.requests":
		if err := events.EnableNetworkCapture(ctx, page); err != nil {
			return nil, err
		}
		requests := events.Requests(page)
		return map[string]any{"requests": requests, "count": len(requests)}, nil
	case "network.request":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		entry, found := events.Request(page, request.ID)
		if !found {
			return nil, &Error{Code: "network_request_not_found", Message: fmt.Sprintf("no captured request with id %q", request.ID)}
		}
		return map[string]any{"request": entry}, nil
	default:
		return nil, fmt.Errorf("unknown network read command %q", frame.Cmd)
	}
}

// handleNetworkControlFrame serves routing and HAR commands. Mocking is
// policy-gated (ClassNetworkMock) at the daemon handler level; the frames
// themselves never fall back to a silent allow (issue #59).
func (r *NavigationRuntime) handleNetworkControlFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	page := service.Page()
	r.mu.Lock()
	browser := r.engines[frame.Session]
	r.mu.Unlock()
	events, ok := browser.(engine.NetworkEvents)
	if !ok {
		return nil, fmt.Errorf("browser engine does not support network control")
	}
	switch frame.Cmd {
	case "network.route":
		var route engine.NetworkRoute
		if err := decodeArgs(frame, &route); err != nil {
			return nil, err
		}
		if err := events.RouteRequests(ctx, page, route); err != nil {
			return nil, err
		}
		return map[string]any{"routed": route.Pattern, "action": route.Action}, nil
	case "network.unroute":
		var request struct {
			Pattern string `json:"pattern"`
		}
		_ = decodeOptionalArgs(frame, &request)
		removed, err := events.UnrouteRequests(ctx, page, request.Pattern)
		if err != nil {
			return nil, err
		}
		return map[string]any{"removed": removed}, nil
	case "network.har":
		var request struct {
			Action  string `json:"action"` // start | stop
			Content string `json:"content,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		switch request.Action {
		case "start":
			if err := events.EnableNetworkCapture(ctx, page); err != nil {
				return nil, err
			}
			return map[string]any{"started": true}, nil
		case "stop":
			document, err := events.HAR(ctx, page, engine.HAROptions{Content: request.Content})
			if err != nil {
				return nil, err
			}
			return map[string]any{"har": json.RawMessage(document), "entries": len(events.Requests(page))}, nil
		default:
			return nil, &Error{Code: "invalid_har_action", Message: "network har action must be \"start\" or \"stop\""}
		}
	default:
		return nil, fmt.Errorf("unknown network control command %q", frame.Cmd)
	}
}

// handleUploadFrame uploads files into the selected file input. Every file
// is path-guarded against the runtime's allowed upload directories
// (issue #63 AC: traversal, symlink escapes and outside paths fail).
func (r *NavigationRuntime) handleUploadFrame(ctx context.Context, frame Frame) (any, error) {
	var request engine.UploadRequest
	if err := decodeArgs(frame, &request); err != nil {
		return nil, err
	}
	if len(request.AllowedDirs) == 0 {
		request.AllowedDirs = r.uploadDirs
	}
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	browser := r.engines[frame.Session]
	r.mu.Unlock()
	transfer, ok := browser.(engine.FileTransfer)
	if !ok {
		return nil, fmt.Errorf("browser engine does not support uploads")
	}
	result, err := transfer.UploadFiles(ctx, service.Page(), request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"uploaded": result.Uploaded}, nil
}

// handleDownloadFrame serves the downloads.list and download.setdir frames
// (issue #63). Download events carry origin URL, size and checksum.
func (r *NavigationRuntime) handleDownloadFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	browser := r.engines[frame.Session]
	r.mu.Unlock()
	transfer, ok := browser.(engine.FileTransfer)
	if !ok {
		return nil, fmt.Errorf("browser engine does not support downloads")
	}
	switch frame.Cmd {
	case "download.setdir":
		var request struct {
			Dir string `json:"dir"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := transfer.SetDownloadBehavior(ctx, service.Page(), engine.DownloadConfig{Dir: request.Dir}); err != nil {
			return nil, err
		}
		return map[string]any{"download_dir": request.Dir}, nil
	case "downloads.list":
		events := transfer.DownloadEvents(service.Page())
		return map[string]any{"downloads": events, "count": len(events)}, nil
	default:
		return nil, fmt.Errorf("unknown download command %q", frame.Cmd)
	}
}
