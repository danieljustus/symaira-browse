package chrome

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cdproto "github.com/chromedp/cdproto"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// Network capture (issue #59): requests are recorded per page session after
// Network.enable; routes mock matching requests via Fetch interception.
// Sensitive header values are masked before anything leaves the engine.

const maxNetworkRequests = 2000

// maskedHeaderKeys are never exposed verbatim in request lists or HAR.
var maskedHeaderKeys = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
}

// EnableNetworkCapture turns on Network domain capture for a page session
// (idempotent).
func (e *Engine) EnableNetworkCapture(ctx context.Context, page engine.Page) error {
	e.networkMu.Lock()
	enabled := e.networkEnabled[page.SessionID]
	e.networkMu.Unlock()
	if enabled {
		return nil
	}
	var result struct{}
	if err := e.call(ctx, page.SessionID, cdproto.CommandNetworkEnable, struct{}{}, &result); err != nil {
		return fmt.Errorf("enable network capture: %w", err)
	}
	e.networkMu.Lock()
	if e.networkEnabled == nil {
		e.networkEnabled = make(map[string]bool)
	}
	e.networkEnabled[page.SessionID] = true
	e.networkMu.Unlock()
	return nil
}

// Requests returns a copy of the session's captured requests.
func (e *Engine) Requests(page engine.Page) []engine.NetworkRequest {
	e.networkMu.Lock()
	defer e.networkMu.Unlock()
	return append([]engine.NetworkRequest(nil), e.networkRequests[page.SessionID]...)
}

// Request returns one captured request by id.
func (e *Engine) Request(page engine.Page, id string) (engine.NetworkRequest, bool) {
	e.networkMu.Lock()
	defer e.networkMu.Unlock()
	byID := e.networkByID[page.SessionID]
	request, ok := byID[id]
	return request, ok
}

// RouteRequests installs one mock route for the session. Fetch
// interception is only active while routes exist.
func (e *Engine) RouteRequests(ctx context.Context, page engine.Page, route engine.NetworkRoute) error {
	if strings.TrimSpace(route.Pattern) == "" {
		return fmt.Errorf("route pattern is required")
	}
	if route.Action != "abort" && route.Action != "mock" {
		return fmt.Errorf("route action must be %q or %q", "abort", "mock")
	}
	e.networkMu.Lock()
	if e.networkRoutes == nil {
		e.networkRoutes = make(map[string]map[string]engine.NetworkRoute)
	}
	if e.networkRoutes[page.SessionID] == nil {
		e.networkRoutes[page.SessionID] = make(map[string]engine.NetworkRoute)
	}
	first := len(e.networkRoutes[page.SessionID]) == 0
	e.networkRoutes[page.SessionID][route.Pattern] = route
	e.networkMu.Unlock()
	if !first {
		return nil
	}
	var result struct{}
	params := struct {
		Patterns []struct {
			URLPattern   string `json:"urlPattern"`
			RequestStage string `json:"requestStage"`
		} `json:"patterns"`
	}{Patterns: []struct {
		URLPattern   string `json:"urlPattern"`
		RequestStage string `json:"requestStage"`
	}{{URLPattern: "*", RequestStage: "Request"}}}
	return e.call(ctx, page.SessionID, cdproto.CommandFetchEnable, params, &result)
}

// UnrouteRequests removes one route (or all when pattern is empty) and
// disables Fetch interception once the last route is gone.
func (e *Engine) UnrouteRequests(ctx context.Context, page engine.Page, pattern string) (bool, error) {
	e.networkMu.Lock()
	routes := e.networkRoutes[page.SessionID]
	removed := false
	if pattern == "" {
		removed = len(routes) > 0
		delete(e.networkRoutes, page.SessionID)
	} else if routes != nil {
		_, removed = routes[pattern]
		delete(routes, pattern)
	}
	remaining := len(e.networkRoutes[page.SessionID])
	e.networkMu.Unlock()
	if remaining == 0 {
		var result struct{}
		if err := e.call(ctx, page.SessionID, cdproto.CommandFetchDisable, struct{}{}, &result); err != nil {
			return removed, fmt.Errorf("disable fetch interception: %w", err)
		}
	}
	return removed, nil
}

