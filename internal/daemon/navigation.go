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
	"github.com/danieljustus/symaira-browse/internal/engine/static"
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
	engineKind      string
	tabs            map[string][]*sessionTab
	activeTab       map[string]int
	autosave        *AutosaveConfig
	stateStore      *state.Store
	lastAutosave    map[string]time.Time
	restoreOnStart  map[string]string // session -> state name to restore
	uploadDirs      []string          // allowed upload roots (issue #63)
	screenshotDirs  []string          // allowed screenshot roots (issue #16)
	requestTimeout  time.Duration     // per-command CDP budget (0 = engine default)
	recorders       map[string]*recorderState
	staticGuard     static.GuardOptions // fetch-hardening for the static engine (step 5)
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
	// ScreenshotDirs are the allowed roots for screenshot files (issue #16);
	// without an explicit directory the first root (cache out dir) is used.
	ScreenshotDirs []string
	// Engine selects the engine implementation: "chrome" (default) or
	// "static" (JS-free HTML reader, issue #64).
	Engine string
	// RequestTimeout is the per-command CDP budget for session engines
	// (chrome.Options.RequestTimeout; default 10s). E2E tests use a
	// generous budget because Chrome round-trips can stall for seconds on
	// loaded machines right after a sibling tab is created.
	RequestTimeout time.Duration
	// StaticGuard provides explicit guard options for the static engine.
	// When nil, hardened defaults (SSRFEnabled: true, RobotsEnabled: true)
	// with AllowPrivate propagated from options are used.
	StaticGuard *static.GuardOptions
}

// NewNavigationRuntime creates a runtime. Chrome is not started until the
// first navigation or wait operation for a session.
func NewNavigationRuntime(registry *SessionRegistry, executable string, options NavigationRuntimeOptions) *NavigationRuntime {
	if executable == "" {
		executable = os.Getenv("SYMBROWSE_EXECUTABLE_PATH")
	}
	var guard static.GuardOptions
	if options.StaticGuard != nil {
		guard = *options.StaticGuard
	} else {
		guard = static.GuardOptions{
			SSRFEnabled:   true,
			AllowPrivate:  options.AllowPrivate,
			RobotsEnabled: true,
		}
	}
	return &NavigationRuntime{
		registry:        registry,
		executable:      executable,
		profile:         options.Profile,
		allowedDomains:  options.AllowedDomains,
		engineKind:      options.Engine,
		ssrfEnabled:     options.SSRFEnabled,
		allowPrivate:    options.AllowPrivate,
		headless:        options.Headless,
		engines:         make(map[string]engine.Engine),
		browserContexts: make(map[string]engine.Context),
		tabs:            make(map[string][]*sessionTab),
		activeTab:       make(map[string]int),
		uploadDirs:      options.UploadDirs,
		screenshotDirs:  options.ScreenshotDirs,
		requestTimeout:  options.RequestTimeout,
		autosave:        options.Autosave,
		stateStore:      options.StateStore,
		lastAutosave:    make(map[string]time.Time),
		restoreOnStart:  options.RestoreOnStart,
		staticGuard:     guard,
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

// dispatch runs one frame against the session service. It is a thin router:
// every command family is delegated to a per-domain handler (see *_frames.go).
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
	case "open", "goto", "back", "forward", "reload", "wait":
		return r.handleNavigationFrame(ctx, frame)
	case "snapshot", "a11y", "screenshot":
		return r.handleCaptureFrame(ctx, frame)
	case string(engine.ActionClick), string(engine.ActionDoubleClick), string(engine.ActionFill), string(engine.ActionType), string(engine.ActionPress), string(engine.ActionHover), string(engine.ActionFocus), string(engine.ActionSelect), string(engine.ActionCheck), string(engine.ActionUncheck), string(engine.ActionScroll), string(engine.ActionScrollIntoView):
		return r.handleInteractionFrame(ctx, frame)
	case "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked":
		return r.handleInspectFrame(ctx, frame)
	case "read", "find":
		return r.handleInspectFrame(ctx, frame)
	case "cookies.list", "cookies.set", "cookies.clear":
		return r.handleCookiesFrame(ctx, frame)
	case "storage.list", "storage.set", "storage.clear":
		return r.handleStorageFrame(ctx, frame)
	case "set.viewport", "set.device", "set.geo", "set.offline", "set.headers", "set.media", "set.user-agent":
		return r.handleEmulationFrame(ctx, frame)
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

// newEngine builds the engine implementation selected by the runtime options:
// "static" (JS-free HTML reader, issue #64) or the default Chrome engine.
func (r *NavigationRuntime) newEngine(userDataDir string) engine.Engine {
	if r.engineKind == "static" {
		return static.NewWithGuard(r.staticGuard)
	}
	return chrome.New(chrome.Options{ExecutablePath: r.executable, UserDataDir: userDataDir, AllowedDomains: r.allowedDomains, SSRFEnabled: r.ssrfEnabled, AllowPrivate: r.allowPrivate, Headless: r.headless, RequestTimeout: r.requestTimeout})
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
	if r.executable == "" && r.engineKind != "static" {
		r.mu.Unlock()
		return nil, errors.New("browser executable is not configured; set SYMBROWSE_EXECUTABLE_PATH")
	}
	userDataDir := info.UserDataDir
	if r.profile != "" {
		userDataDir = r.profile
		slog.Warn("chrome profile reuse", "session", session, "profile", r.profile, "warning", profiles.Warning)
	}
	restoreOnStart := r.restoreOnStart[session]
	stateStore := r.stateStore
	r.mu.Unlock()

	browser := r.newEngine(userDataDir)
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
