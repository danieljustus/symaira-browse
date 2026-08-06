package chrome

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// scriptedReply is one CDP response produced by the scripted peer.
type scriptedReply struct {
	result json.RawMessage
	err    *rpcError
}

// scriptedEngine spins up an in-process WebSocket CDP peer and wires it into
// an Engine without launching Chrome. The script function sees every request
// (method, session id, params) and returns the reply to send back. This lets
// the CDP method paths (Evaluate, AXTree, Screenshot) be unit-tested with a
// scripted/recorded transport instead of a real browser (issue #152).
func scriptedEngine(t *testing.T, script func(req rpcRequest) scriptedReply) *Engine {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()
		for {
			var req rpcRequest
			if err := ws.ReadJSON(&req); err != nil {
				return
			}
			if req.ID == 0 { // events are not exercised by these tests
				continue
			}
			reply := script(req)
			payload := rpcResponse{ID: req.ID}
			if reply.err != nil {
				payload.Error = reply.err
			} else {
				payload.Result = reply.result
			}
			if err := ws.WriteJSON(payload); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), 2*time.Second)
	if err != nil {
		t.Fatalf("dial scripted CDP peer: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	e := New(Options{RequestTimeout: 2 * time.Second})
	e.conn = conn
	return e
}

func okReply(t *testing.T, body string) scriptedReply {
	t.Helper()
	return scriptedReply{result: json.RawMessage(body)}
}

func errReply(code int, message string) scriptedReply {
	return scriptedReply{err: &rpcError{Code: code, Message: message}}
}

// paramsOf re-marshals the request params so assertions can read them as a map.
func paramsOf(t *testing.T, req rpcRequest) map[string]any {
	t.Helper()
	raw, err := json.Marshal(req.Params)
	if err != nil {
		t.Fatalf("marshal request params: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode request params: %v", err)
	}
	return params
}

func TestEvaluateCDPMethod(t *testing.T) {
	var gotMethod, gotSession string
	var gotParams map[string]any
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		gotMethod, gotSession = req.Method, req.SessionID
		gotParams = paramsOf(t, req)
		return okReply(t, `{"result":{"type":"string","value":"hello","description":"hello"}}`)
	})

	out, err := e.Evaluate(context.Background(), engine.Page{ID: "t1", SessionID: "sess-1"}, "document.title")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if gotMethod != "Runtime.evaluate" {
		t.Fatalf("method = %q, want Runtime.evaluate", gotMethod)
	}
	if gotSession != "sess-1" {
		t.Fatalf("session = %q, want sess-1", gotSession)
	}
	if gotParams["expression"] != "document.title" {
		t.Fatalf("expression = %v", gotParams["expression"])
	}
	if gotParams["returnByValue"] != true || gotParams["awaitPromise"] != true {
		t.Fatalf("returnByValue/awaitPromise = %v/%v, want true/true", gotParams["returnByValue"], gotParams["awaitPromise"])
	}
	if out.Type != "string" || string(out.Value) != `"hello"` || out.Description != "hello" {
		t.Fatalf("EvaluationResult = %+v (value %s)", out, out.Value)
	}
}

func TestEvaluateMapsExceptionDetails(t *testing.T) {
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		return okReply(t, `{"result":{"type":"undefined"},"exceptionDetails":{"text":"ReferenceError: x is not defined"}}`)
	})
	out, err := e.Evaluate(context.Background(), engine.Page{SessionID: "sess-1"}, "x")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.ExceptionText != "ReferenceError: x is not defined" {
		t.Fatalf("ExceptionText = %q", out.ExceptionText)
	}
}

func TestEvaluateSurfacesCDPError(t *testing.T) {
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		return errReply(-32602, "invalid params")
	})
	_, err := e.Evaluate(context.Background(), engine.Page{SessionID: "sess-1"}, "1")
	if err == nil || !strings.Contains(err.Error(), "Runtime.evaluate failed (-32602)") {
		t.Fatalf("err = %v, want CDP error mapping", err)
	}
}

func TestEvaluateTimeout(t *testing.T) {
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		time.Sleep(500 * time.Millisecond) // far beyond the request timeout
		return okReply(t, `{"result":{"type":"number","value":1}}`)
	})
	e.options.RequestTimeout = 30 * time.Millisecond
	_, err := e.Evaluate(context.Background(), engine.Page{SessionID: "sess-1"}, "1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestAXTreeCDPMethod(t *testing.T) {
	var gotMethod, gotSession string
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		gotMethod, gotSession = req.Method, req.SessionID
		return okReply(t, `{"nodes":[{"nodeId":"1","role":{"type":"rootWebArea"}},{"nodeId":"2","role":{"type":"heading"}}]}`)
	})
	nodes, err := e.AXTree(context.Background(), engine.Page{ID: "t1", SessionID: "sess-2"})
	if err != nil {
		t.Fatalf("AXTree: %v", err)
	}
	if gotMethod != "Accessibility.getFullAXTree" {
		t.Fatalf("method = %q, want Accessibility.getFullAXTree", gotMethod)
	}
	if gotSession != "sess-2" {
		t.Fatalf("session = %q, want sess-2", gotSession)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	if !strings.Contains(string(nodes[0].Raw), `"rootWebArea"`) || !strings.Contains(string(nodes[1].Raw), `"heading"`) {
		t.Fatalf("node payloads not preserved: %s / %s", nodes[0].Raw, nodes[1].Raw)
	}
}

func TestAXTreeSurfacesCDPError(t *testing.T) {
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		return errReply(-32000, "session detached")
	})
	nodes, err := e.AXTree(context.Background(), engine.Page{SessionID: "sess-1"})
	if err == nil || nodes != nil || !strings.Contains(err.Error(), "Accessibility.getFullAXTree failed (-32000)") {
		t.Fatalf("err = %v, nodes = %v", err, nodes)
	}
}

func TestScreenshotCDPMethod(t *testing.T) {
	// A minimal PNG header; the engine only decodes base64, the bytes pass through.
	want := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	var gotMethod, gotSession string
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		gotMethod, gotSession = req.Method, req.SessionID
		return okReply(t, `{"data":"`+base64.StdEncoding.EncodeToString(want)+`"}`)
	})
	got, err := e.Screenshot(context.Background(), engine.Page{ID: "t1", SessionID: "sess-3"})
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if gotMethod != "Page.captureScreenshot" {
		t.Fatalf("method = %q, want Page.captureScreenshot", gotMethod)
	}
	if gotSession != "sess-3" {
		t.Fatalf("session = %q, want sess-3", gotSession)
	}
	if string(got) != string(want) {
		t.Fatalf("screenshot bytes = %q, want %q", got, want)
	}
}

func TestScreenshotRejectsUndecodableData(t *testing.T) {
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		return okReply(t, `{"data":"%%%not-base64%%%"}`)
	})
	_, err := e.Screenshot(context.Background(), engine.Page{SessionID: "sess-1"})
	if err == nil || !strings.Contains(err.Error(), "decode screenshot") {
		t.Fatalf("err = %v, want decode screenshot error", err)
	}
}

func TestScreenshotSurfacesCDPError(t *testing.T) {
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		return errReply(-32000, "capture failed")
	})
	_, err := e.Screenshot(context.Background(), engine.Page{SessionID: "sess-1"})
	if err == nil || !strings.Contains(err.Error(), "Page.captureScreenshot failed (-32000)") {
		t.Fatalf("err = %v, want CDP error mapping", err)
	}
}
