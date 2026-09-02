package state

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
)

// VaultSetter stores one value in a symvault entry without putting the value in
// the subprocess argument vector. The value is supplied through stdin by the
// production implementation.
type VaultSetter func(context.Context, string, string, []byte) ([]byte, error)

// KeyInitResult describes the result of `state key init` without exposing a
// configured key. When no secure provider is available, Instruction contains
// the one-time shell assignment needed to configure the environment fallback.
type KeyInitResult struct {
	Action      string    `json:"action"`
	Configured  bool      `json:"configured"`
	KeySource   KeySource `json:"key_source"`
	Instruction string    `json:"instruction,omitempty"`
}

// Initialize provisions a state-encryption key without rotating an existing
// one. It follows the same provider order as key resolution: symvault, macOS
// keychain, then a shell instruction for SYMBROWSE_ENCRYPTION_KEY.
func (r *KeyResolver) Initialize(ctx context.Context) (KeyInitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key, source, err := r.resolveCached(ctx)
	if err != nil {
		return KeyInitResult{}, fmt.Errorf("check existing state encryption key: %w", err)
	}
	if len(key) != 0 && source != KeySourceNone {
		return KeyInitResult{
			Action:     "already_configured",
			Configured: true,
			KeySource:  source,
		}, nil
	}

	generated := make([]byte, 32)
	if _, err := rand.Read(generated); err != nil {
		return KeyInitResult{}, fmt.Errorf("generate state encryption key: %w", err)
	}
	encoded := hex.EncodeToString(generated)

	if r.LookPath != nil {
		if _, err := r.LookPath("symvault"); err == nil {
			if r.SetVaultContext == nil {
				return KeyInitResult{}, errors.New("symvault is installed but state-key provisioning is unavailable")
			}
			if _, err := r.SetVaultContext(ctx, "symvault", VaultEntryName, generated); err != nil {
				return KeyInitResult{}, fmt.Errorf("provision state encryption key in symvault: %w", err)
			}
			return r.verifyProvisioned(ctx, generated, KeySourceVault)
		}
	}

	if runtime.GOOS == "darwin" && r.KeychainSet != nil && (r.LookPath == nil || hasExecutable(r.LookPath, "security")) {
		if err := r.KeychainSet(KeychainService, KeychainAccount, generated); err != nil {
			return KeyInitResult{}, fmt.Errorf("provision state encryption key in keychain: %w", err)
		}
		return r.verifyProvisioned(ctx, generated, KeySourceKeychain)
	}

	return KeyInitResult{
		Action:      "configure_environment",
		Configured:  false,
		KeySource:   KeySourceEnv,
		Instruction: fmt.Sprintf("export %s='%s'", EnvKeyName, encoded),
	}, nil
}

func hasExecutable(lookPath func(string) (string, error), name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func (r *KeyResolver) verifyProvisioned(ctx context.Context, expected []byte, wantSource KeySource) (KeyInitResult, error) {
	r.Invalidate()
	actual, source, err := r.resolveCached(ctx)
	if err != nil {
		return KeyInitResult{}, fmt.Errorf("verify state encryption key provisioning: %w", err)
	}
	if source != wantSource || !bytes.Equal(actual, expected) {
		return KeyInitResult{}, fmt.Errorf("verify state encryption key provisioning: provider did not return the new key")
	}
	return KeyInitResult{
		Action:     "initialized",
		Configured: true,
		KeySource:  source,
	}, nil
}
