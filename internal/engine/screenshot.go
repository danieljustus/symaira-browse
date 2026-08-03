package engine

import (
	"context"
	"fmt"
)

// ScreenshotEngine is an optional engine extension for capturing page
// screenshots. Engines without camera support fail with a capability error.
type ScreenshotEngine interface {
	Screenshot(context.Context, Page) ([]byte, error)
}

// Screenshot captures a PNG of the current page. The engine's Screenshot
// method is already part of the core Engine interface, so this is a thin
// service-level wrapper for the daemon protocol.
func (s *NavigationService) Screenshot(ctx context.Context) ([]byte, error) {
	engine, ok := s.engine.(ScreenshotEngine)
	if !ok {
		return nil, fmt.Errorf("browser engine does not support screenshots")
	}
	return engine.Screenshot(ctx, s.page)
}
