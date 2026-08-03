package engine

import (
	"context"
	"time"
)

// ConsoleEntry is one captured console API call (issue #60). The payload is
// protocol-neutral; Text is a bounded rendering of the call arguments.
type ConsoleEntry struct {
	Type      string    `json:"type"` // log, warning, error, debug, info, ...
	Text      string    `json:"text"`
	URL       string    `json:"url,omitempty"`
	Line      int64     `json:"line,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ErrorEntry is one uncaught exception (issue #60). StackTrace is rendered
// as "functionName (url:line:column)" frames so it survives JSON transport.
type ErrorEntry struct {
	Text       string    `json:"text"`
	URL        string    `json:"url,omitempty"`
	Line       int64     `json:"line,omitempty"`
	StackTrace []string  `json:"stacktrace,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// RuntimeEvents is the optional engine capability behind the console and
// errors commands. EnableRuntimeEvents is idempotent per page session.
type RuntimeEvents interface {
	EnableRuntimeEvents(context.Context, Page) error
	ConsoleEvents(Page) []ConsoleEntry
	ErrorEvents(Page) []ErrorEntry
	ClearConsole(Page)
	ClearErrors(Page)
}
