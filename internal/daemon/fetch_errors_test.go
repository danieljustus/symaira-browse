package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fetchFrameError runs one fetch.url frame and returns the typed daemon error.
func fetchFrameError(t *testing.T, runtime *FetchRuntime, args map[string]any) *Error {
	t.Helper()
	_, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "fetch.url",
		Args: marshalArgsForTest(args),
	})
	if err == nil {
		t.Fatal("expected an error, got success")
	}
	var daemonErr *Error
	if !errors.As(err, &daemonErr) {
		t.Fatalf("error is %T, want *daemon.Error: %v", err, err)
	}
	return daemonErr
}

// TestFetchURLSelectorErrorIsMalformedRequest verifies a CSS selector that
// matches nothing is reported as a bad argument, not as a generic failure:
// retrying it unchanged can never succeed.
func TestFetchURLSelectorErrorIsMalformedRequest(t *testing.T) {
	server := fetchTestServer(t)
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	daemonErr := fetchFrameError(t, runtime, map[string]any{
		"url":          server.URL,
		"css_selector": ".no-such-element",
	})
	if daemonErr.Code != ErrorMalformedRequest {
		t.Errorf("code = %q, want %q", daemonErr.Code, ErrorMalformedRequest)
	}
	if daemonErr.Retryable == nil || *daemonErr.Retryable {
		t.Errorf("a selector that matches nothing was reported retryable")
	}
}

// TestFetchURLSSRFDeniedIsPeerDenied verifies a policy refusal is
// distinguishable from an ordinary failure.
func TestFetchURLSSRFDeniedIsPeerDenied(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{AllowPrivate: false, Robots: false})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	daemonErr := fetchFrameError(t, runtime, map[string]any{"url": "http://127.0.0.1:9/blocked"})
	if daemonErr.Code != ErrorPeerDenied {
		t.Errorf("code = %q, want %q", daemonErr.Code, ErrorPeerDenied)
	}
	if daemonErr.Retryable == nil || *daemonErr.Retryable {
		t.Errorf("a policy refusal was reported retryable")
	}
}

// TestFetchURLServerErrorIsRetryable verifies a 5xx is marked retryable while
// a 404 is not: the two need different reactions from an agent.
func TestFetchURLServerErrorIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	runtime := newTestFetchRuntime(t)

	daemonErr := fetchFrameError(t, runtime, map[string]any{"url": server.URL})
	if daemonErr.Retryable == nil || !*daemonErr.Retryable {
		t.Errorf("a 503 was not reported retryable: %+v", daemonErr)
	}
}

// TestFetchURL404CarriesRecoveryCandidates verifies the recovery probe's
// result reaches the caller. The pipeline spends real HTTP round-trips
// discovering replacement URLs; discarding them means the agent pays the
// latency and never sees the answer.
func TestFetchURL404CarriesRecoveryCandidates(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/docs/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><title>Docs</title></head><body>
			<article><h1>Documentation</h1>
			<p>Start here for the guide and the reference material.</p>
			<a href="` + base + `/docs/getting-started">Getting started</a>
			<a href="` + base + `/docs/reference">Reference</a>
			</article></body></html>`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	base = server.URL

	runtime := newTestFetchRuntime(t)
	daemonErr := fetchFrameError(t, runtime, map[string]any{"url": server.URL + "/docs/getting-startd"})

	if daemonErr.Details == nil {
		t.Fatalf("404 error carries no details: %+v", daemonErr)
	}
	recovery, ok := daemonErr.Details["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("404 error carries no recovery hints: %+v", daemonErr.Details)
	}
	ancestor, _ := recovery["nearest_ancestor"].(string)
	if !strings.Contains(ancestor, "/docs/") {
		t.Errorf("nearest_ancestor = %q, want the reachable /docs/ ancestor", ancestor)
	}
	candidates, _ := recovery["candidates"].([]map[string]any)
	if len(candidates) == 0 {
		t.Fatalf("recovery hints carry no candidate URLs: %+v", recovery)
	}
	// A candidate the agent cannot navigate to is worse than none.
	for _, candidate := range candidates {
		raw, _ := candidate["url"].(string)
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Errorf("candidate %q is not a valid URL: %v", raw, err)
			continue
		}
		if parsed.Host == "" || strings.Contains(parsed.Path, "http") {
			t.Errorf("candidate %q is not a usable absolute URL", raw)
		}
	}
}
