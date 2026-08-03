package chrome

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// CDP method names handled by the network policy.
const (
	cdpFetchRequestPaused      = "Fetch.requestPaused"
	cdpTargetAttachedToTarget  = "Target.attachedToTarget"
	cdpCommandFetchEnable      = "Fetch.enable"
	cdpCommandFetchContinue    = "Fetch.continueRequest"
	cdpCommandFetchFail        = "Fetch.failRequest"
	cdpCommandTargetAutoAttach = "Target.setAutoAttach"
	cdpCommandNetworkEnable    = "Network.enable"
)

// fetchEnableParams is the Fetch.enable payload. The catch-all pattern with
// request stage "Request" pauses every request of every resource type before
// it leaves the browser, including navigations, subresources, WebSocket
// handshakes, EventSource, sendBeacon, and worker bootstrap loads.
type fetchEnableParams struct {
	Patterns []fetchPattern `json:"patterns"`
}

type fetchPattern struct {
	URLPattern   string `json:"urlPattern"`
	RequestStage string `json:"requestStage"`
}

type fetchRequestPausedParams struct {
	RequestID string `json:"requestId"`
	Request   struct {
		URL string `json:"url"`
	} `json:"request"`
	ResourceType string `json:"resourceType"`
}

type fetchFailParams struct {
	RequestID   string `json:"requestId"`
	ErrorReason string `json:"errorReason"`
}

type fetchContinueParams struct {
	RequestID string `json:"requestId"`
}

type targetAutoAttachParams struct {
	AutoAttach             bool `json:"autoAttach"`
	WaitForDebuggerOnStart bool `json:"waitForDebuggerOnStart"`
	Flatten                bool `json:"flatten"`
	AutoAttachRelated      bool `json:"autoAttachRelated"`
}

type targetAttachedToTargetParams struct {
	SessionID  string `json:"sessionId"`
	TargetInfo struct {
		Type string `json:"type"`
	} `json:"targetInfo"`
}

// networkPolicy enforces the domain allowlist on the CDP network path. It is
// deny-by-default: every request that is not explicitly allowed is failed
// with "blockedByClient" and counted for the warnings[] report.
type networkPolicy struct {
	allowlist *policy.Allowlist
	ssrf      *policy.SSRFGuard
	call      func(ctx context.Context, sessionID, method string, params, result any) error
	mu        sync.Mutex
	blocked   map[string]*blockedEntry
	total     int
}

// blockedEntry counts requests denied for one distinct URL.
type blockedEntry struct {
	url          string
	resourceType string
	reason       string
	count        int
}

// newNetworkPolicy builds the combined network policy. The allowlist
// activates when patterns are supplied; the SSRF guard activates when
// ssrfEnabled is set (the MCP-mode default). Either active component arms the
// CDP interception and the fail-fast navigation gate.
func newNetworkPolicy(patterns []string, ssrfEnabled, allowPrivate bool, call func(ctx context.Context, sessionID, method string, params, result any) error) (*networkPolicy, error) {
	allowlist, err := policy.ParseAllowlist(patterns)
	if err != nil {
		return nil, err
	}
	var ssrf *policy.SSRFGuard
	if ssrfEnabled {
		ssrf = policy.NewSSRFGuard(allowPrivate)
	}
	return &networkPolicy{
		allowlist: allowlist,
		ssrf:      ssrf,
		call:      call,
		blocked:   make(map[string]*blockedEntry),
	}, nil
}

// active reports whether any policy component requires enforcement.
func (p *networkPolicy) active() bool {
	if p == nil {
		return false
	}
	return p.allowlist.Active() || (p.ssrf != nil && p.ssrf.Enabled())
}

// allowsURL applies the deny-by-default policy to one request URL. Unparsable
// URLs are denied: a request the policy cannot understand must not proceed.
// The returned reason explains which policy denied the request.
func (p *networkPolicy) allowsURL(rawURL string) (bool, string) {
	if !p.active() {
		return true, ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, "unparsable URL"
	}
	if !p.allowlist.AllowsURL(u) {
		return false, "denied by the domain allowlist"
	}
	if err := p.ssrf.AllowsURL(u); err != nil {
		return false, "denied by the SSRF guard"
	}
	return true, ""
}

// handleEvent routes protocol events relevant to the policy. It runs on the
// connection's event goroutine and may issue CDP commands.
func (p *networkPolicy) handleEvent(sessionID, method string, params json.RawMessage) {
	if p == nil || !p.active() {
		return
	}
	switch method {
	case cdpFetchRequestPaused:
		p.handleRequestPaused(sessionID, params)
	case cdpTargetAttachedToTarget:
		p.handleAttachedToTarget(params)
	}
}

