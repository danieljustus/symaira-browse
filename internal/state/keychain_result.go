package state

import (
	"errors"
	"fmt"
	"strings"
)

// keychainExitError is the small part of exec.ExitError needed to classify
// security(1) failures. Keeping the classifier independent of Darwin makes
// its not-found and failure behavior testable on every platform.
type keychainExitError interface {
	ExitCode() int
}

func keychainLookupResult(out []byte, err error) ([]byte, bool, error) {
	if err != nil {
		var exitErr keychainExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("keychain lookup: %w", err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return nil, false, nil
	}
	return []byte(value), true, nil
}
