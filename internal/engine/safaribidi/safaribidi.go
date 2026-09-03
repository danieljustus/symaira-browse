// Package safaribidi implements the safari-bidi engine: it launches its own
// isolated Safari through safaridriver and speaks W3C WebDriver BiDi over the
// WebSocket that session returns.
//
// This is a second Safari engine, not a replacement for safari-attach
// (issue #297). safaridriver sessions are isolated: the human's tabs, cookies
// and logins are not in them. safari-attach remains the only path to the
// logged-in session; safari-bidi is the honest automation path.
//
// # What Safari's BiDi actually implements
//
// The capability set below is measured, not derived from the specification.
// Measured on 2026-09-03 against Safari 27.0 (26A5425a) on macOS 27.0, with
// the enumeration harness in probe_surface_test.go. Safari ships a partial
// BiDi: the session, browsingContext and script modules are largely present,
// and the input, network-interception, emulation and permissions modules are
// absent outright ("'input' domain was not found"). browsingContext's
// captureScreenshot, locateNodes and print are individually absent, and every
// storage command answers InternalError.
//
// The consequence that shapes this engine: session.subscribe accepts every
// event name and returns success, and Safari then delivers no events at all.
// A navigation plus a console.log, given two seconds, produced zero frames.
// So there is no navigation lifecycle and no console capture here — not
// because they were left unimplemented, but because subscribing to them
// succeeds and silently yields nothing. An engine that reported RuntimeEvents
// on that basis would be reporting a capability it does not have.
//
// See docs/engines.md for the full matrix and issue #355 for the scope this
// measurement narrowed.
package safaribidi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// Engine drives an isolated Safari over WebDriver BiDi.
type Engine struct {
	mu sync.Mutex

	options Options
	live    *session
	closed  bool

	// Allowlist and SSRFGuard are the URL admission policies for navigation,
	// supplied by the daemon. Safari's BiDi has no network module, so these
	// are enforced at navigation targets only; see Limitations.
	Allowlist   *policy.Allowlist
	SSRFGuard   *policy.SSRFGuard
	PolicyError error

	// blocked records navigation targets denied by the policies, keyed by URL.
	blocked map[string]*engine.BlockedRequest

	// context is the browsing context the engine currently addresses. It is
	// the top-level context until SetActiveFrame points it at a frame.
	context string
	// rootContext is the session's top-level context, kept so SetActiveFrame
	// can return to the main frame.
	rootContext string
}

// New creates a safari-bidi engine with default options.
func New() *Engine { return NewWithOptions(Options{}) }

// NewWithOptions creates a safari-bidi engine with explicit options.
func NewWithOptions(options Options) *Engine {
	return &Engine{options: options, blocked: map[string]*engine.BlockedRequest{}}
}

// Launch starts safaridriver, creates the BiDi session, and binds the engine
// to the session's top-level browsing context.
func (e *Engine) Launch(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("safari-bidi engine: already closed")
	}
	if e.live != nil {
		e.mu.Unlock()
		return nil
	}
	options := e.options
	e.mu.Unlock()

	live, err := connect(ctx, options)
	if err != nil {
		return err
	}
	topLevel, err := topLevelContext(ctx, live.conn, options)
	if err != nil {
		_ = live.Close(ctx)
		return err
	}

	e.mu.Lock()
	e.live = live
	e.context = topLevel
	e.rootContext = topLevel
	e.mu.Unlock()
	return nil
}

func topLevelContext(ctx context.Context, conn *connection, options Options) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, options.requestTimeout())
	defer cancel()
	var tree struct {
		Contexts []struct {
			Context string `json:"context"`
		} `json:"contexts"`
	}
	if err := conn.Execute(callCtx, "browsingContext.getTree", map[string]any{}, &tree); err != nil {
		return "", fmt.Errorf("safari-bidi engine: read browsing context tree: %w", err)
	}
	if len(tree.Contexts) == 0 {
		return "", errors.New("safari-bidi engine: session has no browsing context")
	}
	return tree.Contexts[0].Context, nil
}

// call runs one BiDi command against the live session.
func (e *Engine) call(ctx context.Context, method string, params, result any) error {
	e.mu.Lock()
	live := e.live
	closed := e.closed
	timeout := e.options.requestTimeout()
	e.mu.Unlock()
	if closed {
		return errors.New("safari-bidi engine: closed")
	}
	if live == nil {
		return errors.New("safari-bidi engine: not launched")
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return live.conn.Execute(callCtx, method, params, result)
}

// NewContext reports the single BiDi session context.
//
// BiDi user contexts (browser.createUserContext) are a separate module that
// Safari does not implement, so there is one context and it is the session's.
func (e *Engine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "safari-bidi"}, nil
}

// NewPage binds to the session's top-level browsing context.
func (e *Engine) NewPage(_ context.Context, _ engine.Context, _ string) (engine.Page, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.context == "" {
		return engine.Page{}, errors.New("safari-bidi engine: not launched")
	}
	return engine.Page{ID: e.context}, nil
}

