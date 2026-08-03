package chrome

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestNetworkCaptureMasksSensitiveHeaders(t *testing.T) {
	e := New(Options{})
	session := "s"
	e.handleEvent(session, "Network.requestWillBeSent", json.RawMessage(`{
		"requestId": "r1",
		"request": {
			"url": "https://example.com/api",
			"method": "POST",
			"headers": {"Authorization": "Bearer secret", "Cookie": "session=abc", "X-Custom": "ok"}
		},
		"type": "XHR",
		"wallTime": 1700000000
	}`))
	e.handleEvent(session, "Network.responseReceived", json.RawMessage(`{
		"requestId": "r1",
		"response": {"status": 200, "statusText": "OK", "mimeType": "application/json", "headers": {"Set-Cookie": "secret=1", "Content-Type": "application/json"}}
	}`))
	e.handleEvent(session, "Network.loadingFinished", json.RawMessage(`{"requestId": "r1"}`))

	requests := e.Requests(engine.Page{SessionID: session})
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.URL != "https://example.com/api" || request.Method != "POST" || request.Type != "XHR" {
		t.Fatalf("request = %+v", request)
	}
	if request.Status != 200 || !request.Finished {
		t.Fatalf("status/finished = %d/%v", request.Status, request.Finished)
	}
	if request.RequestHeaders["Authorization"] != "[redacted]" {
		t.Fatalf("Authorization not masked: %q", request.RequestHeaders["Authorization"])
	}
	if request.RequestHeaders["Cookie"] != "[redacted]" {
		t.Fatalf("Cookie not masked: %q", request.RequestHeaders["Cookie"])
	}
	if request.RequestHeaders["X-Custom"] != "ok" {
		t.Fatalf("X-Custom lost: %q", request.RequestHeaders["X-Custom"])
	}
	if request.ResponseHeaders["Set-Cookie"] != "[redacted]" {
		t.Fatalf("Set-Cookie not masked: %q", request.ResponseHeaders["Set-Cookie"])
	}
	byID, found := e.Request(engine.Page{SessionID: session}, "r1")
	if !found || byID.URL != request.URL {
		t.Fatalf("Request by id = %+v, found=%v", byID, found)
	}
}

func TestNetworkCaptureFailureAndBounding(t *testing.T) {
	e := New(Options{})
	e.handleEvent("s", "Network.requestWillBeSent", json.RawMessage(`{
		"requestId": "r2", "request": {"url": "https://example.com/x", "method": "GET", "headers": {}}, "type": "Document", "wallTime": 1700000001
	}`))
	e.handleEvent("s", "Network.loadingFailed", json.RawMessage(`{"requestId": "r2", "errorText": "net::ERR_CONNECTION_REFUSED"}`))
	requests := e.Requests(engine.Page{SessionID: "s"})
	if len(requests) != 1 || requests[0].Failed != "net::ERR_CONNECTION_REFUSED" || !requests[0].Finished {
		t.Fatalf("requests = %+v", requests)
	}
	// Bounding: the list never grows past the cap.
	for i := 0; i < maxNetworkRequests+10; i++ {
		payload, _ := json.Marshal(map[string]any{
			"requestId": "bulk-" + string(rune('a'+i%26)) + string(rune('0'+i%10)),
			"request":   map[string]any{"url": "https://example.com/", "method": "GET", "headers": map[string]any{}},
			"type":      "Document",
			"wallTime":  1,
		})
		e.handleEvent("s", "Network.requestWillBeSent", payload)
	}
	if got := len(e.Requests(engine.Page{SessionID: "s"})); got != maxNetworkRequests {
		t.Fatalf("bounded requests = %d, want %d", got, maxNetworkRequests)
	}
}

func TestPatternMatchesRoute(t *testing.T) {
	if !patternMatchesRoute("https://example.com/api", "https://example.com/api") {
		t.Fatal("exact match failed")
	}
	if !patternMatchesRoute("https://example.com/*", "https://example.com/api/v1") {
		t.Fatal("prefix match failed")
	}
	if patternMatchesRoute("https://example.com/api", "https://example.com/other") {
		t.Fatal("non-matching URL matched")
	}
}

