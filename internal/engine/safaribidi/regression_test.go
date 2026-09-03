package safaribidi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestSelectedFrameControlsPageOperations(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("browsingContext.getTree", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"contexts": []any{map[string]any{
			"context": "page-1",
			"url":     "https://example.test/",
			"children": []any{map[string]any{
				"context": "frame-1", "url": "https://frame.test/", "children": []any{},
			}},
		}}}, nil
	})
	var targets []string
	fake.on("script.evaluate", func(params json.RawMessage) (any, *fakeError) {
		var request struct {
			Target struct {
				Context string `json:"context"`
			} `json:"target"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			t.Fatalf("decode script.evaluate params: %v", err)
		}
		targets = append(targets, request.Target.Context)
		return scriptString("ok"), nil
	})

	page := engine.Page{ID: "page-1"}
	if err := subject.SetActiveFrame(context.Background(), page, "frame-1"); err != nil {
		t.Fatalf("SetActiveFrame: %v", err)
	}
	if _, err := subject.Evaluate(context.Background(), page, "document.title"); err != nil {
		t.Fatalf("Evaluate selected frame: %v", err)
	}
	if _, err := subject.Evaluate(context.Background(), engine.Page{}, "document.title"); err != nil {
		t.Fatalf("Evaluate selected frame through current page: %v", err)
	}
	if err := subject.SetActiveFrame(context.Background(), page, ""); err != nil {
		t.Fatalf("reset active frame: %v", err)
	}
	if _, err := subject.Evaluate(context.Background(), page, "document.title"); err != nil {
		t.Fatalf("Evaluate main frame: %v", err)
	}
	if got, want := strings.Join(targets, ","), "frame-1,frame-1,page-1"; got != want {
		t.Fatalf("evaluation contexts = %q, want %q", got, want)
	}
}

func TestFrameSelectionCannotCrossTopLevelPages(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("browsingContext.getTree", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"contexts": []any{
			map[string]any{"context": "page-1", "children": []any{map[string]any{"context": "frame-1", "children": []any{}}}},
			map[string]any{"context": "page-2", "children": []any{map[string]any{"context": "frame-2", "children": []any{}}}},
		}}, nil
	})
	if err := subject.SetActiveFrame(context.Background(), engine.Page{ID: "page-1"}, "frame-2"); err == nil {
		t.Fatal("selecting a frame from another page must fail")
	}
}

func TestTopLevelOperationsIgnoreAndClearSelectedFrame(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("browsingContext.getTree", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"contexts": []any{map[string]any{
			"context":  "page-1",
			"children": []any{map[string]any{"context": "frame-1", "children": []any{}}},
		}}}, nil
	})
	var navigated, closed string
	fake.on("browsingContext.navigate", func(params json.RawMessage) (any, *fakeError) {
		var request struct {
			Context string `json:"context"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			t.Fatalf("decode navigate params: %v", err)
		}
		navigated = request.Context
		return map[string]any{"navigation": "nav-1"}, nil
	})
	fake.on("browsingContext.close", func(params json.RawMessage) (any, *fakeError) {
		var request struct {
			Context string `json:"context"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			t.Fatalf("decode close params: %v", err)
		}
		closed = request.Context
		return nil, nil
	})

	page := engine.Page{ID: "page-1"}
	if err := subject.SetActiveFrame(context.Background(), page, "frame-1"); err != nil {
		t.Fatalf("SetActiveFrame before navigation: %v", err)
	}
	if _, err := subject.Navigate(context.Background(), page, "https://example.test/next"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if navigated != "page-1" {
		t.Fatalf("navigation context = %q, want page-1", navigated)
	}
	if frame := subject.activeFrames[page.ID]; frame != "" {
		t.Fatalf("active frame after navigation = %q, want empty", frame)
	}

	if err := subject.SetActiveFrame(context.Background(), page, "frame-1"); err != nil {
		t.Fatalf("SetActiveFrame before close: %v", err)
	}
	if err := subject.TabClose(context.Background(), page); err != nil {
		t.Fatalf("TabClose: %v", err)
	}
	if closed != "page-1" {
		t.Fatalf("closed context = %q, want page-1", closed)
	}
	if frame := subject.activeFrames[page.ID]; frame != "" {
		t.Fatalf("active frame after close = %q, want empty", frame)
	}
}

func TestTabListMarksTheCurrentTopLevelContextActive(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	tabs, err := subject.TabList(context.Background(), engine.Context{})
	if err != nil {
		t.Fatalf("TabList: %v", err)
	}
	if len(tabs) != 1 || tabs[0].ID != "page-1" || !tabs[0].Active {
		t.Fatalf("tabs = %+v", tabs)
	}
}

func TestTabNewAcceptsAboutBlankWithoutNavigation(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("browsingContext.create", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"context": "page-2"}, nil
	})
	page, err := subject.TabNew(context.Background(), engine.Context{}, "", "about:blank")
	if err != nil {
		t.Fatalf("TabNew: %v", err)
	}
	if page.ID != "page-2" {
		t.Fatalf("page id = %q, want page-2", page.ID)
	}
	for _, method := range fake.called() {
		if method == "browsingContext.navigate" {
			t.Fatal("about:blank tab must not issue a navigation command")
		}
	}
}

func TestNavigationStateReportsScriptException(t *testing.T) {
	fake := newFakeBidi(t)
	subject := newFakeEngine(t, fake)
	fake.on("script.evaluate", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{
			"type":             "exception",
			"exceptionDetails": map[string]any{"text": "navigation changed during inspection"},
		}, nil
	})
	_, err := subject.NavigationState(context.Background(), engine.Page{ID: "page-1"})
	if err == nil || !strings.Contains(err.Error(), "navigation changed during inspection") {
		t.Fatalf("NavigationState error = %v", err)
	}
}

func TestExecuteClearsExpiredWriteDeadline(t *testing.T) {
	fake := newFakeBidi(t)
	conn, err := verifiedDial(context.Background(), fake.url(), Options{DriverReadyTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("verifiedDial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	first, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err := conn.Execute(first, "session.status", map[string]any{}, nil); err != nil {
		cancel()
		t.Fatalf("first Execute: %v", err)
	}
	cancel()
	time.Sleep(150 * time.Millisecond)
	if err := conn.Execute(context.Background(), "session.status", map[string]any{}, nil); err != nil {
		t.Fatalf("Execute after expired deadline: %v", err)
	}
}

func TestCreateSessionRecordsIDBeforeSocketValidation(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = r.URL.Path == "/session/session-1"
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{"webSocketUrl":true}}}`))
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	_, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split server host: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}

	session := &driverSession{httpPort: port}
	if err := session.createSession(context.Background(), Options{}); err == nil {
		t.Fatal("boolean webSocketUrl must fail socket validation")
	}
	if session.sessionID != "session-1" {
		t.Fatalf("session id = %q, want session-1", session.sessionID)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !deleted {
		t.Fatal("Close did not delete the partially initialized WebDriver session")
	}
}

func TestWaitForDriverReturnsWhenProcessAlreadyExited(t *testing.T) {
	done := make(chan struct{})
	close(done)
	session := &driverSession{httpPort: 1, waitDone: done, waitErr: errors.New("exit status 1")}
	started := time.Now()
	err := waitForDriver(context.Background(), session, time.Second)
	if err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("waitForDriver error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("waitForDriver took %s after process exit", elapsed)
	}
}
