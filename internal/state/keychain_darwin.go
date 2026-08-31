//go:build darwin

package state

import "os/exec"

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
