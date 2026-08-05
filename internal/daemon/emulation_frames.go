package daemon

import (
	"context"
	"fmt"
)

// handleEmulationFrame serves the set.* emulation commands: viewport,
// device, geo, offline, headers, media and user-agent.
func (r *NavigationRuntime) handleEmulationFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	switch frame.Cmd {
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
		return nil, fmt.Errorf("unknown emulation command %q", frame.Cmd)
	}
}
