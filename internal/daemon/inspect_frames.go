package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// handleInspectFrame serves the inspection commands: get.* / is.* element
// inspection, the read command and find.
func (r *NavigationRuntime) handleInspectFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	switch frame.Cmd {
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
	default:
		return nil, fmt.Errorf("unknown inspection command %q", frame.Cmd)
	}
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