// HAR builds a Chrome-DevTools-loadable HAR 1.2 document from the captured
// requests (issue #59 AC). Content "all" includes response bodies.
func (e *Engine) HAR(ctx context.Context, page engine.Page, options engine.HAROptions) ([]byte, error) {
	if options.Content == "" {
		options.Content = "none"
	}
	e.networkMu.Lock()
	requests := append([]engine.NetworkRequest(nil), e.networkRequests[page.SessionID]...)
	bodies := map[string]string{}
	for id, body := range e.networkBodies[page.SessionID] {
		bodies[id] = body
	}
	e.networkMu.Unlock()

	type harEntry struct {
		StartedDateTime string `json:"startedDateTime"`
		Time            int64  `json:"time"`
		Request         struct {
			Method      string      `json:"method"`
			URL         string      `json:"url"`
			HTTPVersion string      `json:"httpVersion"`
			Headers     []harHeader `json:"headers"`
			QueryString []any       `json:"queryString"`
			Cookies     []any       `json:"cookies"`
			HeadersSize int         `json:"headersSize"`
			BodySize    int         `json:"bodySize"`
		} `json:"request"`
		Response struct {
			Status      int         `json:"status"`
			StatusText  string      `json:"statusText"`
			HTTPVersion string      `json:"httpVersion"`
			Headers     []harHeader `json:"headers"`
			Cookies     []any       `json:"cookies"`
			Content     struct {
				Size     int    `json:"size"`
				MimeType string `json:"mimeType"`
				Text     string `json:"text,omitempty"`
			} `json:"content"`
			RedirectURL string `json:"redirectURL"`
			HeadersSize int    `json:"headersSize"`
			BodySize    int    `json:"bodySize"`
		} `json:"response"`
		Cache   map[string]any `json:"cache"`
		Timings map[string]any `json:"timings"`
	}
	entries := make([]harEntry, 0, len(requests))
	for _, request := range requests {
		var entry harEntry
		entry.StartedDateTime = request.StartedDateTime.UTC().Format(time.RFC3339Nano)
		entry.Time = 0
		entry.Request.Method = request.Method
		entry.Request.URL = request.URL
		entry.Request.HTTPVersion = "HTTP/1.1"
		entry.Request.Headers = headersToHAR(request.RequestHeaders)
		entry.Request.HeadersSize = -1
		entry.Request.BodySize = -1
		entry.Response.Status = request.Status
		entry.Response.StatusText = request.StatusText
		entry.Response.HTTPVersion = "HTTP/1.1"
		entry.Response.Headers = headersToHAR(request.ResponseHeaders)
		entry.Response.Content.MimeType = request.MimeType
		entry.Response.Content.Size = int(request.EncodedBodySize)
		if options.Content == "all" {
			if body, ok := bodies[request.ID]; ok {
				entry.Response.Content.Text = body
			}
		}
		entry.Response.HeadersSize = -1
		entry.Response.BodySize = int(request.EncodedBodySize)
		entry.Response.RedirectURL = ""
		entry.Cache = map[string]any{}
		entry.Timings = map[string]any{"send": 0, "wait": 0, "receive": 0}
		entries = append(entries, entry)
	}
	document := map[string]any{
		"log": map[string]any{
			"version": "1.2",
			"creator": map[string]any{"name": "symbrowse", "version": "0.1.0"},
			"pages": []any{
				map[string]any{
					"startedDateTime": time.Now().UTC().Format(time.RFC3339Nano),
					"id":              "page_1",
					"title":           "",
					"pageTimings":     map[string]any{},
				},
			},
			"entries": entries,
		},
	}
	return json.MarshalIndent(document, "", "  ")
}

// harHeader is one HAR header pair.
type harHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func headersToHAR(headers map[string]string) []harHeader {
	if len(headers) == 0 {
		return []harHeader{}
	}
	result := make([]harHeader, 0, len(headers))
	for name, value := range headers {
		result = append(result, harHeader{Name: name, Value: value})
	}
	return result
}

// ---- event capture -------------------------------------------------------

func (e *Engine) recordRequestWillBeSent(sessionID string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"request"`
		Type      string  `json:"type"`
		Timestamp float64 `json:"timestamp"`
		WallTime  float64 `json:"wallTime"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	entry := engine.NetworkRequest{
		ID:              event.RequestID,
		URL:             event.Request.URL,
		Method:          event.Request.Method,
		Type:            event.Type,
		StartedDateTime: time.Unix(int64(event.WallTime), 0).UTC(),
		RequestHeaders:  maskHeaders(event.Request.Headers),
	}
	e.networkMu.Lock()
	if e.networkRequests == nil {
		e.networkRequests = make(map[string][]engine.NetworkRequest)
	}
	e.networkRequests[sessionID] = appendBoundedRequests(e.networkRequests[sessionID], entry)
	if e.networkByID == nil {
		e.networkByID = make(map[string]map[string]engine.NetworkRequest)
	}
	if e.networkByID[sessionID] == nil {
		e.networkByID[sessionID] = make(map[string]engine.NetworkRequest)
	}
	e.networkByID[sessionID][event.RequestID] = entry
	e.networkMu.Unlock()
}

