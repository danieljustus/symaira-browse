package output

import (
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/exitcodes"
)

// Code is a stable machine-readable error code. Every error path in symbrowse
// produces exactly one code from this enum; free-form strings are never used
// as codes. The full list with exit-code mapping is documented in
// docs/errors.md.
type Code string

const (
	// CodeStaleRef: an element ref that disappeared from the page was accessed.
	CodeStaleRef Code = "stale_ref"
	// CodeUnknownRef: the ref is not present in the current ref map.
	CodeUnknownRef Code = "unknown_ref"
	// CodeInvalidArgs: command arguments are malformed or missing.
	CodeInvalidArgs Code = "invalid_args"
	// CodeInvalidInspection: an unsupported inspection kind or misuse.
	CodeInvalidInspection Code = "invalid_inspection"
	// CodeMalformedRequest: a daemon frame could not be decoded.
	CodeMalformedRequest Code = "malformed_request"
	// CodeUnknownCommand: the daemon does not implement the command.
	CodeUnknownCommand Code = "unknown_command"
	// CodeOperationFailed: a daemon handler failed without a more specific code.
	CodeOperationFailed Code = "operation_failed"
	// CodeOperationTimeout: the daemon operation exceeded its timeout.
	CodeOperationTimeout Code = "operation_timeout"
	// CodePeerDenied: the connecting peer was rejected.
	CodePeerDenied Code = "peer_denied"
	// CodeDaemonUnavailable: the daemon could not be reached or started.
	CodeDaemonUnavailable Code = "daemon_unavailable"
	// CodeInvalidSession: the session name is invalid or unknown.
	CodeInvalidSession Code = "invalid_session"
	// CodeSessionNotFound: the session does not exist.
	CodeSessionNotFound Code = "session_not_found"
	// CodeNotFound: a requested resource does not exist.
	CodeNotFound Code = "not_found"
	// CodeAuth: authentication failed.
	CodeAuth Code = "auth"
	// CodePermission: the operation is not permitted.
	CodePermission Code = "permission"
	// CodeValidation: a value failed validation.
	CodeValidation Code = "validation"
	// CodeNoInput: required input was not provided.
	CodeNoInput Code = "no_input"
	// CodeFlowFailed: a flow step or the flow run failed.
	CodeFlowFailed Code = "flow_failed"
	// CodeConfig: configuration is invalid or unreadable.
	CodeConfig Code = "config"
	// CodeConflict: the operation conflicts with the current state.
	CodeConflict Code = "conflict"
	// CodeSessionUserControl: a human currently controls the session.
	CodeSessionUserControl Code = "session_user_control"
	// CodeSessionInactive: the session has completed or expired.
	CodeSessionInactive Code = "session_inactive"
	// CodeHandoffTimeout: a handoff timed out and was denied.
	CodeHandoffTimeout Code = "handoff_timeout"
	// CodeUnavailable: a required service or resource is unavailable.
	CodeUnavailable Code = "unavailable"
	// CodeInternal: an unexpected internal failure.
	CodeInternal Code = "internal"
)

// allCodes is the canonical enum. Codes are compared by value so the set also
// covers codes produced by the daemon protocol (daemon.Error) and the engine.
var allCodes = map[Code]bool{
	CodeStaleRef: true, CodeUnknownRef: true, CodeInvalidArgs: true,
	CodeInvalidInspection: true, CodeMalformedRequest: true, CodeUnknownCommand: true,
	CodeOperationFailed: true, CodeOperationTimeout: true, CodePeerDenied: true,
	CodeDaemonUnavailable: true, CodeInvalidSession: true, CodeSessionNotFound: true,
	CodeNotFound: true, CodeAuth: true, CodePermission: true, CodeValidation: true,
	CodeNoInput: true, CodeFlowFailed: true, CodeConfig: true, CodeConflict: true,
	CodeSessionUserControl: true, CodeSessionInactive: true, CodeHandoffTimeout: true,
	CodeUnavailable: true,
	CodeInternal:    true,
}

// IsValid reports whether code is a member of the documented enum.
func IsValid(code string) bool {
	return allCodes[Code(code)]
}

