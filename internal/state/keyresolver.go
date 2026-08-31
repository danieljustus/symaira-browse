package state

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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
// Resolution results are cached behind a mutex so providers are called at
// most once per resolver lifetime unless Invalidate or Reset is called.
type KeyResolver struct {
	LookPath func(string) (string, error)
	// RunVault is the legacy test seam. New code should use RunVaultContext so
	// cancellation reaches the subprocess.
	RunVault func(string, ...string) ([]byte, error)
	// RunVaultContext is the context-aware symvault test seam.
	RunVaultContext VaultRunner
	// KeychainGet returns the secret for the given service/account, or
	// (nil, false) when no item exists.
	KeychainGet func(service, account string) ([]byte, bool, error)
	Env         func(string) string

	mu           sync.Mutex
	resolved     bool
	resolving    bool
	wait         chan struct{}
	generation   uint64
	cachedKey    []byte
	cachedSource KeySource
}

// NewKeyResolver creates a resolver with production lookups.
func NewKeyResolver() *KeyResolver {
	return &KeyResolver{
		LookPath:        exec.LookPath,
		RunVaultContext: RunVaultCommand,
		KeychainGet:     keychainGet,
		Env:             os.Getenv,
	}
}

// Key implements KeyProvider. It returns KeySourceNone with a nil key when no
// source is configured so callers can fall back to plaintext.
// Results are cached after the first successful resolution.
func (r *KeyResolver) Key() ([]byte, KeySource, error) {
	return r.resolveCached(context.Background())
}

// Source reports which key source would currently be used, without exposing
// the key itself. It uses the cached resolution if available.
func (r *KeyResolver) Source() (KeySource, error) {
	_, source, err := r.resolveCached(context.Background())
	return source, err
}

// Invalidate clears the cached key resolution so subsequent Key/Source calls
// re-query the underlying key providers.
func (r *KeyResolver) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolved = false
	r.cachedKey = nil
	r.cachedSource = ""
	r.generation++
}

// Reset clears the cached key resolution; alias for Invalidate.
func (r *KeyResolver) Reset() {
	r.Invalidate()
}

// resolveCached serializes resolution attempts without holding the mutex while
// a provider or subprocess runs. Concurrent callers wait for the one in-flight
// resolution and receive the same safely published result.
func (r *KeyResolver) resolveCached(ctx context.Context) ([]byte, KeySource, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		r.mu.Lock()
		if r.resolved {
			key, source := append([]byte(nil), r.cachedKey...), r.cachedSource
			r.mu.Unlock()
			return key, source, nil
		}
		if !r.resolving {
			r.resolving = true
			r.wait = make(chan struct{})
			wait := r.wait
			generation := r.generation
			r.mu.Unlock()

			key, source, err := r.resolve(ctx)

			r.mu.Lock()
			if err == nil && generation == r.generation {
				r.resolved = true
				r.cachedKey = append([]byte(nil), key...)
				r.cachedSource = source
			}
			r.resolving = false
			close(wait)
			r.mu.Unlock()
			if err != nil {
				return nil, "", err
			}
			return append([]byte(nil), key...), source, nil
		}
		wait := r.wait
		r.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
}

func (r *KeyResolver) resolve(ctx context.Context) ([]byte, KeySource, error) {
	if r.LookPath != nil && (r.RunVaultContext != nil || r.RunVault != nil) {
		if key, err := r.vaultKey(ctx); err != nil {
			return nil, "", err
		} else if key != nil {
			return key, KeySourceVault, nil
		}
	}
	if r.KeychainGet != nil {
		if raw, ok, err := r.KeychainGet(KeychainService, KeychainAccount); err != nil {
			return nil, "", err
		} else if ok {
			key, err := parseKeychainKey(raw)
			if err != nil {
				return nil, "", fmt.Errorf("%s/%s: %w", KeychainService, KeychainAccount, err)
			}
			return key, KeySourceKeychain, nil
		}
	}
	if r.Env != nil {
		if raw := r.Env(EnvKeyName); raw != "" {
			key, err := parseHexKey(raw)
			if err != nil {
				return nil, "", fmt.Errorf("%s: %w", EnvKeyName, err)
			}
			return key, KeySourceEnv, nil
		}
	}
	return nil, KeySourceNone, nil
}

// vaultKey resolves the key from symvault when the binary is present and the
// entry exists. A missing binary or missing entry yields (nil, nil) so the
// resolver can continue down the chain. Context cancellation and timeout are
// errors because silently falling back could cause an unsafe save.
func (r *KeyResolver) vaultKey(ctx context.Context) ([]byte, error) {
	runner := r.RunVaultContext
	if runner == nil && r.RunVault != nil {
		runner = func(_ context.Context, name string, args ...string) ([]byte, error) {
			return r.RunVault(name, args...)
		}
	}
	out, err := LookupVault(ctx, r.LookPath, runner, VaultEntryName)
	if err != nil {
		if errors.Is(err, ErrVaultUnavailable) || errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == vaultEntryNotFoundExitCode || exitErr.ExitCode() == vaultNotInitializedExitCode) {
			// ExitNotFound and ExitNotInitialized mean no usable entry is
			// configured; continue the documented resolution chain. Every
			// other command failure is surfaced so a locked or denied vault
			// cannot silently downgrade.
			return nil, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("symvault entry %q: %w", VaultEntryName, err)
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
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		return !isHex
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

// parseKeychainKey accepts the documented 64-character hex form and the raw
// 32-byte form returned by defensive test doubles or alternate keychain APIs.
func parseKeychainKey(raw []byte) ([]byte, error) {
	if len(raw) == 32 {
		return append([]byte(nil), raw...), nil
	}
	key, err := parseHexKey(string(raw))
	if err != nil {
		return nil, errors.New("keychain value must be a 64-character hex string or 32 raw bytes")
	}
	return key, nil
}
