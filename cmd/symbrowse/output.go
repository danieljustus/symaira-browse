package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// jsonOutputFlag reports whether the unified machine-readable envelope is
// requested. The global --json flag is defined on the root command; every
// subcommand inherits it.
func jsonOutputFlag(cmd *cobra.Command) bool {
	value, err := cmd.Flags().GetBool("json")
	if err != nil {
		return false
	}
	return value
}

// writeEnvelope writes one unified envelope to the command output.
func writeEnvelope(cmd *cobra.Command, envelope output.Envelope) error {
	return output.Write(cmd.OutOrStdout(), envelope, jsonOutputFlag(cmd))
}

// writeEnvelopeFromResponse converts a daemon protocol response into the
// unified output envelope. Failed responses become CLI errors so the error
// code is preserved through the unified error classification.
func writeEnvelopeFromResponse(cmd *cobra.Command, response daemon.Response) error {
	if !response.Success {
		return responseError(response)
	}
	warnings := make([]output.Warning, 0, len(response.Warnings))
	for _, warning := range response.Warnings {
		warnings = append(warnings, output.Warning{Kind: warning.Kind, Severity: warning.Severity, Message: warning.Message, Ref: warning.Ref, Excerpt: warning.Excerpt})
	}
	return writeEnvelope(cmd, output.OK(response.Data, warnings))
}

// responseError converts a failed daemon response into a CLIError whose kind
// and exit code follow the unified error-code mapping, preserving the stable
// daemon protocol code.
func responseError(response daemon.Response) error {
	if response.Error == nil {
		return errors.New("daemon request failed")
	}
	code := output.Code(response.Error.Code)
	if !output.IsValid(response.Error.Code) {
		code = output.CodeInternal
	}
	wrapped := exitcodes.Wrapf(response.Error, output.ExitCodeFromCode(code), output.KindFromCode(code), "%s", response.Error.Message)
	if response.Error.Hint != "" {
		wrapped.Hint = response.Error.Hint
	}
	return wrapped
}