// handleRequestPaused decides one intercepted request. Every paused request
// must receive continue or fail promptly, otherwise Chrome stalls the page.
func (p *networkPolicy) handleRequestPaused(sessionID string, params json.RawMessage) {
	var paused fetchRequestPausedParams
	if err := json.Unmarshal(params, &paused); err != nil {
		slog.Debug("network policy: undecodable Fetch.requestPaused", "error", err)
		// Fail closed: an undecodable request must not proceed.
		p.respondBlocked(sessionID, "", "", "", "undecodable request")
		return
	}
	if paused.RequestID == "" {
		return
	}
	allowed, reason := p.allowsURL(paused.Request.URL)
	if !allowed {
		p.respondBlocked(sessionID, paused.RequestID, paused.Request.URL, paused.ResourceType, reason)
		return
	}
	ctx := context.Background()
	if err := p.call(ctx, sessionID, cdpCommandFetchContinue, fetchContinueParams{RequestID: paused.RequestID}, nil); err != nil {
		// The request may already have been canceled by Chrome; a failed
		// continue must not stall the page, so the error is only logged.
		slog.Debug("network policy: continueRequest failed", "error", err)
	}
}

// respondBlocked fails one intercepted request and counts it.
func (p *networkPolicy) respondBlocked(sessionID, requestID, rawURL, resourceType, reason string) {
	p.record(rawURL, resourceType, reason)
	ctx := context.Background()
	if err := p.call(ctx, sessionID, cdpCommandFetchFail, fetchFailParams{RequestID: requestID, ErrorReason: "blockedByClient"}, nil); err != nil {
		slog.Debug("network policy: failRequest failed", "error", err)
	}
}

// handleAttachedToTarget extends the policy to every related target Chrome
// attaches: dedicated/shared/service workers, cross-origin iframes, and
// window.open popups. Each such target gets its own CDP session; requests
// made there are invisible to the page session, so the interception must be
// enabled per session.
func (p *networkPolicy) handleAttachedToTarget(params json.RawMessage) {
	var attached targetAttachedToTargetParams
	if err := json.Unmarshal(params, &attached); err != nil {
		slog.Debug("network policy: undecodable Target.attachedToTarget", "error", err)
		return
	}
	switch attached.TargetInfo.Type {
	case "page", "iframe", "worker", "shared_worker", "service_worker":
	default:
		// Browser-level and auxiliary targets do not load page requests.
		return
	}
	if attached.SessionID == "" {
		return
	}
	ctx := context.Background()
	if err := p.call(ctx, attached.SessionID, cdpCommandFetchEnable, fetchEnableParams{Patterns: []fetchPattern{{URLPattern: "*", RequestStage: "Request"}}}, nil); err != nil {
		slog.Debug("network policy: Fetch.enable on attached target failed", "error", err)
	}
	if err := p.call(ctx, attached.SessionID, cdpCommandNetworkEnable, struct{}{}, nil); err != nil {
		slog.Debug("network policy: Network.enable on attached target failed", "error", err)
	}
}

// record counts one blocked request, deduplicated per URL. The first deny
// reason for a URL is kept; it explains the block in the warnings[] payload.
func (p *networkPolicy) record(rawURL, resourceType, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.blocked[rawURL]
	if entry == nil {
		entry = &blockedEntry{url: rawURL, resourceType: resourceType, reason: reason}
		p.blocked[rawURL] = entry
	}
	entry.count++
	p.total++
}

// blockedRequests returns the sorted snapshot of denied requests.
func (p *networkPolicy) blockedRequests() []engine.BlockedRequest {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]engine.BlockedRequest, 0, len(p.blocked))
	for _, entry := range p.blocked {
		result = append(result, engine.BlockedRequest{URL: entry.url, ResourceType: entry.resourceType, Count: entry.count, Reason: entry.reason})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].URL < result[j].URL })
	return result
}

// describe returns the configured allowlist patterns for error and warning
// messages. The SSRF guard is reported separately when active.
func (p *networkPolicy) describe() string {
	if p == nil {
		return ""
	}
	var parts []string
	if patterns := p.allowlist.Patterns(); len(patterns) > 0 {
		parts = append(parts, strings.Join(patterns, ","))
	}
	if p.ssrf != nil && p.ssrf.Enabled() {
		parts = append(parts, "ssrf-guard")
	}
	return strings.Join(parts, ",")
}
