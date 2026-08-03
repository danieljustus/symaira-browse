package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cdproto "github.com/chromedp/cdproto"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// Console capture bounds (issue #60): the buffer is capped at
// maxConsoleEntries per session and every entry is truncated to
// maxConsoleEntryChars so the console respects the token budget.
const (
	maxConsoleEntries    = 500
	maxConsoleEntryChars = 4096
)

// handleEvent dispatches protocol events from the read loop into the
// per-session console and error buffers. It runs on the read-loop goroutine
// and must not block.
func (e *Engine) handleEvent(sessionID, method string, params json.RawMessage) {
	switch method {
	case "Runtime.consoleAPICalled":
		e.recordConsole(sessionID, params)
	case "Runtime.exceptionThrown":
		e.recordException(sessionID, params)
	}
}

// EnableRuntimeEvents turns on Runtime domain events for a page session
// (idempotent). After this, console API calls and uncaught exceptions are
// buffered until the session closes.
func (e *Engine) EnableRuntimeEvents(ctx context.Context, page engine.Page) error {
	e.runtimeMu.Lock()
	enabled := e.runtimeEnabled[page.SessionID]
	e.runtimeMu.Unlock()
	if enabled {
		return nil
	}
	var result struct{}
	if err := e.call(ctx, page.SessionID, cdproto.CommandRuntimeEnable, struct{}{}, &result); err != nil {
		return fmt.Errorf("enable runtime events: %w", err)
	}
	e.runtimeMu.Lock()
	if e.runtimeEnabled == nil {
		e.runtimeEnabled = make(map[string]bool)
	}
	e.runtimeEnabled[page.SessionID] = true
	e.runtimeMu.Unlock()
	return nil
}

// ConsoleEvents returns a copy of the session's console buffer.
func (e *Engine) ConsoleEvents(page engine.Page) []engine.ConsoleEntry {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	return append([]engine.ConsoleEntry(nil), e.console[page.SessionID]...)
}

// ErrorEvents returns a copy of the session's uncaught-exception buffer.
func (e *Engine) ErrorEvents(page engine.Page) []engine.ErrorEntry {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	return append([]engine.ErrorEntry(nil), e.errors[page.SessionID]...)
}

// ClearConsole drops the session's console buffer.
func (e *Engine) ClearConsole(page engine.Page) {
	e.runtimeMu.Lock()
	delete(e.console, page.SessionID)
	e.runtimeMu.Unlock()
}

// ClearErrors drops the session's error buffer.
func (e *Engine) ClearErrors(page engine.Page) {
	e.runtimeMu.Lock()
	delete(e.errors, page.SessionID)
	e.runtimeMu.Unlock()
}

func (e *Engine) recordConsole(sessionID string, params json.RawMessage) {
	var event struct {
		Type string `json:"type"`
		Args []struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"args"`
		URL        string `json:"url"`
		LineNumber int64  `json:"lineNumber"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	text := renderConsoleArgs(event.Args)
	if text == "" {
		return
	}
	entry := engine.ConsoleEntry{
		Type:      event.Type,
		Text:      truncateConsole(text),
		URL:       event.URL,
		Line:      event.LineNumber + 1, // CDP lines are 0-based
		Timestamp: time.Now(),
	}
	e.runtimeMu.Lock()
	e.console[sessionID] = appendBoundedConsole(e.console[sessionID], entry)
	e.runtimeMu.Unlock()
}

func (e *Engine) recordException(sessionID string, params json.RawMessage) {
	var event struct {
		ExceptionDetails *struct {
			Text       string `json:"text"`
			URL        string `json:"url"`
			LineNumber int64  `json:"lineNumber"`
			StackTrace *struct {
				CallFrames []struct {
					FunctionName string `json:"functionName"`
					URL          string `json:"url"`
					LineNumber   int64  `json:"lineNumber"`
					ColumnNumber int64  `json:"columnNumber"`
				} `json:"callFrames"`
			} `json:"stackTrace"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(params, &event); err != nil || event.ExceptionDetails == nil {
		return
	}
	details := event.ExceptionDetails
	entry := engine.ErrorEntry{
		Text:       truncateConsole(details.Text),
		URL:        details.URL,
		Line:       details.LineNumber + 1,
		Timestamp:  time.Now(),
		StackTrace: renderStackTrace(details.StackTrace),
	}
	e.runtimeMu.Lock()
	e.errors[sessionID] = appendBoundedErrors(e.errors[sessionID], entry)
	e.runtimeMu.Unlock()
}

// renderConsoleArgs renders CDP RemoteObject arguments as one text line:
// string values are used verbatim, everything else falls back to the
// object description.
func renderConsoleArgs(args []struct {
	Type        string          `json:"type"`
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description"`
}) string {
	var parts []string
	for _, arg := range args {
		switch {
		case arg.Type == "string":
			var value string
			if err := json.Unmarshal(arg.Value, &value); err == nil {
				parts = append(parts, value)
				continue
			}
			parts = append(parts, string(arg.Value))
		case arg.Description != "":
			parts = append(parts, arg.Description)
		case len(arg.Value) > 0 && string(arg.Value) != "null":
			parts = append(parts, string(arg.Value))
		}
	}
	return strings.Join(parts, " ")
}

func renderStackTrace(stackTrace *struct {
	CallFrames []struct {
		FunctionName string `json:"functionName"`
		URL          string `json:"url"`
		LineNumber   int64  `json:"lineNumber"`
		ColumnNumber int64  `json:"columnNumber"`
	} `json:"callFrames"`
}) []string {
	if stackTrace == nil {
		return nil
	}
	frames := make([]string, 0, len(stackTrace.CallFrames))
	for _, frame := range stackTrace.CallFrames {
		function := frame.FunctionName
		if function == "" {
			function = "(anonymous)"
		}
		frames = append(frames, fmt.Sprintf("%s (%s:%d:%d)", function, frame.URL, frame.LineNumber+1, frame.ColumnNumber+1))
	}
	return frames
}

func truncateConsole(text string) string {
	if len(text) > maxConsoleEntryChars {
		return text[:maxConsoleEntryChars] + "…"
	}
	return text
}

func appendBoundedConsole(entries []engine.ConsoleEntry, entry engine.ConsoleEntry) []engine.ConsoleEntry {
	if len(entries) >= maxConsoleEntries {
		entries = append([]engine.ConsoleEntry(nil), entries[len(entries)-maxConsoleEntries+1:]...)
	}
	return append(entries, entry)
}

func appendBoundedErrors(entries []engine.ErrorEntry, entry engine.ErrorEntry) []engine.ErrorEntry {
	if len(entries) >= maxConsoleEntries {
		entries = append([]engine.ErrorEntry(nil), entries[len(entries)-maxConsoleEntries+1:]...)
	}
	return append(entries, entry)
}
