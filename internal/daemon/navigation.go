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

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/chrome"
)

// NavigationRuntime lazily owns one protocol-neutral navigation service and
// Chrome engine per session. CDP details remain confined to engine/chrome.
type NavigationRuntime struct {
	mu             sync.Mutex
	registry       *SessionRegistry
	executable     string
	allowedDomains []string
	services       map[string]*engine.NavigationService
	engines        map[string]engine.Engine
}

// NavigationRuntimeOptions configures the browser engines created per session.
type NavigationRuntimeOptions struct {
	// AllowedDomains activates the domain allowlist network policy for every
	// session engine (see chrome.Options.AllowedDomains).
	AllowedDomains []string
}

// NewNavigationRuntime creates a runtime. Chrome is not started until the
// first navigation or wait operation for a session.
func NewNavigationRuntime(registry *SessionRegistry, executable string, options NavigationRuntimeOptions) *NavigationRuntime {
	if executable == "" {
		executable = os.Getenv("SYMBROWSE_EXECUTABLE_PATH")
	}
	return &NavigationRuntime{registry: registry, executable: executable, allowedDomains: options.AllowedDomains, services: make(map[string]*engine.NavigationService), engines: make(map[string]engine.Engine)}
}

// Handle executes one navigation frame and returns JSON-serializable data
// together with network-policy warnings collected from the session engine.
func (r *NavigationRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	data, err := r.dispatch(ctx, frame)
	if err != nil {
		return nil, nil, err
	}
	return data, r.policyWarnings(frame.Session), nil
}

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
	case "read":
		var request struct {
			URL string `json:"url,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if request.URL != "" {
			if _, err := service.Open(ctx, request.URL); err != nil {
				return nil, err
			}
		}
		return readPage(ctx, service)

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
	default:
		return nil, fmt.Errorf("unknown navigation command %q", frame.Cmd)
	}
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
	browser := chrome.New(chrome.Options{ExecutablePath: r.executable, UserDataDir: info.UserDataDir, AllowedDomains: r.allowedDomains})
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

func decodeArgs(frame Frame, target any) error {
	if len(frame.Args) == 0 || string(frame.Args) == "null" {
		return errors.New("command arguments are required")
	}
	if err := json.Unmarshal(frame.Args, target); err != nil {
		return fmt.Errorf("decode %s arguments: %w", frame.Cmd, err)
	}
	return nil
}