// pageContext resolves the BiDi context a Page addresses, falling back to the
// engine's top-level context for zero-valued pages.
func (e *Engine) pageContext(page engine.Page) (string, error) {
	if page.ID != "" {
		return page.ID, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.context == "" {
		return "", errors.New("safari-bidi engine: not launched")
	}
	return e.context, nil
}

// Navigate loads a URL after the target has passed the daemon's URL policies.
//
// browsingContext.navigate with wait=complete is a real load barrier, unlike
// the safari-attach engine's URL polling: Safari does not answer the command
// until the load settles. The navigation lifecycle *events* are still absent
// (see the package comment), but the command's own completion is sufficient
// here and is what the engine relies on.
func (e *Engine) Navigate(ctx context.Context, page engine.Page, target string) (engine.NavigationResult, error) {
	contextID, err := e.pageContext(page)
	if err != nil {
		return engine.NavigationResult{}, err
	}
	if err := e.guardTarget(target); err != nil {
		return engine.NavigationResult{}, err
	}
	var result struct {
		Navigation string `json:"navigation"`
		URL        string `json:"url"`
	}
	if err := e.call(ctx, "browsingContext.navigate", map[string]any{
		"context": contextID,
		"url":     target,
		"wait":    "complete",
	}, &result); err != nil {
		return engine.NavigationResult{ErrorText: err.Error()}, err
	}
	return engine.NavigationResult{FrameID: contextID, LoaderID: result.Navigation}, nil
}

// guardTarget validates a navigation before any BiDi command is issued, and
// records denials so NetworkPolicyReporter can surface them as warnings.
func (e *Engine) guardTarget(target string) error {
	trimmed := strings.TrimSpace(target)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("safari-bidi engine: invalid URL %q: %w", target, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" {
		return fmt.Errorf("safari-bidi engine: unsupported navigation target %q (http/https URL required)", target)
	}

	e.mu.Lock()
	allowlist := e.Allowlist
	ssrfGuard := e.SSRFGuard
	policyErr := e.PolicyError
	e.mu.Unlock()

	if policyErr != nil {
		return fmt.Errorf("safari-bidi engine: URL policy is invalid: %w", policyErr)
	}
	if allowlist != nil && !allowlist.AllowsURL(parsed) {
		e.recordBlocked(trimmed, "domain allowlist")
		return fmt.Errorf("safari-bidi engine: navigation to %q is blocked by the domain allowlist", target)
	}
	if ssrfGuard != nil {
		if err := ssrfGuard.AllowsURL(parsed); err != nil {
			e.recordBlocked(trimmed, "ssrf guard")
			return fmt.Errorf("safari-bidi engine: navigation to %q is blocked by the SSRF guard: %w", target, err)
		}
	}
	return nil
}

func (e *Engine) recordBlocked(target, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.blocked == nil {
		e.blocked = map[string]*engine.BlockedRequest{}
	}
	entry, found := e.blocked[target]
	if !found {
		entry = &engine.BlockedRequest{URL: target, ResourceType: "document", Reason: reason}
		e.blocked[target] = entry
	}
	entry.Count++
}

// Evaluate runs JavaScript in the page realm.
//
// Unlike safari-attach, arbitrary evaluation needs no opt-in here: the session
// is isolated and carries none of the human's logins, so the blast radius of a
// script is the automation window and nothing else.
func (e *Engine) Evaluate(ctx context.Context, page engine.Page, expression string) (engine.EvaluationResult, error) {
	contextID, err := e.pageContext(page)
	if err != nil {
		return engine.EvaluationResult{}, err
	}
	var result struct {
		Type      string `json:"type"`
		Result    remote `json:"result"`
		Exception struct {
			Text       string `json:"text"`
			Exception  remote `json:"exception"`
			StackTrace any    `json:"stackTrace"`
		} `json:"exceptionDetails"`
	}
	if err := e.call(ctx, "script.evaluate", map[string]any{
		"expression":   expression,
		"target":       map[string]any{"context": contextID},
		"awaitPromise": true,
	}, &result); err != nil {
		return engine.EvaluationResult{}, err
	}
	if result.Type == "exception" {
		text := result.Exception.Text
		if text == "" {
			text = string(result.Exception.Exception.Value)
		}
		return engine.EvaluationResult{ExceptionText: text}, nil
	}
	return engine.EvaluationResult{
		Value:       result.Result.Value,
		Type:        result.Result.Type,
		Description: result.Result.Description,
	}, nil
}

// remote is a BiDi RemoteValue, reduced to the fields the engine boundary uses.
type remote struct {
	Type        string          `json:"type"`
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description"`
}

// Screenshot is not supported: Safari 27.0 does not implement
// browsingContext.captureScreenshot ("was not found"), and there is no other
// BiDi capture path.
func (e *Engine) Screenshot(context.Context, engine.Page) ([]byte, error) {
	return nil, engine.UnsupportedOperation(EngineKind, "screenshot")
}

// Close deletes the WebDriver session and stops safaridriver. The automation
// Safari belongs to the engine, so unlike safari-attach it is torn down.
func (e *Engine) Close() error {
	e.mu.Lock()
	live := e.live
	e.live = nil
	e.closed = true
	e.mu.Unlock()
	if live == nil {
		return nil
	}
	return live.Close(context.Background())
}

// Capabilities reports the measured boundary of the safari-bidi engine.
//
// The set is deliberately smaller than issue #355 anticipated. InteractionEngine
// is absent because Safari implements no input module: the only remaining way
// to click would be a JavaScript click(), which bypasses hit-testing and would
// reintroduce exactly the /overlay truth defect that docs/engines.md records
// against safari-attach. NetworkEvents is absent because there is no network
// module to intercept with. RuntimeEvents and any lifecycle capability are
// absent because subscribing succeeds and delivers nothing.
func (e *Engine) Capabilities() engine.Capabilities {
	caps := engine.CapabilitiesFor(EngineKind,
		"InspectionEngine",
		"NavigationStateProvider",
		"NetworkPolicyReporter",
		"FrameManager",
		"TabManager",
	)
	caps.LaunchMode = "launch"
	return caps
}
