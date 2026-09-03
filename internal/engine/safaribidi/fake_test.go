package safaribidi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeBidi is an in-process WebDriver BiDi server. It exists so every engine
// path is testable without a Safari, and so the transport's handling of the
// typed success/error/event envelope is asserted rather than assumed.
type fakeBidi struct {
	server   *httptest.Server
	upgrader websocket.Upgrader

	mu       sync.Mutex
	writeMu  sync.Mutex
	handlers map[string]func(params json.RawMessage) (any, *fakeError)
	calls    []string
	conns    []*websocket.Conn
}

type fakeError struct {
	Code    string
	Message string
}

func newFakeBidi(t *testing.T) *fakeBidi {
	t.Helper()
	fake := &fakeBidi{handlers: map[string]func(json.RawMessage) (any, *fakeError){}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	fake.on("session.status", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"ready": true, "message": ""}, nil
	})
	return fake
}

func (f *fakeBidi) on(method string, handler func(json.RawMessage) (any, *fakeError)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method] = handler
}

func (f *fakeBidi) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeBidi) url() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http")
}

func (f *fakeBidi) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
	for {
		var request struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, request.Method)
		handler := f.handlers[request.Method]
		f.mu.Unlock()

		if handler == nil {
			_ = f.writeJSON(conn, map[string]any{
				"type": "error", "id": request.ID,
				"error": "unknown command", "message": "'" + request.Method + "' was not found",
			})
			continue
		}
		result, failure := handler(request.Params)
		if failure != nil {
			_ = f.writeJSON(conn, map[string]any{
				"type": "error", "id": request.ID,
				"error": failure.Code, "message": failure.Message,
			})
			continue
		}
		if result == nil {
			result = map[string]any{}
		}
		_ = f.writeJSON(conn, map[string]any{"type": "success", "id": request.ID, "result": result})
	}
}

func (f *fakeBidi) writeJSON(conn *websocket.Conn, value any) error {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return conn.WriteJSON(value)
}

// emit pushes an event frame to every connected client.
func (f *fakeBidi) emit(method string, params any) {
	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()
	for _, conn := range conns {
		_ = f.writeJSON(conn, map[string]any{"type": "event", "method": method, "params": params})
	}
}

// scriptString answers a script.evaluate with a string RemoteValue, the shape
// Safari returns for the JSON payloads this engine asks pages to build.
func scriptString(value string) map[string]any {
	return map[string]any{
		"type":   "success",
		"result": map[string]any{"type": "string", "value": value},
	}
}

// newFakeEngine wires an Engine to a fake BiDi server, bypassing safaridriver.
func newFakeEngine(t *testing.T, fake *fakeBidi) *Engine {
	t.Helper()
	fake.on("browsingContext.getTree", func(json.RawMessage) (any, *fakeError) {
		return map[string]any{"contexts": []any{map[string]any{
			"context": "page-1", "url": "about:blank", "children": []any{},
		}}}, nil
	})
	ctx := context.Background()
	conn, err := verifiedDial(ctx, fake.url(), Options{DriverReadyTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("verifiedDial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	engine := New()
	engine.live = &session{driver: &driverSession{}, conn: conn}
	engine.context = "page-1"
	engine.rootContext = "page-1"
	return engine
}
