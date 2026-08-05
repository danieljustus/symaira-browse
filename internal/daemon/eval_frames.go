package daemon

import (
	"context"
	"errors"
	"strings"
)

// handleEvalFrame executes one JavaScript expression on the active page
// (issue #60). The result is returned protocol-neutral; exceptions carry
// their text.
func (r *NavigationRuntime) handleEvalFrame(ctx context.Context, frame Frame) (any, error) {
	var request struct {
		Expression string `json:"expression"`
	}
	if err := decodeArgs(frame, &request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Expression) == "" {
		return nil, errors.New("eval requires a non-empty expression")
	}
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	result, err := service.Evaluate(ctx, request.Expression)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type":           result.Type,
		"value":          result.Value,
		"description":    result.Description,
		"exception_text": result.ExceptionText,
	}, nil
}
