package daemon

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// handleNavigationFrame serves the navigation commands: open, goto, back,
// forward, reload and wait.
func (r *NavigationRuntime) handleNavigationFrame(ctx context.Context, frame Frame) (any, error) {
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
	default:
		return nil, fmt.Errorf("unknown navigation command %q", frame.Cmd)
	}
}
