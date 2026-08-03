package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeStorageEngine records evaluated expressions and returns canned results.
// Expressions are matched by substring so tests stay robust against exact
// whitespace in generated JavaScript.
type fakeStorageEngine struct {
	results map[string]EvaluationResult
	last    []string
}

func (f *fakeStorageEngine) Launch(context.Context) error { return nil }
func (f *fakeStorageEngine) NewContext(context.Context) (Context, error) {
	return Context{ID: "ctx"}, nil
}
func (f *fakeStorageEngine) NewPage(context.Context, Context, string) (Page, error) {
	return Page{ID: "page"}, nil
}
func (f *fakeStorageEngine) Navigate(context.Context, Page, string) (NavigationResult, error) {
	return NavigationResult{}, nil
}
func (f *fakeStorageEngine) Evaluate(_ context.Context, _ Page, expression string) (EvaluationResult, error) {
	f.last = append(f.last, expression)
	// Longest needles first: the origin probe is a substring of the storage
	// expression, so exact probes must win over broader ones.
	for _, needle := range sortedNeedles(f.results) {
		if strings.Contains(expression, needle) {
			return f.results[needle], nil
		}
	}
	return EvaluationResult{}, errors.New("no canned result for expression")
}

func sortedNeedles(results map[string]EvaluationResult) []string {
	needles := make([]string, 0, len(results))
	for needle := range results {
		needles = append(needles, needle)
	}
	for i := 1; i < len(needles); i++ {
		for j := i; j > 0 && len(needles[j]) > len(needles[j-1]); j-- {
			needles[j], needles[j-1] = needles[j-1], needles[j]
		}
	}
	return needles
}
func (f *fakeStorageEngine) AXTree(context.Context, Page) ([]AXNode, error)   { return nil, nil }
func (f *fakeStorageEngine) Screenshot(context.Context, Page) ([]byte, error) { return nil, nil }
func (f *fakeStorageEngine) Close() error                                     { return nil }

// fakeStorageEngine implements CookieEngine so CaptureCookiesAndStorage can
// run against it in tests.
func (f *fakeStorageEngine) Cookies(context.Context, Page, []string) ([]Cookie, error) {
	return nil, nil
}
func (f *fakeStorageEngine) SetCookie(context.Context, Page, Cookie, string) error { return nil }
func (f *fakeStorageEngine) DeleteCookies(context.Context, Page, string, string) error {
	return nil
}

func evalResult(value string) EvaluationResult {
	return EvaluationResult{Value: json.RawMessage(value)}
}

func TestStorageItemsAreOriginScoped(t *testing.T) {
	fake := &fakeStorageEngine{results: map[string]EvaluationResult{
		"window.localStorage": evalResult(`{"origin":"https://example.com","items":{"session":"abc"}}`),
		"location.origin":     evalResult(`"https://example.com"`),
	}}
	service := NewStorageService(fake, Page{ID: "page"})
	items, err := service.StorageItems(context.Background(), StorageLocal)
	if err != nil {
		t.Fatal(err)
	}
	if items["session"] != "abc" {
		t.Fatalf("items = %#v", items)
	}
	origin, err := service.StorageOrigin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if origin != "https://example.com" {
		t.Fatalf("origin = %q", origin)
	}
}

func TestStorageSetAndClearRoundTrip(t *testing.T) {
	fake := &fakeStorageEngine{results: map[string]EvaluationResult{
		"window.sessionStorage": evalResult(`true`),
		"window.localStorage":   evalResult(`true`),
	}}
	service := NewStorageService(fake, Page{ID: "page"})
	if err := service.SetStorageItem(context.Background(), StorageSession, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := service.ClearStorage(context.Background(), StorageLocal); err != nil {
		t.Fatal(err)
	}
	if len(fake.last) != 2 {
		t.Fatalf("expected 2 evaluations, got %d", len(fake.last))
	}
}

func TestStorageValidationRejectsBadKindAndEmptyKey(t *testing.T) {
	fake := &fakeStorageEngine{}
	service := NewStorageService(fake, Page{ID: "page"})
	if _, err := service.StorageItems(context.Background(), StorageKind("cookie")); err == nil {
		t.Fatal("invalid storage kind accepted")
	}
	if err := service.SetStorageItem(context.Background(), StorageLocal, " ", "v"); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestStorageExceptionSurfaces(t *testing.T) {
	fake := &fakeStorageEngine{results: map[string]EvaluationResult{
		"window.localStorage": {ExceptionText: "SecurityError: storage disabled"},
	}}
	service := NewStorageService(fake, Page{ID: "page"})
	if _, err := service.StorageItems(context.Background(), StorageLocal); err == nil {
		t.Fatal("exception swallowed")
	}
}

func TestCaptureAndRestoreStateKeepsOriginsSeparate(t *testing.T) {
	// Capture: two different origins must never merge into one storage map.
	// The capture path reads the page origin once and keys storage by it.
	fake := &fakeStorageEngine{results: map[string]EvaluationResult{
		"location.origin":       evalResult(`"https://app.example.com"`),
		"window.localStorage":   evalResult(`{"origin":"https://app.example.com","items":{"token":"t-1"}}`),
		"window.sessionStorage": evalResult(`{"origin":"https://app.example.com","items":{}}`),
	}}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{})
	_, storage, err := service.CaptureCookiesAndStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(storage) != 1 {
		t.Fatalf("storage = %#v", storage)
	}
	if _, ok := storage["https://app.example.com"]; !ok {
		t.Fatalf("storage keyed by wrong origin: %#v", storage)
	}
	if _, ok := storage["https://other.example.com"]; ok {
		t.Fatal("foreign origin leaked into state")
	}
}

func TestCookieURLReconstructsScope(t *testing.T) {
	tests := []struct {
		name   string
		cookie Cookie
		want   string
	}{
		{"secure subdomain", Cookie{Domain: ".example.com", Path: "/app", Secure: true}, "https://example.com/app"},
		{"plain domain", Cookie{Domain: "example.com", Path: ""}, "http://example.com/"},
		{"empty domain", Cookie{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cookieURL(tt.cookie); got != tt.want {
				t.Fatalf("cookieURL = %q, want %q", got, tt.want)
			}
		})
	}
}
