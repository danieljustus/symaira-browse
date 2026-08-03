//go:build darwin

package state

import (
	"errors"
	"os/exec"
	"strings"
)

// keychainGet reads a generic-password item via the macOS security CLI.
// It returns (nil, false) when no item exists so the resolver can continue
// down the key chain.
func keychainGet(service, account string) ([]byte, bool, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// 44 = item not found; any other failure also falls through.
			return nil, false, nil
		}
		return nil, false, nil
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return nil, false, nil
	}
	return []byte(value), true, nil
}
