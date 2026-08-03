package chrome

import (
	"context"
	"fmt"
	"time"

	cdproto "github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// Cookies returns all cookies visible to the given URLs. When urls is empty,
// Chrome returns cookies for the current page origin.
func (e *Engine) Cookies(ctx context.Context, page engine.Page, urls []string) ([]engine.Cookie, error) {
	params := network.GetCookiesParams{URLs: urls}
	var result struct {
		Cookies []network.Cookie `json:"cookies"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandNetworkGetCookies, params, &result); err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}
	cookies := make([]engine.Cookie, 0, len(result.Cookies))
	for _, c := range result.Cookies {
		cookies = append(cookies, engine.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  c.Expires,
			Size:     c.Size,
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			Session:  c.Session,
			SameSite: string(c.SameSite),
		})
	}
	return cookies, nil
}

// SetCookie writes one cookie scoped to url. Chrome derives domain/path from
// the URL unless the cookie carries explicit domain/path fields. Rejections
// (invalid domain, scheme mismatch) surface as CDP errors.
func (e *Engine) SetCookie(ctx context.Context, page engine.Page, cookie engine.Cookie, url string) error {
	param := &network.CookieParam{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Domain:   cookie.Domain,
		Path:     cookie.Path,
		HTTPOnly: cookie.HTTPOnly,
		Secure:   cookie.Secure,
		SameSite: network.CookieSameSite(cookie.SameSite),
		URL:      url,
	}
	if cookie.Expires > 0 {
		expires := cdp.TimeSinceEpoch(time.Unix(int64(cookie.Expires), 0))
		param.Expires = &expires
	}
	params := network.SetCookiesParams{
		Cookies: []*network.CookieParam{param},
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandNetworkSetCookies, params, nil); err != nil {
		return fmt.Errorf("set cookie %q: %w", cookie.Name, err)
	}
	return nil
}

// DeleteCookies removes one cookie by name from the given URL scope.
func (e *Engine) DeleteCookies(ctx context.Context, page engine.Page, name, url string) error {
	params := network.DeleteCookiesParams{Name: name, URL: url}
	if err := e.call(ctx, page.SessionID, cdproto.CommandNetworkDeleteCookies, params, nil); err != nil {
		return fmt.Errorf("delete cookie %q: %w", name, err)
	}
	return nil
}

var _ engine.CookieEngine = (*Engine)(nil)
