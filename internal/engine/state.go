package engine

import (
	"context"
	"errors"
	"fmt"
)

// Cookies returns the cookies visible to the current page origin.
func (s *NavigationService) Cookies(ctx context.Context) ([]Cookie, error) {
	return s.CookiesForURLs(ctx, nil)
}

// CookiesForURLs returns cookies visible to the given URLs (or the page origin
// when urls is empty).
func (s *NavigationService) CookiesForURLs(ctx context.Context, urls []string) ([]Cookie, error) {
	provider, ok := s.engine.(CookieEngine)
	if !ok {
		return nil, errors.New("cookie management is not supported by this engine")
	}
	return provider.Cookies(ctx, s.page, urls)
}

// SetCookie writes one cookie scoped to url.
func (s *NavigationService) SetCookie(ctx context.Context, cookie Cookie, url string) error {
	provider, ok := s.engine.(CookieEngine)
	if !ok {
		return errors.New("cookie management is not supported by this engine")
	}
	if cookie.Name == "" {
		return errors.New("cookie name is required")
	}
	return provider.SetCookie(ctx, s.page, cookie, url)
}

// DeleteCookie removes one cookie by name from the given URL scope.
func (s *NavigationService) DeleteCookie(ctx context.Context, name, url string) error {
	provider, ok := s.engine.(CookieEngine)
	if !ok {
		return errors.New("cookie management is not supported by this engine")
	}
	if name == "" {
		return errors.New("cookie name is required")
	}
	return provider.DeleteCookies(ctx, s.page, name, url)
}

// Storage returns a per-origin web-storage controller for this page.
func (s *NavigationService) Storage() *StorageService {
	return NewStorageService(s.engine, s.page)
}

// Origin returns the current page origin (scheme://host[:port]).
func (s *NavigationService) Origin(ctx context.Context) (string, error) {
	return s.Storage().StorageOrigin(ctx)
}

// RestoreCookiesAndStorage applies a saved session state (cookies and
// per-origin web storage) to the current page. Cookies are set through the CDP
// Network domain; storage is written in the page context for the current
// origin. Unknown origins in the state are skipped with a warning.
func (s *NavigationService) RestoreCookiesAndStorage(ctx context.Context, cookies []Cookie, storage map[string]map[StorageKind]map[string]string) ([]string, error) {
	var warnings []string
	for _, cookie := range cookies {
		if err := s.SetCookie(ctx, cookie, cookieURL(cookie)); err != nil {
			warnings = append(warnings, fmt.Sprintf("cookie %q: %v", cookie.Name, err))
		}
	}
	if len(storage) == 0 {
		return warnings, nil
	}
	origin, err := s.Origin(ctx)
	if err != nil {
		return warnings, err
	}
	items, ok := storage[origin]
	if !ok {
		return warnings, nil
	}
	storageService := s.Storage()
	for kind, pairs := range items {
		for key, value := range pairs {
			if err := storageService.SetStorageItem(ctx, kind, key, value); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s storage %q: %v", kind, key, err))
			}
		}
	}
	return warnings, nil
}

// CaptureCookiesAndStorage reads the current session state for persistence.
// The returned map is keyed by origin so storage never leaks across origins.
func (s *NavigationService) CaptureCookiesAndStorage(ctx context.Context) ([]Cookie, map[string]map[StorageKind]map[string]string, error) {
	cookies, err := s.Cookies(ctx)
	if err != nil {
		return nil, nil, err
	}
	origin, err := s.Origin(ctx)
	if err != nil {
		return nil, nil, err
	}
	storageService := s.Storage()
	local, err := storageService.StorageItems(ctx, StorageLocal)
	if err != nil {
		return nil, nil, err
	}
	session, err := storageService.StorageItems(ctx, StorageSession)
	if err != nil {
		return nil, nil, err
	}
	storage := map[string]map[StorageKind]map[string]string{}
	if len(local) > 0 || len(session) > 0 {
		storage[origin] = map[StorageKind]map[string]string{
			StorageLocal:   local,
			StorageSession: session,
		}
	}
	return cookies, storage, nil
}

// cookieURL reconstructs a URL scope for a cookie from its domain and path so
// that persisted cookies can be restored without the original page URL.
func cookieURL(cookie Cookie) string {
	domain := cookie.Domain
	if domain == "" {
		return ""
	}
	if len(domain) > 0 && domain[0] == '.' {
		domain = domain[1:]
	}
	scheme := "http"
	if cookie.Secure {
		scheme = "https"
	}
	path := cookie.Path
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("%s://%s%s", scheme, domain, path)
}
