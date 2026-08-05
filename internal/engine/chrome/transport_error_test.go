package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cdproto "github.com/chromedp/cdproto"
	"github.com/gorilla/websocket"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// cdpFixture starts a websocket server that answers each request with the
// JSON produced by respond(requestID, method). A nil reply leaves the request
// unanswered (the connection stays open).
func cdpFixture(t *testing.T, respond func(requestID uint64, method string) []byte) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()
		for {
			var request struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if err := ws.ReadJSON(&request); err != nil {
				return
			}
			reply := respond(request.ID, request.Method)
			if reply == nil {
				continue
			}
			if err := ws.WriteJSON(json.RawMessage(reply)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func wsEndpoint(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// engineWithConn returns an Engine whose CDP connection points at a fake
// websocket server.
func engineWithConn(t *testing.T, server *httptest.Server) *Engine {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dial(ctx, wsEndpoint(server), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	e := New(Options{RequestTimeout: time.Second})
	e.mu.Lock()
	e.conn = conn
	e.mu.Unlock()
	return e
}

func TestDialFailure(t *testing.T) {
	// A port that was listening and is now closed refuses the dial.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := dial(ctx, "ws://"+addr+"/devtools", 500*time.Millisecond); err == nil {
		t.Fatal("expected the dial to fail for a closed port")
	}
}

func TestDialRejectsNonWebsocketEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a websocket"))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := dial(ctx, wsEndpoint(server), time.Second); err == nil {
		t.Fatal("expected a non-websocket endpoint to fail the dial")
	}
}

func TestExecuteErrorFrameAndDecode(t *testing.T) {
	server := cdpFixture(t, func(id uint64, method string) []byte {
		switch method {
		case "Fail.method":
			return []byte(fmt.Sprintf(`{"id":%d,"error":{"code":-32601,"message":"Method not found"}}`, id))
		case "Bad.result":
			return []byte(fmt.Sprintf(`{"id":%d,"result":"not-an-object"}`, id))
		case "Null.result":
			return []byte(fmt.Sprintf(`{"id":%d,"result":null}`, id))
		default:
			return []byte(fmt.Sprintf(`{"id":%d,"result":{}}`, id))
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dial(ctx, wsEndpoint(server), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	err = conn.Execute(ctx, "Fail.method", struct{}{}, nil)
	if err == nil || !strings.Contains(err.Error(), "CDP Fail.method failed (-32601): Method not found") {
		t.Fatalf("error frame err = %v", err)
	}
	var target struct {
		OK bool `json:"ok"`
	}
	err = conn.Execute(ctx, "Bad.result", struct{}{}, &target)
	if err == nil || !strings.Contains(err.Error(), "decode CDP response Bad.result") {
		t.Fatalf("decode err = %v", err)
	}
	// A nil result skips decoding entirely.
	if err := conn.Execute(ctx, "Any.method", struct{}{}, nil); err != nil {
		t.Fatalf("nil-result execute = %v", err)
	}
	// A null result into a non-nil target also skips decoding.
	if err := conn.Execute(ctx, "Null.result", struct{}{}, &target); err != nil {
		t.Fatalf("null-result execute = %v", err)
	}
}

func TestExecuteContextTimeout(t *testing.T) {
	server := cdpFixture(t, func(uint64, string) []byte { return nil }) // never replies
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dial(ctx, wsEndpoint(server), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer shortCancel()
	err = conn.Execute(shortCtx, "Slow.method", struct{}{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestExecuteServerDisconnectFailsPending(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		var request map[string]any
		_ = ws.ReadJSON(&request)
		time.Sleep(100 * time.Millisecond)
		_ = ws.Close() // drop the connection without a reply
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dial(ctx, wsEndpoint(server), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	err = conn.Execute(ctx, "Drop.method", struct{}{}, nil)
	if err == nil || !strings.Contains(err.Error(), "CDP Drop.method failed (-32000)") {
		t.Fatalf("err = %v, want the failPending error frame", err)
	}
}

func TestExecuteAfterCloseAndNilConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// A zero-value connection was never dialed.
	if err := (&rpcConnection{}).Execute(ctx, "X.method", struct{}{}, nil); err == nil || !strings.Contains(err.Error(), "CDP connection is closed") {
		t.Fatalf("zero-value execute err = %v", err)
	}
	// A dialed connection that is closed locally rejects new commands.
	server := cdpFixture(t, func(id uint64, method string) []byte {
		return []byte(fmt.Sprintf(`{"id":%d,"result":{}}`, id))
	})
	conn, err := dial(ctx, wsEndpoint(server), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Execute(ctx, "Ok.method", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Execute(ctx, "After.method", struct{}{}, nil); err == nil || !strings.Contains(err.Error(), "CDP connection is closed") {
		t.Fatalf("post-close execute err = %v", err)
	}
}

func TestEngineCallErrorBranches(t *testing.T) {
	t.Run("not launched", func(t *testing.T) {
		e := New(Options{RequestTimeout: time.Second})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := e.call(ctx, "", "Target.createTarget", struct{}{}, nil)
		if err == nil || !strings.Contains(err.Error(), "chrome engine is not launched") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("CDP error frame", func(t *testing.T) {
		server := cdpFixture(t, func(id uint64, method string) []byte {
			return []byte(fmt.Sprintf(`{"id":%d,"error":{"code":-32000,"message":"boom"}}`, id))
		})
		e := engineWithConn(t, server)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := e.call(ctx, "", "Target.createTarget", struct{}{}, nil)
		if err == nil || !strings.Contains(err.Error(), "CDP Target.createTarget failed (-32000): boom") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNewContextAndNewPageErrorBranches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	t.Run("empty browser context id", func(t *testing.T) {
		server := cdpFixture(t, func(id uint64, method string) []byte {
			return []byte(fmt.Sprintf(`{"id":%d,"result":{}}`, id))
		})
		if _, err := engineWithConn(t, server).NewContext(ctx); err == nil || !strings.Contains(err.Error(), "empty browser context id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty target id", func(t *testing.T) {
		server := cdpFixture(t, func(id uint64, method string) []byte {
			return []byte(fmt.Sprintf(`{"id":%d,"result":{}}`, id))
		})
		if _, err := engineWithConn(t, server).NewPage(ctx, engine.Context{ID: "ctx1"}, "https://example.com"); err == nil || !strings.Contains(err.Error(), "empty target id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty session id", func(t *testing.T) {
		server := cdpFixture(t, func(id uint64, method string) []byte {
			switch method {
			case cdproto.CommandTargetCreateTarget:
				return []byte(fmt.Sprintf(`{"id":%d,"result":{"targetId":"t1"}}`, id))
			default:
				return []byte(fmt.Sprintf(`{"id":%d,"result":{}}`, id))
			}
		})
		if _, err := engineWithConn(t, server).NewPage(ctx, engine.Context{ID: "ctx1"}, "https://example.com"); err == nil || !strings.Contains(err.Error(), "empty CDP session id") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("enable fails", func(t *testing.T) {
		server := cdpFixture(t, func(id uint64, method string) []byte {
			switch method {
			case cdproto.CommandTargetCreateTarget:
				return []byte(fmt.Sprintf(`{"id":%d,"result":{"targetId":"t1"}}`, id))
			case cdproto.CommandTargetAttachToTarget:
				return []byte(fmt.Sprintf(`{"id":%d,"result":{"sessionId":"s1"}}`, id))
			default:
				return []byte(fmt.Sprintf(`{"id":%d,"error":{"code":-32601,"message":"not enabled"}}`, id))
			}
		})
		if _, err := engineWithConn(t, server).NewPage(ctx, engine.Context{ID: "ctx1"}, "https://example.com"); err == nil || !strings.Contains(err.Error(), "enable Page.enable") {
			t.Fatalf("err = %v", err)
		}
	})
}
