// Package output implements the unified symbrowse output and error schema.
//
// Every command writes exactly one envelope. Success:
//
//	{"success":true,"data":…,"warnings":[…]}
//
// Failure:
//
//	{"success":false,"error":{"code":"stale_ref","message":…,"hint":…,"details":{…}}}
//
// The error code is always a member of the documented enum (codes.go) and maps
// onto corekit/exitcodes at the process boundary. Human-readable mode renders
// the same envelope without JSON framing.
package output

import (
	"encoding/json"
	"fmt"
	"io"
)

// Warning is a non-fatal diagnostic attached to a successful envelope.
type Warning struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

// Error is the structured error payload. Code is always a member of the
// documented error-code enum; it is never a free-form string.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Error implements the error interface for envelope-level failures.
func (e *Error) Error() string {
	if e == nil {
		return "output envelope error"
	}
	return e.Message
}

// Envelope is the single output shape for every symbrowse command.
type Envelope struct {
	Success  bool      `json:"success"`
	Data     any       `json:"data,omitempty"`
	Warnings []Warning `json:"warnings,omitempty"`
	Error    *Error    `json:"error,omitempty"`
}

// OK builds a successful envelope.
func OK(data any, warnings []Warning) Envelope {
	return Envelope{Success: true, Data: data, Warnings: warnings}
}

// Failure builds a failed envelope with a documented error code.
func Failure(code, message string) Envelope {
	return Envelope{Success: false, Error: &Error{Code: code, Message: message}}
}

// FailureWithHint builds a failed envelope with code, message and hint.
func FailureWithHint(code, message, hint string) Envelope {
	return Envelope{Success: false, Error: &Error{Code: code, Message: message, Hint: hint}}
}

// Write serialises the envelope. With jsonOutput the envelope is written as
// compact JSON followed by a newline; otherwise only the human-readable
// payload is written.
func Write(w io.Writer, envelope Envelope, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(w, envelope)
	}
	return writeHuman(w, envelope)
}

func writeJSON(w io.Writer, envelope Envelope) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}

func writeHuman(w io.Writer, envelope Envelope) error {
	if !envelope.Success {
		if envelope.Error == nil {
			_, err := fmt.Fprintln(w, "error")
			return err
		}
		_, err := fmt.Fprintln(w, envelope.Error.Message)
		return err
	}
	if envelope.Data == nil {
		_, err := fmt.Fprintln(w, "ok")
		return err
	}
	if text, ok := envelope.Data.(string); ok {
		_, err := fmt.Fprintln(w, text)
		return err
	}
	_, err := fmt.Fprintln(w, envelope.Data)
	return err
}

// WriteError serialises an error as a failed envelope. The error is classified
// through codes.go; a plain error without a mapped kind becomes internal.
func WriteError(w io.Writer, err error, jsonOutput bool) error {
	return Write(w, ErrorEnvelope(err), jsonOutput)
}

// ErrorEnvelope converts an error into the unified failure envelope.
func ErrorEnvelope(err error) Envelope {
	return Envelope{Success: false, Error: FromError(err)}
}
