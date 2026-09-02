package state

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os/exec"
	"time"
)

// VaultCommandTimeout is the maximum time allowed for one symvault CLI lookup.
// Both daemon credential and state-key lookups use this same bound.
const (
	VaultCommandTimeout         = 15 * time.Second
	vaultEntryNotFoundExitCode  = 2 // symvault's public ExitNotFound contract
	vaultNotInitializedExitCode = 3 // no vault exists yet; continue fallback
)

// ErrVaultUnavailable indicates that symvault is not installed or cannot be
// discovered on PATH.
var ErrVaultUnavailable = errors.New("symvault is not installed")

// VaultRunner executes one symvault command. It is a function type so callers
// can inject a test runner while production still uses exec.CommandContext.
type VaultRunner func(context.Context, string, ...string) ([]byte, error)

// RunVaultCommand runs a command with the supplied context. It is shared by
// all symvault integrations so cancellation reaches the subprocess.
func RunVaultCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// LookupVault discovers symvault and retrieves one entry using the shared
// timeout. The caller owns the entry parser because credentials and encryption
// keys intentionally have different output shapes.
func LookupVault(ctx context.Context, lookPath func(string) (string, error), run VaultRunner, entry string) ([]byte, error) {
	if lookPath == nil || run == nil {
		return nil, ErrVaultUnavailable
	}
	if _, err := lookPath("symvault"); err != nil {
		return nil, ErrVaultUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	vaultCtx, cancel := context.WithTimeout(ctx, VaultCommandTimeout)
	defer cancel()
	out, err := run(vaultCtx, "symvault", "get", entry)
	if err != nil && vaultCtx.Err() != nil {
		return nil, vaultCtx.Err()
	}
	return out, err
}

// RunVaultSetCommand stores a state-encryption key in symvault. The key is
// passed through stdin so it never appears in the child process arguments.
func RunVaultSetCommand(ctx context.Context, name, entry string, key []byte) ([]byte, error) {
	encoded := []byte(hex.EncodeToString(key) + "\n")
	command := exec.CommandContext(ctx, name, "set", entry, "--stdin-value")
	command.Stdin = bytes.NewReader(encoded)
	return command.Output()
}
