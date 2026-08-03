// Package daemon provides the local symbrowse daemon's Unix-socket protocol.
package daemon

import (
	"encoding/json"
	"fmt"
)

// Frame is one newline-delimited request sent to the daemon.
type Frame struct {
	Cmd       string          `json:"cmd"`
	Args      json.RawMessage `json:"args,omitempty"`
	Session   string          `json:"session,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
}

// Error is the stable structured error payload returned by the daemon.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface for handler-level protocol failures.
func (e *Error) Error() string {
	if e == nil {
		return "daemon protocol error"
	}
	return e.Message
}

// ErrorCode exposes the stable protocol error code for the unified output
// schema (internal/output). Codes are members of the documented enum.
func (e *Error) ErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// Warning is a non-fatal diagnostic attached to a response.
type Warning struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

// Response is one newline-delimited response returned by the daemon.
type Response struct {
	Success  bool      `json:"success"`
	Data     any       `json:"data,omitempty"`
	Error    *Error    `json:"error,omitempty"`
	Warnings []Warning `json:"warnings,omitempty"`
}

const (
	ErrorMalformedRequest  = "malformed_request"
	ErrorUnknownCommand    = "unknown_command"
	ErrorOperationTimeout  = "operation_timeout"
	ErrorPeerDenied        = "peer_denied"
	ErrorDaemonUnavailable = "daemon_unavailable"
	ErrorInvalidSession    = "invalid_session"
	ErrorSessionNotFound   = "session_not_found"
)

// DecodeFrame validates and decodes a single JSON frame.
func DecodeFrame(raw []byte) (Frame, error) {
	var frame Frame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return Frame{}, fmt.Errorf("decode frame: %w", err)
	}
	if frame.Cmd == "" {
		return Frame{}, fmt.Errorf("missing cmd")
	}
	return frame, nil
}

// NewError constructs a response error with a stable code and message.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// ErrorResponse creates a failed protocol response.
func ErrorResponse(code, message string) Response {
	return Response{Success: false, Error: NewError(code, message)}
}

// SuccessResponse creates a successful protocol response.
func SuccessResponse(data any, warnings []Warning) Response {
	return Response{Success: true, Data: data, Warnings: warnings}
}
