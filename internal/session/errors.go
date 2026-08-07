package session

import "fmt"

// Stable hard-stop codes shared by CLI, daemon, MCP and OOB callers.
const (
	CodeSessionUserControl = "session_user_control"
	CodeSessionInactive    = "session_inactive"
	CodeHandoffTimeout     = "handoff_timeout"
)

// HardStopError is a non-retryable session error. It deliberately carries
// enough machine-readable guidance for every transport to stop without
// guessing whether an implicit retry or takeover is safe.
type HardStopError struct {
	Code                     string
	Message                  string
	Retryable                bool
	RequiresUserConfirmation bool
	ResumeHint               string
}

func (e *HardStopError) Error() string {
	if e == nil {
		return "session hard stop"
	}
	return e.Message
}

// ErrorCode lets the unified output and daemon envelopes preserve the stable
// code without importing this package.
func (e *HardStopError) ErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func (e *HardStopError) RetryableError() bool {
	return e != nil && e.Retryable
}

func (e *HardStopError) RequiresConfirmation() bool {
	return e != nil && e.RequiresUserConfirmation
}

func (e *HardStopError) ResumeGuidance() string {
	if e == nil {
		return ""
	}
	return e.ResumeHint
}

func userControlError(sessionID string) *HardStopError {
	return &HardStopError{
		Code:                     CodeSessionUserControl,
		Message:                  fmt.Sprintf("session %q is controlled by a human", sessionID),
		Retryable:                false,
		RequiresUserConfirmation: true,
		ResumeHint:               "request explicit confirmation before taking control back",
	}
}

func inactiveError(sessionID string) *HardStopError {
	return &HardStopError{
		Code:       CodeSessionInactive,
		Message:    fmt.Sprintf("session %q is inactive", sessionID),
		Retryable:  false,
		ResumeHint: "reopen the session before retrying",
	}
}

func NewHandoffTimeoutError(sessionID string) *HardStopError {
	return timeoutError(sessionID)
}

func timeoutError(sessionID string) *HardStopError {
	return &HardStopError{
		Code:                     CodeHandoffTimeout,
		Message:                  fmt.Sprintf("handoff for session %q timed out and was denied", sessionID),
		Retryable:                false,
		RequiresUserConfirmation: true,
		ResumeHint:               "start a new handoff after explicit human confirmation",
	}
}
