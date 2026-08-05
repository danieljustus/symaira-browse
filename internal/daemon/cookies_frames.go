package daemon

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// handleCookiesFrame serves cookies.list, cookies.set and cookies.clear.
func (r *NavigationRuntime) handleCookiesFrame(ctx context.Context, frame Frame) (any, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, err
	}
	switch frame.Cmd {
	case "cookies.list":
		var request struct {
			URLs []string `json:"urls,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		cookies, err := service.CookiesForURLs(ctx, request.URLs)
		if err != nil {
			return nil, err
		}
		origin, originErr := service.Origin(ctx)
		if originErr != nil {
			origin = ""
		}
		return map[string]any{"origin": origin, "cookies": cookies}, nil
	case "cookies.set":
		var request struct {
			Cookie engine.Cookie `json:"cookie"`
			URL    string        `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.SetCookie(ctx, request.Cookie, request.URL); err != nil {
			return nil, err
		}
		return map[string]any{"set": request.Cookie.Name}, nil
	case "cookies.clear":
		var request struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, err
		}
		if err := service.DeleteCookie(ctx, request.Name, request.URL); err != nil {
			return nil, err
		}
		return map[string]any{"cleared": request.Name}, nil
	default:
		return nil, fmt.Errorf("unknown cookies command %q", frame.Cmd)
	}
}