func (e *Engine) recordResponseReceived(sessionID string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
		Response  struct {
			Status      int               `json:"status"`
			StatusText  string            `json:"statusText"`
			Headers     map[string]string `json:"headers"`
			MimeType    string            `json:"mimeType"`
			EncodedData int64             `json:"encodedDataLength"`
		} `json:"response"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	e.networkMu.Lock()
	defer e.networkMu.Unlock()
	byID := e.networkByID[sessionID]
	entry, ok := byID[event.RequestID]
	if !ok {
		return
	}
	entry.Status = event.Response.Status
	entry.StatusText = event.Response.StatusText
	entry.MimeType = event.Response.MimeType
	entry.ResponseHeaders = maskHeaders(event.Response.Headers)
	entry.EncodedBodySize = event.Response.EncodedData
	byID[event.RequestID] = entry
	e.replaceRequest(sessionID, entry)
}

func (e *Engine) recordLoadingFinished(sessionID string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	e.networkMu.Lock()
	entry, ok := e.networkByID[sessionID][event.RequestID]
	if ok {
		entry.Finished = true
		e.networkByID[sessionID][event.RequestID] = entry
		e.replaceRequest(sessionID, entry)
	}
	e.networkMu.Unlock()
}

func (e *Engine) recordLoadingFailed(sessionID string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
		ErrorText string `json:"errorText"`
		Canceled  bool   `json:"canceled"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	e.networkMu.Lock()
	entry, ok := e.networkByID[sessionID][event.RequestID]
	if ok {
		entry.Failed = event.ErrorText
		entry.Finished = true
		e.networkByID[sessionID][event.RequestID] = entry
		e.replaceRequest(sessionID, entry)
	}
	e.networkMu.Unlock()
}

// replaceRequest updates the entry in the per-session ordered list.
func (e *Engine) replaceRequest(sessionID string, entry engine.NetworkRequest) {
	requests := e.networkRequests[sessionID]
	for i := range requests {
		if requests[i].ID == entry.ID {
			requests[i] = entry
			return
		}
	}
}

func (e *Engine) recordRequestPaused(sessionID string, params json.RawMessage) {
	var event struct {
		RequestID string `json:"requestId"`
		Request   struct {
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"request"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	// Resolve the route snapshot outside the read loop; the CDP answer is
	// sent from a separate goroutine so the read loop never blocks.
	e.networkMu.Lock()
	routes := map[string]engine.NetworkRoute{}
	for pattern, route := range e.networkRoutes[sessionID] {
		routes[pattern] = route
	}
	e.networkMu.Unlock()
	go e.answerPausedRequest(ctxForRoute(), sessionID, event.RequestID, event.Request.URL, routes)
}

// answerPausedRequest continues, fulfills or fails one paused request.
func (e *Engine) answerPausedRequest(ctx context.Context, sessionID, requestID, url string, routes map[string]engine.NetworkRoute) {
	var route *engine.NetworkRoute
	for pattern, candidate := range routes {
		if patternMatchesRoute(pattern, url) {
			copy := candidate
			route = &copy
			break
		}
	}
	if route == nil {
		// No mock route matches. When the network policy (allowlist/SSRF)
		// is active, it already answered this paused request (continue or
		// fail); responding again would double-answer the same requestId.
		// Only auto-continue when no policy owns the interception.
		e.mu.Lock()
		policyActive := e.policy != nil && e.policy.active()
		e.mu.Unlock()
		if policyActive {
			return
		}
		var result struct{}
		_ = e.call(ctx, sessionID, cdproto.CommandFetchContinueRequest, struct {
			RequestID string `json:"requestId"`
		}{requestID}, &result)
		return
	}
	if route.Action == "abort" {
		var result struct{}
		_ = e.call(ctx, sessionID, cdproto.CommandFetchFailRequest, struct {
			RequestID   string `json:"requestId"`
			ErrorReason string `json:"errorReason"`
		}{requestID, "BlockedByClient"}, &result)
		return
	}
	body := ""
	if len(route.Body) > 0 {
		body = base64.StdEncoding.EncodeToString(route.Body)
	}
	headers := []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{}
	if route.ContentType != "" {
		headers = append(headers, struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{Name: "Content-Type", Value: route.ContentType})
	}
	status := route.Status
	if status == 0 {
		status = 200
	}
	var result struct{}
	_ = e.call(ctx, sessionID, cdproto.CommandFetchFulfillRequest, struct {
		RequestID       string `json:"requestId"`
		ResponseCode    int    `json:"responseCode"`
		ResponsePhrase  string `json:"responsePhrase,omitempty"`
		ResponseHeaders []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"responseHeaders,omitempty"`
		Body string `json:"body,omitempty"`
	}{requestID, status, "", headers, body}, &result)
}

func patternMatchesRoute(pattern, url string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(url, strings.TrimSuffix(pattern, "*"))
	}
	return url == pattern
}

func maskHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	masked := make(map[string]string, len(headers))
	for name, value := range headers {
		if maskedHeaderKeys[strings.ToLower(name)] {
			masked[name] = "[redacted]"
		} else {
			masked[name] = value
		}
	}
	return masked
}

func appendBoundedRequests(requests []engine.NetworkRequest, entry engine.NetworkRequest) []engine.NetworkRequest {
	if len(requests) >= maxNetworkRequests {
		requests = append([]engine.NetworkRequest(nil), requests[len(requests)-maxNetworkRequests+1:]...)
	}
	return append(requests, entry)
}

// ctxForRoute is the context for route answers; the engine request timeout
// bounds the call via the engine's per-request timeout wrapper.
func ctxForRoute() context.Context {
	return context.Background()
}
