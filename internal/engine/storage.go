// Package engine defines protocol-neutral browser boundaries.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Cookie is the protocol-neutral shape of one HTTP cookie.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"` // -1 for session cookies
	Size     int64   `json:"size"`
	HTTPOnly bool    `json:"http_only"`
	Secure   bool    `json:"secure"`
	Session  bool    `json:"session"`
	SameSite string  `json:"same_site,omitempty"`
}

// CookieEngine is an optional engine extension for cookie management. The
// navigation service falls back to a structured error when it is not
// implemented, keeping injected engines small and testable.
type CookieEngine interface {
	Cookies(context.Context, Page, []string) ([]Cookie, error)
	SetCookie(context.Context, Page, Cookie, string) error
	DeleteCookies(context.Context, Page, string, string) error
}

// StorageKind identifies the two web-storage namespaces.
type StorageKind string

const (
	StorageLocal   StorageKind = "local"
	StorageSession StorageKind = "session"
)

// StorageService implements per-origin web-storage access through Runtime
// evaluation. Storage is inherently origin-scoped: the evaluation runs in the
// page context, so keys can never leak across origins.
type StorageService struct {
	engine Engine
	page   Page
}

// NewStorageService creates a storage controller for one page.
func NewStorageService(browser Engine, page Page) *StorageService {
	return &StorageService{engine: browser, page: page}
}

// StorageItems returns all key/value pairs of one storage namespace for the
// page's current origin.
func (s *StorageService) StorageItems(ctx context.Context, kind StorageKind) (map[string]string, error) {
	if err := validateStorageKind(kind); err != nil {
		return nil, err
	}
	expression := fmt.Sprintf(`(function(){
		const s = window.%s;
		const out = {};
		for (let i = 0; i < s.length; i++) { const k = s.key(i); out[k] = s.getItem(k); }
		return {origin: location.origin, items: out};
	})()`, storageObject(kind))
	result, err := s.engine.Evaluate(ctx, s.page, expression)
	if err != nil {
		return nil, fmt.Errorf("read %s storage: %w", kind, err)
	}
	if result.ExceptionText != "" {
		return nil, errors.New(result.ExceptionText)
	}
	var payload struct {
		Origin string            `json:"origin"`
		Items  map[string]string `json:"items"`
	}
	if err := json.Unmarshal(result.Value, &payload); err != nil {
		return nil, fmt.Errorf("decode %s storage: %w", kind, err)
	}
	if payload.Items == nil {
		payload.Items = map[string]string{}
	}
	return payload.Items, nil
}

// StorageOrigin returns the page's current origin without touching storage.
func (s *StorageService) StorageOrigin(ctx context.Context) (string, error) {
	result, err := s.engine.Evaluate(ctx, s.page, `location.origin`)
	if err != nil {
		return "", err
	}
	if result.ExceptionText != "" {
		return "", errors.New(result.ExceptionText)
	}
	var origin string
	if err := json.Unmarshal(result.Value, &origin); err != nil {
		return "", fmt.Errorf("decode origin: %w", err)
	}
	return origin, nil
}

// SetStorageItem writes one key of one storage namespace for the page's
// current origin. Empty values are allowed; the key itself must be non-empty.
func (s *StorageService) SetStorageItem(ctx context.Context, kind StorageKind, key, value string) error {
	if err := validateStorageKind(kind); err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("storage key is required")
	}
	encodedKey, _ := json.Marshal(key)
	encodedValue, _ := json.Marshal(value)
	expression := fmt.Sprintf(`(function(){ const s = window.%s; s.setItem(%s, %s); return true; })()`,
		storageObject(kind), encodedKey, encodedValue)
	result, err := s.engine.Evaluate(ctx, s.page, expression)
	if err != nil {
		return fmt.Errorf("set %s storage %q: %w", kind, key, err)
	}
	if result.ExceptionText != "" {
		return fmt.Errorf("set %s storage %q: %s", kind, key, result.ExceptionText)
	}
	return nil
}

// ClearStorage removes all keys of one storage namespace for the page's
// current origin.
func (s *StorageService) ClearStorage(ctx context.Context, kind StorageKind) error {
	if err := validateStorageKind(kind); err != nil {
		return err
	}
	expression := fmt.Sprintf(`(function(){ window.%s.clear(); return true; })()`, storageObject(kind))
	result, err := s.engine.Evaluate(ctx, s.page, expression)
	if err != nil {
		return fmt.Errorf("clear %s storage: %w", kind, err)
	}
	if result.ExceptionText != "" {
		return fmt.Errorf("clear %s storage: %s", kind, result.ExceptionText)
	}
	return nil
}

func validateStorageKind(kind StorageKind) error {
	switch kind {
	case StorageLocal, StorageSession:
		return nil
	default:
		return fmt.Errorf("invalid storage kind %q", kind)
	}
}

func storageObject(kind StorageKind) string {
	if kind == StorageSession {
		return "sessionStorage"
	}
	return "localStorage"
}
