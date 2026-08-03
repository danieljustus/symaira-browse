package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// EnvKeyName is the documented fallback environment variable. It must hold a
// 64-character hex string (32 bytes) and is deliberately the last resort.
const EnvKeyName = "SYMBROWSE_ENCRYPTION_KEY"

// VaultEntryName is the symvault entry consulted for the encryption key.
const VaultEntryName = "symbrowse/encryption-key"

// KeychainService and KeychainAccount identify the generic-password item used
// on macOS. Other platforms skip the keychain source.
const (
	KeychainService = "symbrowse"
	KeychainAccount = "encryption-key"
)

// KeyResolver resolves the state-encryption key in the fixed architecture
// order: symvault (runtime detection), OS keychain, environment variable.
// The lookup functions are fields so tests can inject fakes.
type KeyResolver struct {
	LookPath func(string) (string, error)
	RunVault func(string, ...string) ([]byte, error)
	// KeychainGet returns the secret for the given service/account, or
	// (nil, false) when no item exists.
	KeychainGet func(service, account string) ([]byte, bool, error)
	Env         func(string) string
}

// NewKeyResolver creates a resolver with production lookups.
func NewKeyResolver() *KeyResolver {
	return &KeyResolver{
		LookPath:    exec.LookPath,
		RunVault:    runCommand,
		KeychainGet: keychainGet,
		Env:         os.Getenv,
	}
}

func runCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Key implements KeyProvider. It returns KeySourceNone with a nil key when no
// source is configured so callers can fall back to plaintext.
func (r *KeyResolver) Key() ([]byte, KeySource, error) {
	if key, err := r.vaultKey(); err != nil {
		return nil, "", err
	} else if key != nil {
		return key, KeySourceVault, nil
	}
	if key, ok, err := r.KeychainGet(KeychainService, KeychainAccount); err != nil {
		return nil, "", err
	} else if ok {
		return key, KeySourceKeychain, nil
	}
	if raw := r.Env(EnvKeyName); raw != "" {
		key, err := parseHexKey(raw)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", EnvKeyName, err)
		}
		return key, KeySourceEnv, nil
	}
	return nil, KeySourceNone, nil
}

// Source reports which key source would currently be used, without exposing
// the key itself.
func (r *KeyResolver) Source() (KeySource, error) {
	key, source, err := r.Key()
	if err != nil {
		return "", err
	}
	if key == nil {
		return KeySourceNone, nil
	}
	return source, nil
}

// vaultKey resolves the key from symvault when the binary is present and the
// entry exists. A missing binary or missing entry yields (nil, nil) so the
// resolver can continue down the chain.
func (r *KeyResolver) vaultKey() ([]byte, error) {
	if _, err := r.LookPath("symvault"); err != nil {
		return nil, nil
	}
	out, err := r.RunVault("symvault", "get", VaultEntryName)
	if err != nil {
		// symvault present but entry missing or locked: continue the chain.
		return nil, nil
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	// symvault may return a JSON envelope; extract the first 64-hex token.
	key, err := parseHexKey(firstHexToken(raw))
	if err != nil {
		return nil, fmt.Errorf("symvault entry %q: %w", VaultEntryName, err)
	}
	return key, nil
}

// firstHexToken finds the first 64-character hex run in a string.
func firstHexToken(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F')
	})
	for _, field := range fields {
		if len(field) >= 64 {
			return field[:64]
		}
	}
	return ""
}

// parseHexKey validates a 64-character hex string and returns its 32 bytes.
func parseHexKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) != 64 {
		return nil, errors.New("key must be a 64-character hex string (32 bytes)")
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, errors.New("key must be a 64-character hex string (32 bytes)")
	}
	return key, nil
}
