package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

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
