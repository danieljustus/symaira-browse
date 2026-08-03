package static

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func testPage(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Unit Page</title></head>
<body><h1 id="top">Hello</h1><p class="intro">Intro <b>bold</b> text</p><a href="/next">Next</a><input value="v1"></body></html>`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestLaunchContextPageLifecycle(t *testing.T) {
	e := New()
	if err := e.Launch(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, err := e.NewContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ID == "" {
		t.Fatal("context id is empty")
	}
	page, err := e.NewPage(context.Background(), ctx, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if page.ID == "" {
		t.Fatal("page id is empty")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNavigateParsesDocument(t *testing.T) {
	e := New()
	defer func() { _ = e.Close() }()
	result, err := e.Navigate(context.Background(), engine.Page{}, testPage(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.FrameID == "" || result.ErrorText != "" {
		t.Fatalf("result = %+v", result)
	}
	// Navigate again after close fails cleanly.
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Navigate(context.Background(), engine.Page{}, "https://example.com"); err == nil {
		t.Fatal("navigate after close succeeded")
	}
}

func TestEvaluateInspectionExpressions(t *testing.T) {
	e := New()
	defer func() { _ = e.Close() }()
	if _, err := e.Navigate(context.Background(), engine.Page{}, testPage(t)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		expression string
		want       string
	}{
		{"document.title", "Unit Page"},
		{"location.href", "http"},                      // prefix: local URL varies per test run
		{"document.documentElement.outerHTML", "html"}, // JSON-escaped markup
	}
	for _, tt := range tests {
		result, err := e.Evaluate(context.Background(), engine.Page{}, tt.expression)
		if err != nil {
			t.Fatalf("%s: %v", tt.expression, err)
		}
		if result.ExceptionText != "" {
			t.Fatalf("%s: exception %s", tt.expression, result.ExceptionText)
		}
		if !strings.Contains(string(result.Value), tt.want) {
			t.Fatalf("%s = %s, want containing %q", tt.expression, result.Value, tt.want)
		}
	}
	// Unsupported JavaScript fails with an explicit capability error.
	if _, err := e.Evaluate(context.Background(), engine.Page{}, "1+1"); err == nil {
		t.Fatal("arbitrary JS succeeded on the static engine")
	}
	// Evaluate before any navigation fails cleanly.
	other := New()
	if _, err := other.Evaluate(context.Background(), engine.Page{}, "document.title"); err == nil {
		t.Fatal("evaluate without page succeeded")
	}
}

func TestInspectKinds(t *testing.T) {
	e := New()
	defer func() { _ = e.Close() }()
	if _, err := e.Navigate(context.Background(), engine.Page{}, testPage(t)); err != nil {
		t.Fatal(err)
	}
	text, err := e.Inspect(context.Background(), engine.Page{}, engine.InspectionRequest{Kind: engine.InspectText, Selector: "#top"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text.Value), "Hello") {
		t.Fatalf("text = %s", text.Value)
	}
	count, err := e.Inspect(context.Background(), engine.Page{}, engine.InspectionRequest{Kind: engine.InspectCount, Selector: "p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(count.Value) != `"1"` {
		t.Fatalf("count = %s, want \"1\"", count.Value)
	}
	attribute, err := e.Inspect(context.Background(), engine.Page{}, engine.InspectionRequest{Kind: engine.InspectAttr, Selector: "input", Attribute: "value"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(attribute.Value), "v1") {
		t.Fatalf("attr = %s", attribute.Value)
	}
	// Missing selector match is an explicit error.
	if _, err := e.Inspect(context.Background(), engine.Page{}, engine.InspectionRequest{Kind: engine.InspectText, Selector: "#nope"}, nil); err == nil {
		t.Fatal("missing selector succeeded")
	}
	// Ref-based inspection is a capability error.
	if _, err := e.Inspect(context.Background(), engine.Page{}, engine.InspectionRequest{Kind: engine.InspectText, Selector: "p"}, &engine.InteractionTarget{NodeID: "n1", BackendNodeID: 1}); err == nil {
		t.Fatal("ref-based inspection succeeded")
	}
}

func TestAXTreeAndNavigationState(t *testing.T) {
	e := New()
	defer func() { _ = e.Close() }()
	if _, err := e.Navigate(context.Background(), engine.Page{}, testPage(t)); err != nil {
		t.Fatal(err)
	}
	nodes, err := e.AXTree(context.Background(), engine.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatal("AX tree is empty")
	}
	state, err := e.NavigationState(context.Background(), engine.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if state.HTTPStatus != 200 || state.ReadyState != "complete" || !state.NetworkIdle {
		t.Fatalf("state = %+v", state)
	}
	// Screenshot is a capability error.
	if _, err := e.Screenshot(context.Background(), engine.Page{}); err == nil {
		t.Fatal("screenshot succeeded")
	}
}

func TestCapabilitiesList(t *testing.T) {
	caps := Capabilities()
	joined := strings.Join(caps, ",")
	for _, want := range []string{"read-only", "no-javascript", "no-rendering", "no-network-control"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("capabilities lack %s: %s", want, joined)
		}
	}
}
