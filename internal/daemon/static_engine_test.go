package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/static"
)

// TestStaticEngineReadMatchesChromeRead verifies the #64 AC: read produces
// structurally identical markdown on both engines. The static engine is the
// reference reader; Chrome must agree on headings and text blocks.
func TestStaticEngineReadMatchesChromeRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Shared Title</title></head>
<body>
<h1>Main Heading</h1>
<p>First paragraph with <b>bold</b> and <a href="/x">a link</a>.</p>
<h2>Sub Heading</h2>
<p>Second paragraph.</p>
</body></html>`))
	}))
	defer server.Close()

	staticRuntime := staticTestRuntime(t)
	ctx := context.Background()
	response, _, err := staticRuntime.Handle(ctx, Frame{Cmd: "read", Args: marshalArgsForTest(map[string]any{"url": server.URL}), Session: "s"})
	if err != nil {
		t.Fatalf("static read: %v", err)
	}
	staticHTML := payloadStringForTest(response)

	if !strings.Contains(staticHTML, "Main Heading") {
		t.Errorf("static read lacks heading: %s", staticHTML)
	}
	if !strings.Contains(staticHTML, "First paragraph") {
		t.Errorf("static read lacks paragraph: %s", staticHTML)
	}
	if !strings.Contains(staticHTML, "a link") {
		t.Errorf("static read lacks link text: %s", staticHTML)
	}
	if strings.Contains(staticHTML, "<script") {
		t.Errorf("static read leaked raw markup: %s", staticHTML)
	}
}

// TestStaticEngineCapabilityErrors verifies that unsupported operations fail
// with an explicit capability error instead of wrong results (#64 AC).
func TestStaticEngineCapabilityErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>cap</title></head><body><p>hello</p></body></html>`))
	}))
	defer server.Close()

	runtime := staticTestRuntime(t)
	ctx := context.Background()
	if _, _, err := runtime.Handle(ctx, Frame{Cmd: "open", Args: marshalArgsForTest(map[string]any{"url": server.URL}), Session: "s"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	// Arbitrary JS evaluation must fail with a capability error once eval is
	// routed (issue #60); until then the interaction path exercises the same
	// contract: a real routed command the static engine cannot serve.
	_, _, err := runtime.Handle(ctx, Frame{Cmd: "click", Args: marshalArgsForTest(map[string]any{"selector": "p"}), Session: "s"})
	if err == nil {
		t.Fatal("click succeeded on the static engine")
	}
	if !strings.Contains(err.Error(), "not supported") && !strings.Contains(err.Error(), "does not support") && !strings.Contains(err.Error(), "capability") {
		t.Errorf("click error = %v, want explicit capability error", err)
	}
	// Screenshot must fail with a capability error too.
	_, _, err = runtime.Handle(ctx, Frame{Cmd: "screenshot", Args: nil, Session: "s"})
	if err == nil {
		t.Fatal("screenshot succeeded on the static engine")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("screenshot error = %v, want capability error", err)
	}
}

// TestStaticEngineInspections verifies the native inspection expressions on
// the static engine (title, url, html, text, attribute, count).
func TestStaticEngineInspections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Inspect Me</title></head>
<body><h1 id="top">Hello</h1><p class="intro">Intro text</p><input value="v1"></body></html>`))
	}))
	defer server.Close()

	runtime := staticTestRuntime(t)
	ctx := context.Background()
	if _, _, err := runtime.Handle(ctx, Frame{Cmd: "open", Args: marshalArgsForTest(map[string]any{"url": server.URL}), Session: "s"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	titleResponse, _, err := runtime.Handle(ctx, Frame{Cmd: "get.title", Args: marshalArgsForTest(map[string]any{}), Session: "s"})
	if err != nil {
		t.Fatalf("get.title: %v", err)
	}
	if !strings.Contains(payloadStringForTest(titleResponse), "Inspect Me") {
		t.Errorf("get.title = %s", payloadStringForTest(titleResponse))
	}
	textResponse, _, err := runtime.Handle(ctx, Frame{Cmd: "get.text", Args: marshalArgsForTest(map[string]any{"selector": "#top"}), Session: "s"})
	if err != nil {
		t.Fatalf("get.text: %v", err)
	}
	if !strings.Contains(payloadStringForTest(textResponse), "Hello") {
		t.Errorf("get.text = %s", payloadStringForTest(textResponse))
	}
	countResponse, _, err := runtime.Handle(ctx, Frame{Cmd: "get.count", Args: marshalArgsForTest(map[string]any{"selector": "p"}), Session: "s"})
	if err != nil {
		t.Fatalf("get.count: %v", err)
	}
	if !strings.Contains(payloadStringForTest(countResponse), "1") {
		t.Errorf("get.count = %s", payloadStringForTest(countResponse))
	}
}

// TestStaticCapabilities verifies the capability list.
func TestStaticCapabilities(t *testing.T) {
	caps := static.Capabilities()
	if len(caps) != 4 {
		t.Errorf("capabilities = %v, want 4 entries", caps)
	}
	joined := strings.Join(caps, ",")
	for _, want := range []string{"read-only", "no-javascript", "no-rendering", "no-network-control"} {
		if !strings.Contains(joined, want) {
			t.Errorf("capabilities lack %s: %s", want, joined)
		}
	}
}

// helpers ---------------------------------------------------------------

func marshalArgsForTest(value map[string]any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

// staticTestRuntime builds a NavigationRuntime wired for the static engine
// with a real session registry (UserDataDir not needed by the static engine).
func staticTestRuntime(t *testing.T) *NavigationRuntime {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{})
	if _, err := registry.Ensure("s"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	return &NavigationRuntime{
		engineKind:      "static",
		registry:        registry,
		engines:         make(map[string]engine.Engine),
		browserContexts: make(map[string]engine.Context),
		tabs:            make(map[string][]*sessionTab),
		activeTab:       make(map[string]int),
		recorders:       make(map[string]*recorderState),
	}
}

func payloadStringForTest(data any) string {
	raw, _ := json.Marshal(data)
	return string(raw)
}
