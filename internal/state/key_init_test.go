package state

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestKeyResolverInitializeDoesNotRotateExistingKey(t *testing.T) {
	existing := strings.Repeat("ab", 32)
	resolver := &KeyResolver{
		Env: func(name string) string {
			if name == EnvKeyName {
				return existing
			}
			return ""
		},
	}

	result, err := resolver.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "already_configured" || !result.Configured || result.KeySource != KeySourceEnv || result.Instruction != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestKeyResolverInitializeUsesSymvaultBeforeOtherProviders(t *testing.T) {
	var provisioned []byte
	resolver := &KeyResolver{
		LookPath: func(name string) (string, error) {
			if name == "symvault" {
				return "/usr/bin/symvault", nil
			}
			return "", os.ErrNotExist
		},
		RunVault: func(_ string, args ...string) ([]byte, error) {
			if len(args) == 2 && args[0] == "get" && provisioned == nil {
				return nil, os.ErrNotExist
			}
			if len(args) == 2 && args[0] == "get" {
				payload, _ := json.Marshal(map[string]string{
					"name":  VaultEntryName,
					"value": hex.EncodeToString(provisioned),
				})
				return payload, nil
			}
			return nil, errors.New("unexpected symvault command")
		},
		SetVaultContext: func(_ context.Context, name, entry string, key []byte) ([]byte, error) {
			if name != "symvault" || entry != VaultEntryName || len(key) != 32 {
				t.Fatalf("provision request = %q/%q with %d-byte key", name, entry, len(key))
			}
			provisioned = append([]byte(nil), key...)
			return nil, nil
		},
		Env: func(string) string { return "" },
	}

	result, err := resolver.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "initialized" || !result.Configured || result.KeySource != KeySourceVault || len(provisioned) != 32 {
		t.Fatalf("result = %#v, provisioned bytes = %d", result, len(provisioned))
	}
}

func TestKeyResolverInitializeFallsBackToEnvironmentInstruction(t *testing.T) {
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Env:      func(string) string { return "" },
	}

	result, err := resolver.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "configure_environment" || result.Configured || result.KeySource != KeySourceEnv {
		t.Fatalf("result = %#v", result)
	}
	if !strings.HasPrefix(result.Instruction, "export "+EnvKeyName+"='") || !strings.HasSuffix(result.Instruction, "'") {
		t.Fatalf("instruction = %q", result.Instruction)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(result.Instruction, "export "+EnvKeyName+"='"), "'")
	if len(encoded) != 64 {
		t.Fatalf("generated key length = %d, want 64 hex characters", len(encoded))
	}
	if _, err := parseHexKey(encoded); err != nil {
		t.Fatalf("generated key is invalid: %v", err)
	}
}

func TestKeyResolverInitializeUsesMacOSKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS keychain provisioning is only available on Darwin")
	}
	var provisioned []byte
	resolver := &KeyResolver{
		LookPath: func(name string) (string, error) {
			if name == "security" {
				return "/usr/bin/security", nil
			}
			return "", os.ErrNotExist
		},
		KeychainGet: func(_, _ string) ([]byte, bool, error) {
			if provisioned == nil {
				return nil, false, nil
			}
			return append([]byte(nil), provisioned...), true, nil
		},
		KeychainSet: func(_, _ string, key []byte) error {
			provisioned = append([]byte(nil), key...)
			return nil
		},
		Env: func(string) string { return "" },
	}

	result, err := resolver.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "initialized" || !result.Configured || result.KeySource != KeySourceKeychain || len(provisioned) != 32 {
		t.Fatalf("result = %#v, provisioned bytes = %d", result, len(provisioned))
	}
}
