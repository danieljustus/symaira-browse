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

// Clip is a capture region in CSS viewport pixels (issue #16). It maps to the
// CDP Page.captureScreenshot clip parameter; the values come from an element
// bounding box resolved on the page.
type Clip struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ScreenshotOptions controls one capture (issue #16, B-12). Format is "png"
// (default) or "jpeg"; Quality only applies to jpeg. FullPage captures the
// whole scrollable page instead of the viewport; Clip restricts the capture
// to an element bounding box. FullPage and Clip are mutually exclusive.
type ScreenshotOptions struct {
	Format   string
	Quality  int
	FullPage bool
	Clip     *Clip
}

// ScreenshotOptionsEngine is an optional engine extension for captures with
// format, quality, full-page and clip options.
type ScreenshotOptionsEngine interface {
	ScreenshotWithOptions(context.Context, Page, ScreenshotOptions) ([]byte, error)
}

// Screenshot captures a PNG of the current page viewport.
func (s *NavigationService) Screenshot(ctx context.Context) ([]byte, error) {
	return s.ScreenshotWithOptions(ctx, ScreenshotOptions{})
}

// ScreenshotWithOptions captures the page according to opts. Engines without
// option support fall back to a plain viewport capture when no options are
// requested and fail with a capability error otherwise.
func (s *NavigationService) ScreenshotWithOptions(ctx context.Context, opts ScreenshotOptions) ([]byte, error) {
	if optionsEngine, ok := s.engine.(ScreenshotOptionsEngine); ok {
		return optionsEngine.ScreenshotWithOptions(ctx, s.page, opts)
	}
	if plainEngine, ok := s.engine.(ScreenshotEngine); ok {
		if opts.Format == "" && opts.Quality == 0 && !opts.FullPage && opts.Clip == nil {
			return plainEngine.Screenshot(ctx, s.page)
		}
		return nil, fmt.Errorf("browser engine does not support screenshot options (format, full page, clip)")
	}
	return nil, fmt.Errorf("browser engine does not support screenshots")
}