// codeFromKind maps a corekit error kind to the unified error code.
func codeFromKind(kind exitcodes.ErrorKind) Code {
	switch kind {
	case exitcodes.KindNotFound:
		return CodeNotFound
	case exitcodes.KindAuth:
		return CodeAuth
	case exitcodes.KindPermission:
		return CodePermission
	case exitcodes.KindValidation:
		return CodeInvalidArgs
	case exitcodes.KindConfig:
		return CodeConfig
	case exitcodes.KindConflict:
		return CodeConflict
	case exitcodes.KindUnavailable:
		return CodeUnavailable
	case exitcodes.KindInternal:
		return CodeInternal
	default:
		return CodeInternal
	}
}

// kindFromCode maps a unified error code back to the corekit error kind used
// for exit-code resolution at the process boundary.
func kindFromCode(code Code) exitcodes.ErrorKind {
	switch code {
	case CodeNotFound, CodeSessionNotFound, CodeSessionInactive, CodeUnknownRef:
		return exitcodes.KindNotFound
	case CodeAuth:
		return exitcodes.KindAuth
	case CodePermission, CodePeerDenied:
		return exitcodes.KindPermission
	case CodeInvalidArgs, CodeInvalidInspection, CodeInvalidSession,
		CodeMalformedRequest, CodeUnknownCommand, CodeValidation, CodeNoInput:
		return exitcodes.KindValidation
	case CodeConfig:
		return exitcodes.KindConfig
	case CodeStaleRef, CodeConflict, CodeSessionUserControl:
		return exitcodes.KindConflict
	case CodeFlowFailed:
		return exitcodes.KindInternal
	case CodeDaemonUnavailable, CodeUnavailable, CodeOperationTimeout, CodeOperationFailed, CodeHandoffTimeout:
		return exitcodes.KindUnavailable
	default:
		return exitcodes.KindInternal
	}
}

// KindFromCode maps a unified error code to the corekit error kind.
func KindFromCode(code Code) exitcodes.ErrorKind {
	return kindFromCode(code)
}

// ExitCodeFromCode resolves the process exit code for a unified error code.
func ExitCodeFromCode(code Code) exitcodes.ExitCode {
	switch kindFromCode(code) {
	case exitcodes.KindNotFound:
		return exitcodes.ExitNotFound
	case exitcodes.KindAuth:
		return exitcodes.ExitNoAuth
	case exitcodes.KindPermission:
		return exitcodes.ExitForbidden
	case exitcodes.KindValidation:
		return exitcodes.ExitNoInput
	case exitcodes.KindConfig:
		return exitcodes.ExitConfig
	case exitcodes.KindConflict:
		return exitcodes.ExitConflict
	case exitcodes.KindUnavailable:
		return exitcodes.ExitGeneric
	default:
		return exitcodes.ExitSoftware
	}
}

// metadataError is implemented by hard-stop errors that carry retry and
// explicit-confirmation guidance across transports.
type metadataError interface {
	ErrorCode() string
	RetryableError() bool
	RequiresConfirmation() bool
	ResumeGuidance() string
}

// codedError is implemented by structured errors that already carry a code
// from the unified enum (for example daemon protocol errors).
type codedError interface {
	ErrorCode() string
}

// FromError converts any error into the unified error payload. The code is
// derived from the most specific classification available: a corekit CLIError
// kind, a structured hard-stop error, a protocol error code, or the internal
// fallback.
func FromError(err error) *Error {
	if err == nil {
		return nil
	}
	var cliErr *exitcodes.CLIError
	if errors.As(err, &cliErr) {
		return &Error{
			Code:    string(codeFromKind(cliErr.Kind)),
			Message: cliErr.Message,
			Hint:    cliErr.Hint,
		}
	}
	var metadata metadataError
	if errors.As(err, &metadata) {
		retryable := metadata.RetryableError()
		requiresConfirmation := metadata.RequiresConfirmation()
		return &Error{
			Code:                     metadata.ErrorCode(),
			Message:                  err.Error(),
			Retryable:                &retryable,
			RequiresUserConfirmation: &requiresConfirmation,
			ResumeHint:               metadata.ResumeGuidance(),
		}
	}
	var coded codedError
	if errors.As(err, &coded) {
		code := coded.ErrorCode()
		if IsValid(code) {
			return &Error{Code: code, Message: err.Error()}
		}
	}
	return &Error{Code: string(CodeInternal), Message: fmt.Sprintf("%v", err)}
}
