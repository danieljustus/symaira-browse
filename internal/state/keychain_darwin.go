//go:build darwin

package state

import (
	"encoding/hex"
	"os/exec"
)

// runKeychainCommand is a seam for Darwin-focused tests and keeps the
// security CLI invocation isolated from result classification.
var runKeychainCommand = func(args ...string) ([]byte, error) {
	return exec.Command("security", args...).Output()
}

// keychainGet reads a generic-password item via the macOS security CLI.
// security exit code 44 means the item is absent; every other failure is
// returned so callers do not silently downgrade to plaintext.
func keychainGet(service, account string) ([]byte, bool, error) {
	out, err := runKeychainCommand("find-generic-password", "-s", service, "-a", account, "-w")
	return keychainLookupResult(out, err)
}

// keychainSet stores a generic-password item without replacing an existing
// item. A duplicate therefore fails closed instead of rotating the key.
func keychainSet(service, account string, value []byte) error {
	_, err := runKeychainCommand("add-generic-password", "-s", service, "-a", account, "-w", hex.EncodeToString(value))
	return err
}
