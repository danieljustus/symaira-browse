package chrome

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestRecordConsoleParsesParams(t *testing.T) {
	e := New(Options{})
	params := json.RawMessage(`{
		"type": "warning",
		"args": [
			{"type": "string", "value": "hello"},
			{"type": "string", "value": "world"},
			{"type": "object", "description": "Object"}
		],
		"url": "https://example.com/app.js",
		"lineNumber": 7
	}`)
	e.handleEvent("session-1", "Runtime.consoleAPICalled", params)
	entries := e.ConsoleEvents(engine.Page{SessionID: "session-1"})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Type != "warning" || entry.Text != "hello world Object" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.URL != "https://example.com/app.js" || entry.Line != 8 {
		t.Fatalf("entry url/line = %s/%d, want app.js/8 (1-based)", entry.URL, entry.Line)
	}
}

func TestRecordConsoleIgnoresMalformedAndBoundsBuffer(t *testing.T) {
	e := New(Options{})
	// Malformed params are ignored without panicking.
	e.handleEvent("s", "Runtime.consoleAPICalled", json.RawMessage(`{"type":`))
	if got := len(e.ConsoleEvents(engine.Page{SessionID: "s"})); got != 0 {
		t.Fatalf("malformed params produced %d entries", got)
	}
	// Bounded buffer: appending more than the cap keeps the newest entries.
	for i := 0; i < maxConsoleEntries+50; i++ {
		params, _ := json.Marshal(map[string]any{
			"type": "log",
			"args": []any{map[string]any{"type": "string", "value": "x"}},
		})
		e.handleEvent("s", "Runtime.consoleAPICalled", params)
	}
	entries := e.ConsoleEvents(engine.Page{SessionID: "s"})
	if len(entries) != maxConsoleEntries {
		t.Fatalf("buffer len = %d, want %d", len(entries), maxConsoleEntries)
	}
	// Clear drops the buffer.
	e.ClearConsole(engine.Page{SessionID: "s"})
	if got := len(e.ConsoleEvents(engine.Page{SessionID: "s"})); got != 0 {
		t.Fatalf("after clear: %d entries", got)
	}
}

func TestRecordExceptionRendersStackTrace(t *testing.T) {
	e := New(Options{})
	params := json.RawMessage(`{
		"exceptionDetails": {
			"text": "TypeError: x is not a function",
			"url": "https://example.com/app.js",
			"lineNumber": 3,
			"stackTrace": {
				"callFrames": [
					{"functionName": "run", "url": "https://example.com/app.js", "lineNumber": 3, "columnNumber": 5},
					{"functionName": "", "url": "https://example.com/app.js", "lineNumber": 1, "columnNumber": 0}
				]
			}
		}
	}`)
	e.handleEvent("s", "Runtime.exceptionThrown", params)
	errors := e.ErrorEvents(engine.Page{SessionID: "s"})
	if len(errors) != 1 {
		t.Fatalf("errors = %d, want 1", len(errors))
	}
	entry := errors[0]
	if entry.Text != "TypeError: x is not a function" {
		t.Fatalf("text = %s", entry.Text)
	}
	if len(entry.StackTrace) != 2 {
		t.Fatalf("stacktrace = %v, want 2 frames", entry.StackTrace)
	}
	if entry.StackTrace[0] != "run (https://example.com/app.js:4:6)" {
		t.Fatalf("frame0 = %q", entry.StackTrace[0])
	}
	if !strings.Contains(entry.StackTrace[1], "(anonymous)") {
		t.Fatalf("frame1 = %q, want anonymous fallback", entry.StackTrace[1])
	}
}

func TestTruncateConsoleBoundsEntries(t *testing.T) {
	long := strings.Repeat("a", maxConsoleEntryChars+100)
	truncated := truncateConsole(long)
	if len(truncated) > maxConsoleEntryChars+len("…") {
		t.Fatalf("truncated length = %d, want <= %d", len(truncated), maxConsoleEntryChars+len("…"))
	}
	if !strings.HasPrefix(truncated, strings.Repeat("a", maxConsoleEntryChars)) {
		t.Fatal("truncated text lost its prefix")
	}
	// Short text passes through untouched.
	if got := truncateConsole("short"); got != "short" {
		t.Fatalf("truncateConsole(short) = %q", got)
	}
}
