package chrome

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// DisableScripts implements engine.ScriptDisabler: it disables JavaScript
// execution for one page session so a probe load renders what a static
// fetch would see (the --engine-hint comparison, issue #35).
func (e *Engine) DisableScripts(ctx context.Context, page engine.Page) error {
	params := struct {
		Value bool `json:"value"`
	}{Value: true}
	if err := e.call(ctx, page.SessionID, "Emulation.setScriptExecutionDisabled", params, nil); err != nil {
		return fmt.Errorf("disable script execution: %w", err)
	}
	return nil
}

var _ engine.ScriptDisabler = (*Engine)(nil)
