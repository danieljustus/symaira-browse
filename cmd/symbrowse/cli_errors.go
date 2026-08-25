package main

import (
	"errors"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/exitcodes"
)

// invalidArgs classifies command argument and flag validation failures without
// changing the stable human-readable message.
func invalidArgs(format string, args ...any) error {
	return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, format, args...)
}

// normalizeCommandError upgrades Cobra's generic positional-argument and flag
// parser errors to the documented invalid_args contract. Errors already carrying
// an explicit CLI classification are left untouched.
func normalizeCommandError(err error) error {
	if err == nil {
		return nil
	}
	var cliErr *exitcodes.CLIError
	if errors.As(err, &cliErr) {
		return err
	}
	message := err.Error()
	if strings.Contains(message, "arg(s)") || strings.HasPrefix(message, "unknown command ") || strings.HasPrefix(message, "unknown flag ") || strings.Contains(message, "flag provided but not defined") {
		return invalidArgs("%s", message)
	}
	return err
}
