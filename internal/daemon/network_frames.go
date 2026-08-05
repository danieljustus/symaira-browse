package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

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
