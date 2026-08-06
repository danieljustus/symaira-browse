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

// Warning is a non-fatal diagnostic attached to a successful envelope. Ref
// and Excerpt carry the optional element locator and evidence excerpt of
// prompt-injection detections (issue #28).
type Warning struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
	Ref      string `json:"ref,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
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
	// Truncate-and-store marker (issue #23, B-19): render head + separator +
	// foot + the cache hint instead of dumping the marker map.
	if marker, ok := truncationMarker(envelope.Data); ok {
		if _, err := fmt.Fprintln(w, marker.Head); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "\n… [truncated: %d of %d tokens] …\n\n", marker.TokensReturned, marker.TokensTotal); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, marker.Foot); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "\nfull output: %s\n", marker.Hint)
		return err
	}
	if text, ok := envelope.Data.(string); ok {
		_, err := fmt.Fprintln(w, text)
		return err
	}
	_, err := fmt.Fprintln(w, envelope.Data)
	return err
}

// truncationMarker detects the budget truncation payload (daemon-side,
// issue #23) and returns its renderable fields. The marker is delivered as a
// JSON object; both the budget.Marker struct and its decoded map form match.
func truncationMarker(data any) (marker, bool) {
	fields, ok := data.(map[string]any)
	if !ok {
		return marker{}, false
	}
	truncated, ok := fields["truncated"].(bool)
	if !ok || !truncated {
		return marker{}, false
	}
	head, _ := fields["head"].(string)
	foot, _ := fields["foot"].(string)
	hint, _ := fields["hint"].(string)
	returned, _ := fields["tokens_returned"].(float64)
	total, _ := fields["tokens_total"].(float64)
	return marker{Head: head, Foot: foot, Hint: hint, TokensReturned: int(returned), TokensTotal: int(total)}, true
}

// marker is the renderable slice of a truncation payload.
type marker struct {
	Head           string
	Foot           string
	Hint           string
	TokensReturned int
	TokensTotal    int
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
