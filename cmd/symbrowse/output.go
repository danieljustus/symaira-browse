package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// resolveOutputFormat reports the requested envelope format for a command.
// The global --json flag is shorthand for --output json and wins over an
// explicit --output value; --output accepts text, json or yaml. The flags are
// read from the root persistent flag set so the resolution also works when a
// command is driven without cobra Execute (unit tests).
func resolveOutputFormat(cmd *cobra.Command) (output.Format, error) {
	flags := cmd.Root().PersistentFlags()
	if value, err := flags.GetBool("json"); err == nil && value {
		return output.FormatJSON, nil
	}
	format, err := flags.GetString("output")
	if err != nil {
		return output.FormatText, err
	}
	switch output.Format(format) {
	case output.FormatText, output.FormatJSON, output.FormatYAML:
		return output.Format(format), nil
	default:
		return output.FormatText, fmt.Errorf("invalid --output format %q: want text, json or yaml", format)
	}
}

// structuredOutput reports whether the command should emit the unified
// machine-readable envelope (json or yaml) instead of the human-readable text
// rendering. It mirrors resolveOutputFormat without the error plumbing; an
// invalid --output value falls back to false so the human-readable path can
// surface the validation failure.
func structuredOutput(cmd *cobra.Command) bool {
	format, err := resolveOutputFormat(cmd)
	return err == nil && format != output.FormatText
}

// writeEnvelope writes one unified envelope to the command output.
func writeEnvelope(cmd *cobra.Command, envelope output.Envelope) error {
	format, err := resolveOutputFormat(cmd)
	if err != nil {
		return err
	}
	return output.Write(cmd.OutOrStdout(), envelope, format)
}

// writeErrorEnvelope writes one unified failure envelope to the command
// output, falling back to text when the format flag itself is invalid.
func writeErrorEnvelope(cmd *cobra.Command, err error) error {
	format, formatErr := resolveOutputFormat(cmd)
	if formatErr != nil {
		format = output.FormatText
	}
	return output.WriteError(cmd.OutOrStdout(), err, format)
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
