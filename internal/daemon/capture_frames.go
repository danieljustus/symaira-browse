package daemon

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// handleCaptureFrame serves the page capture commands: snapshot, a11y
// audit and screenshot.
func (r *NavigationRuntime) handleCaptureFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	switch frame.Cmd {
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
	case "a11y":
		var options engine.A11yOptions
		if err := decodeOptionalArgs(frame, &options); err != nil {
			return nil, err
		}
		result, err := service.Audit(ctx, options)
		if err != nil {
			return nil, err
		}
		return result, nil
	case "screenshot":
		data, err := service.Screenshot(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"png": data}, nil
	default:
		return nil, fmt.Errorf("unknown capture command %q", frame.Cmd)
	}
}
