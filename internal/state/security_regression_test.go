package state

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeVaultExitError struct {
	code int
}

func (e fakeVaultExitError) Error() string { return "symvault command failed" }
func (e fakeVaultExitError) ExitCode() int { return e.code }

func TestKeyResolverVaultNotFoundExitFallsBackToEnv(t *testing.T) {
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVault: func(string, ...string) ([]byte, error) {
			return nil, fakeVaultExitError{code: vaultEntryNotFoundExitCode}
		},
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env:         func(string) string { return strings.Repeat("ef", 32) },
	}

	key, source, err := resolver.Key()
	if err != nil {
		t.Fatal(err)
	}
	if source != KeySourceEnv || len(key) != 32 {
		t.Fatalf("source = %q, key len = %d", source, len(key))
	}
}

func TestKeyResolverVaultNotInitializedFallsBackToEnv(t *testing.T) {
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVault: func(string, ...string) ([]byte, error) {
			return nil, fakeVaultExitError{code: vaultNotInitializedExitCode}
		},
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env:         func(string) string { return strings.Repeat("ef", 32) },
	}

	key, source, err := resolver.Key()
	if err != nil {
		t.Fatal(err)
	}
	if source != KeySourceEnv || len(key) != 32 {
		t.Fatalf("source = %q, key len = %d", source, len(key))
	}
}

func TestKeyResolverVaultFailureDoesNotFallback(t *testing.T) {
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVault: func(string, ...string) ([]byte, error) {
			return nil, fakeVaultExitError{code: 4}
		},
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env:         func(string) string { return strings.Repeat("ab", 32) },
	}

	if _, _, err := resolver.Key(); err == nil || !strings.Contains(err.Error(), "symvault command failed") {
		t.Fatalf("vault failure = %v, want propagated error", err)
	}
}

func TestStoreSaveFailsClosedOnKeySourceError(t *testing.T) {
	expected := errors.New("keychain access denied")
	provider := &fakeKeyProvider{source: KeySourceKeychain, err: expected}
	store := newTestStore(t, 30*24*time.Hour, provider)

	if err := store.Save(sampleState("blocked")); !errors.Is(err, expected) {
		t.Fatalf("save error = %v, want %v", err, expected)
	}
	if _, err := os.Stat(store.Dir() + "/blocked.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file after failed save: %v, want not found", err)
	}
}

func TestV3PlaintextReadableWithKeyedStore(t *testing.T) {
	dir := t.TempDir()
	plain, err := NewStore(StoreOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Save(sampleState("plain-v3")); err != nil {
		t.Fatal(err)
	}

	keyed, err := NewStore(StoreOptions{
		Dir:  dir,
		Keys: &fakeKeyProvider{key: testKey(), source: KeySourceEnv},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := keyed.Load("plain-v3")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.KeySource != string(KeySourceNone) || loaded.Name != "plain-v3" {
		t.Fatalf("loaded plaintext state = %#v", loaded)
	}
}