func TestMaskHeaders(t *testing.T) {
	masked := maskHeaders(map[string]string{"Authorization": "x", "authorization": "y", "Proxy-Authorization": "z", "Cookie": "c", "Set-Cookie": "s", "Accept": "a"})
	for _, key := range []string{"Authorization", "authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"} {
		if masked[key] != "[redacted]" {
			t.Fatalf("%s = %q, want [redacted]", key, masked[key])
		}
	}
	if masked["Accept"] != "a" {
		t.Fatalf("Accept = %q", masked["Accept"])
	}
}

func TestHARBuildsLoadableDocument(t *testing.T) {
	e := New(Options{})
	session := "s"
	e.handleEvent(session, "Network.requestWillBeSent", json.RawMessage(`{
		"requestId": "r1",
		"request": {"url": "https://example.com/page", "method": "GET", "headers": {"Authorization": "Bearer tok", "Accept": "text/html"}},
		"type": "Document", "wallTime": 1700000000
	}`))
	e.handleEvent(session, "Network.responseReceived", json.RawMessage(`{
		"requestId": "r1",
		"response": {"status": 200, "statusText": "OK", "mimeType": "text/html", "headers": {"Content-Type": "text/html"}}
	}`))
	document, err := e.HAR(context.Background(), engine.Page{SessionID: session}, engine.HAROptions{Content: "none"})
	if err != nil {
		t.Fatal(err)
	}
	var har map[string]any
	if err := json.Unmarshal(document, &har); err != nil {
		t.Fatalf("HAR is not valid JSON: %v", err)
	}
	log, _ := har["log"].(map[string]any)
	if log == nil || log["version"] != "1.2" {
		t.Fatalf("HAR log = %v", log)
	}
	entries, _ := log["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("HAR entries = %d, want 1", len(entries))
	}
	entry, _ := entries[0].(map[string]any)
	request, _ := entry["request"].(map[string]any)
	headers, _ := request["headers"].([]any)
	found := false
	for _, header := range headers {
		pair, _ := header.(map[string]any)
		if pair["name"] == "Authorization" {
			found = true
			if pair["value"] != "[redacted]" {
				t.Fatalf("HAR Authorization = %q, want [redacted]", pair["value"])
			}
		}
	}
	if !found {
		t.Fatal("HAR lacks the Authorization header")
	}
	response, _ := entry["response"].(map[string]any)
	content, _ := response["content"].(map[string]any)
	if _, hasText := content["text"]; hasText {
		t.Fatal("content none must not include bodies")
	}
}

func TestHARContentAllIncludesBodies(t *testing.T) {
	e := New(Options{})
	session := "s"
	e.handleEvent(session, "Network.requestWillBeSent", json.RawMessage(`{
		"requestId": "r1", "request": {"url": "https://example.com/data", "method": "GET", "headers": {}}, "type": "XHR", "wallTime": 1700000000
	}`))
	e.networkMu.Lock()
	if e.networkBodies[session] == nil {
		e.networkBodies[session] = make(map[string]string)
	}
	e.networkBodies[session]["r1"] = `{"ok":true}`
	e.networkMu.Unlock()
	document, err := e.HAR(context.Background(), engine.Page{SessionID: session}, engine.HAROptions{Content: "all"})
	if err != nil {
		t.Fatal(err)
	}
	var har map[string]any
	if err := json.Unmarshal(document, &har); err != nil {
		t.Fatal(err)
	}
	log, _ := har["log"].(map[string]any)
	entries, _ := log["entries"].([]any)
	entry, _ := entries[0].(map[string]any)
	response, _ := entry["response"].(map[string]any)
	content, _ := response["content"].(map[string]any)
	if text, _ := content["text"].(string); text != `{"ok":true}` {
		t.Fatalf("HAR content text = %q, want the response body", text)
	}
}
