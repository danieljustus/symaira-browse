package safaribidi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// NavigationState reports the page state the engine can honestly observe.
//
// HTTPStatus is always zero. Safari implements no network module, so the
// response status is not obtainable at all; reporting anything else would be
// an invention. NetworkIdle is likewise always false rather than optimistic:
// with no network events there is nothing to observe going quiet, and claiming
// idle would let a networkidle wait pass on no evidence.
func (e *Engine) NavigationState(ctx context.Context, page engine.Page) (engine.NavigationState, error) {
	contextID, err := e.pageContext(page)
	if err != nil {
		return engine.NavigationState{}, err
	}
	var result struct {
		Type      string `json:"type"`
		Result    remote `json:"result"`
		Exception struct {
			Text      string `json:"text"`
			Exception remote `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := e.call(ctx, "script.evaluate", map[string]any{
		"expression":   `JSON.stringify({url: location.href, ready: document.readyState})`,
		"target":       map[string]any{"context": contextID},
		"awaitPromise": false,
	}, &result); err != nil {
		return engine.NavigationState{}, err
	}
	if result.Type == "exception" {
		text := result.Exception.Text
		if text == "" {
			text = string(result.Exception.Exception.Value)
		}
		return engine.NavigationState{}, fmt.Errorf("safari-bidi engine: read navigation state: %s", text)
	}
	var encoded string
	if err := json.Unmarshal(result.Result.Value, &encoded); err != nil {
		return engine.NavigationState{}, fmt.Errorf("safari-bidi engine: decode navigation state: %w", err)
	}
	var payload struct {
		URL   string `json:"url"`
		Ready string `json:"ready"`
	}
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		return engine.NavigationState{}, fmt.Errorf("safari-bidi engine: decode navigation state: %w", err)
	}
	return engine.NavigationState{URL: payload.URL, ReadyState: payload.Ready}, nil
}

// BlockedRequests reports navigation targets denied by the URL policies.
func (e *Engine) BlockedRequests() []engine.BlockedRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	blocked := make([]engine.BlockedRequest, 0, len(e.blocked))
	for _, entry := range e.blocked {
		blocked = append(blocked, *entry)
	}
	sort.Slice(blocked, func(i, j int) bool { return blocked[i].URL < blocked[j].URL })
	return blocked
}

// Limitations states, at startup, exactly how far the URL policies reach.
//
// Issue #355 expected this engine to enforce the allowlist and SSRF guard on
// subresources through BiDi's network module — the one capability
// safari-attach structurally cannot have. Safari 27.0 does not implement that
// module ("'network' domain was not found"), so the reach is the same as
// safari-attach's: navigation targets only. A page this engine loads can still
// fetch whatever it likes. Saying so at startup is the difference between a
// known limitation and a false sense of enforcement.
func (e *Engine) Limitations() []string {
	return []string{
		"safari-bidi enforces the domain allowlist and SSRF guard on navigation targets only, specifically URLs passed directly to navigate: Safari 27.0 implements no WebDriver BiDi network module, so redirects, script-initiated navigations, and subresource requests (fetch, XHR, images, scripts) are not intercepted and not policed",
	}
}

var (
	_ engine.Engine                  = (*Engine)(nil)
	_ engine.NavigationStateProvider = (*Engine)(nil)
	_ engine.NetworkPolicyReporter   = (*Engine)(nil)
	_ engine.InspectionEngine        = (*Engine)(nil)
	_ engine.TabManager              = (*Engine)(nil)
	_ engine.FrameManager            = (*Engine)(nil)
	_ engine.CapabilityReporter      = (*Engine)(nil)
)
