//go:build !darwin

package state

// keychainGet is unavailable on non-macOS platforms: the resolver skips the
// OS keychain source and continues with the environment variable.
func keychainGet(service, account string) ([]byte, bool, error) {
	return nil, false, nil
}

func keychainSet(service, account string, value []byte) error {
	return nil
}
