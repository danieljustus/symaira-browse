package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// VaultCredentials are the resolved username/password pair. The values live
// only in memory and must never be logged, journaled or returned.
type VaultCredentials struct {
	Username string
	Password string
}

// VaultResolver resolves credential entries through the symvault CLI. It is a
// field-based struct so tests can inject fakes; production uses the real
// binary discovered on PATH.
type VaultResolver struct {
	LookPath func(string) (string, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
}

// NewVaultResolver creates a resolver backed by the symvault binary.
func NewVaultResolver() *VaultResolver {
	return &VaultResolver{LookPath: exec.LookPath, Run: runVaultCommand}
}

func runVaultCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// ErrVaultUnavailable is returned when symvault is not installed. The CLI
// maps it to a clear error with setup instructions and never offers a
// plaintext fallback.
var ErrVaultUnavailable = errors.New("symvault is not installed")

// Resolve fetches one vault entry and extracts username/password. Supported
// entry shapes: {"username": ..., "password": ...} JSON, "key: value" lines
// and "key=value" lines. The raw output is never returned to callers.
func (r *VaultResolver) Resolve(ctx context.Context, entry string) (VaultCredentials, error) {
	if _, err := r.LookPath("symvault"); err != nil {
		return VaultCredentials{}, ErrVaultUnavailable
	}
	vaultCtx, cancel := context.WithTimeout(ctx, vaultCommandTimeout)
	defer cancel()
	out, err := r.Run(vaultCtx, "symvault", "get", entry)
	if err != nil {
		return VaultCredentials{}, fmt.Errorf("symvault could not resolve entry %q: %w", entry, err)
	}
	creds, err := parseVaultOutput(string(out))
	if err != nil {
		return VaultCredentials{}, fmt.Errorf("symvault entry %q: %w", entry, err)
	}
	return creds, nil
}

// vaultCommandTimeout bounds vault CLI calls. It is applied by callers that
// pass a context; kept as a constant so the timeout is defined once.
const vaultCommandTimeout = 15 * time.Second

// parseVaultOutput extracts username and password from common symvault
// output shapes.
func parseVaultOutput(raw string) (VaultCredentials, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return VaultCredentials{}, errors.New("entry is empty")
	}
	// JSON envelope: {"username": "...", "password": "..."} (case variants).
	var object map[string]any
	if json.Unmarshal([]byte(raw), &object) == nil && object != nil {
		username, password := vaultField(object, "username"), vaultField(object, "password")
		if username == "" || password == "" {
			return VaultCredentials{}, errors.New("entry has no username/password fields")
		}
		return VaultCredentials{Username: username, Password: password}, nil
	}
	// Line-based: "username: value" / "password = value".
	var username, password string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			key, value, ok = strings.Cut(line, "=")
		}
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "username", "user", "login":
			username = strings.TrimSpace(value)
		case "password", "pass", "secret":
			password = strings.TrimSpace(value)
		}
	}
	if username == "" || password == "" {
		return VaultCredentials{}, errors.New("entry has no username/password fields")
	}
	return VaultCredentials{Username: username, Password: password}, nil
}

func vaultField(object map[string]any, name string) string {
	for key, value := range object {
		if strings.EqualFold(key, name) {
			if text, ok := value.(string); ok {
				return text
			}
		}
	}
	return ""
}

// redactSecrets replaces known secret values in a string. It is used by the
// journal and error paths so credentials never leak into durable output.
func redactSecrets(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "••••")
	}
	return text
}
