package testserver

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIssue19ServerStartsInProcessAndCloses(t *testing.T) {
	first := New()
	second := New()
	defer first.Close()
	defer second.Close()

	if first.URL == "" || first.BaseURL == "" || first.URL != first.BaseURL {
		t.Fatalf("server URL fields are inconsistent: URL=%q BaseURL=%q", first.URL, first.BaseURL)
	}
	firstURL, err := url.Parse(first.URL)
	if err != nil {
		t.Fatalf("parse first server URL: %v", err)
	}
	secondURL, err := url.Parse(second.URL)
	if err != nil {
		t.Fatalf("parse second server URL: %v", err)
	}
	if firstURL.Host == secondURL.Host {
		t.Fatalf("two live servers reused the same address: %q", firstURL.Host)
	}
	if firstURL.Port() == "" || firstURL.Port() == "80" || firstURL.Port() == "443" {
		t.Fatalf("server did not use an ephemeral test port: %q", firstURL.Host)
	}

	response, err := http.Get(first.URLFor(Static))
	if err != nil {
		t.Fatalf("request against in-process server: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("static fixture status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	first.Close()
	if _, err := http.Get(first.URLFor(Static)); err == nil {
		t.Fatal("request succeeded after server Close")
	}
}

func TestIssue19AllRegisteredFixtures(t *testing.T) {
	server := New(t)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	for _, route := range Routes() {
		route := route
		t.Run(string(route.Fixture), func(t *testing.T) {
			response, err := client.Get(server.BaseURL + route.Path)
			if err != nil {
				t.Fatalf("GET %s: %v", route.Path, err)
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read %s: %v", route.Path, err)
			}

			wantStatus := http.StatusOK
			switch route.Fixture {
			case RedirectLoop:
				wantStatus = http.StatusFound
			case NotFound:
				wantStatus = http.StatusNotFound
			case InternalServerError:
				wantStatus = http.StatusInternalServerError
			}
			if response.StatusCode != wantStatus {
				t.Fatalf("%s status = %d, want %d", route.Path, response.StatusCode, wantStatus)
			}
			if len(body) == 0 {
				t.Fatalf("%s returned an empty body", route.Path)
			}
		})
	}
}

func TestIssue19FixtureContentAndBehaviors(t *testing.T) {
	server := New(t)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	tests := []struct {
		fixture Fixture
		markers []string
	}{
		{Static, []string{"Static fixture", "static-content"}},
		{Form, []string{"name=\"text\"", "name=\"select\"", "type=\"checkbox\"", "type=\"radio\"", "type=\"file\""}},
		{SPA, []string{"data-hydrated=\"false\"", "setTimeout", "Hydrated application content"}},
		{Overlay, []string{"role=\"dialog\"", "underlying-button", "close-modal"}},
		{Iframe, []string{"src=\"/iframe/child\"", "child-frame"}},
		{ShadowDOM, []string{"attachShadow", "shadow-content", "mode: 'open'"}},
		{HiddenText, []string{"display-none", "visibility-hidden", "font-size-zero", "opacity-zero", "offscreen"}},
		{AriaLabelMismatch, []string{"aria-label=\"Delete account\"", ">Continue<"}},
	}

	for _, test := range tests {
		t.Run(string(test.fixture), func(t *testing.T) {
			response, err := client.Get(server.URLFor(test.fixture))
			if err != nil {
				t.Fatalf("GET fixture: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			content := string(body)
			for _, marker := range test.markers {
				if !strings.Contains(content, marker) {
					t.Errorf("fixture %s is missing marker %q", test.fixture, marker)
				}
			}
		})
	}

	response, err := client.Get(server.URLFor(Iframe) + "")
	if err != nil {
		t.Fatalf("GET iframe parent: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("iframe parent status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	child, err := client.Get(server.BaseURL + "/iframe/child")
	if err != nil {
		t.Fatalf("GET iframe child: %v", err)
	}
	_ = child.Body.Close()
	if child.StatusCode != http.StatusOK {
		t.Fatalf("iframe child status = %d, want %d", child.StatusCode, http.StatusOK)
	}
	grandchild, err := client.Get(server.BaseURL + "/iframe/grandchild")
	if err != nil {
		t.Fatalf("GET iframe grandchild: %v", err)
	}
	_ = grandchild.Body.Close()
	if grandchild.StatusCode != http.StatusOK {
		t.Fatalf("iframe grandchild status = %d, want %d", grandchild.StatusCode, http.StatusOK)
	}

	for _, path := range []string{"/redirect-loop/a", "/redirect-loop/b"} {
		response, err := client.Get(server.BaseURL + path)
		if err != nil {
			t.Fatalf("GET redirect endpoint %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusFound {
			t.Errorf("redirect endpoint %s status = %d, want %d", path, response.StatusCode, http.StatusFound)
		}
	}
}

func TestIssue19SlowFixtureIsDeterministicallyDelayed(t *testing.T) {
	server := New(t)
	started := time.Now()
	response, err := http.Get(server.URLFor(Slow))
	if err != nil {
		t.Fatalf("GET slow fixture: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("slow fixture status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if elapsed := time.Since(started); elapsed < SlowResponseDelay-20*time.Millisecond {
		t.Fatalf("slow fixture completed in %s, want at least %s", elapsed, SlowResponseDelay-20*time.Millisecond)
	}
}
