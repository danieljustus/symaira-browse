package chrome

import (
	"context"
	"encoding/json"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/axe"
)

// RunA11y evaluates axe-core inside the page and returns the raw audit
// payload. The script is injected only when axe is not already present.
func (e *Engine) RunA11y(ctx context.Context, page engine.Page, options engine.A11yOptions) (json.RawMessage, error) {
	script, err := axe.RunScript(options.Tags, options.Selector)
	if err != nil {
		return nil, err
	}
	result, err := e.Evaluate(ctx, page, script)
	if err != nil {
		return nil, err
	}
	if result.ExceptionText != "" {
		return nil, &engine.InspectionError{Code: "a11y_failed", Message: result.ExceptionText}
	}
	if len(result.Value) == 0 || string(result.Value) == "null" {
		return nil, &engine.InspectionError{Code: "a11y_failed", Message: "axe-core returned no result"}
	}
	return result.Value, nil
}

var _ engine.A11yAuditor = (*Engine)(nil)
