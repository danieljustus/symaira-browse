package state

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func newTestStore(t *testing.T, expireIn time.Duration, keys KeyProvider) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(StoreOptions{Dir: dir, ExpireIn: expireIn, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return store
}

func sampleState(name string) *State {
	return &State{
		SchemaVersion: SchemaVersion,
		Name:          name,
		Origins: map[string]OriginState{
			"https://example.com": {
				Cookies:      []engine.Cookie{{Name: "session", Value: "s3cret", Domain: ".example.com"}},
				LocalStorage: map[string]string{"token": "t-1"},
			},
		},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store := newTestStore(t, 30*24*time.Hour, nil)
	if err := store.Save(sampleState("demo")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || got.SchemaVersion != SchemaVersion {
		t.Fatalf("state = %#v", got)
	}
	origin := got.Origins["https://example.com"]
	if len(origin.Cookies) != 1 || origin.Cookies[0].Value != "s3cret" {
		t.Fatalf("origin = %#v", origin)
	}
	if origin.LocalStorage["token"] != "t-1" {
		t.Fatalf("local storage = %#v", origin.LocalStorage)
	}
	if got.ExpiresAt == "" || got.SavedAt == "" {
		t.Fatal("timestamps missing")
	}
}

func TestSaveIsAtomicAnd0600(t *testing.T) {
	store := newTestStore(t, 30*24*time.Hour, nil)
	if err := store.Save(sampleState("demo")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.Dir(), "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX mode bits (chmod only toggles read-only).
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("permissions = %o, want 600", perm)
		}
	}
	// No temp files may remain after an atomic write.
	entries, _ := os.ReadDir(store.Dir())
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "tmp") {
			t.Fatalf("leftover temp file %q", entry.Name())
		}
	}
}

func TestMetadataNeverExposesValues(t *testing.T) {
	store := newTestStore(t, 30*24*time.Hour, nil)
	if err := store.Save(sampleState("demo")); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Metadata("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Origins) != 1 {
		t.Fatalf("meta = %#v", meta)
	}
	origin := meta.Origins[0]
	if origin.Origin != "https://example.com" || origin.CookieCount != 1 || origin.LocalKeys != 1 || origin.SessionKeys != 0 {
		t.Fatalf("origin meta = %#v", origin)
	}
	raw, _ := meta.JSON()
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "t-1") {
		t.Fatalf("metadata leaked values: %s", raw)
	}
}

func TestExpiryAndClean(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	store := newTestStore(t, 30*24*time.Hour, nil)
	store.now = func() time.Time { return now }
	if err := store.Save(sampleState("fresh")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sampleState("old")); err != nil {
		t.Fatal(err)
	}
	// Age the "old" state past expiry.
	oldPath := filepath.Join(store.Dir(), "old.json")
	raw, _ := os.ReadFile(oldPath)
	old := strings.Replace(string(raw), now.Add(30*24*time.Hour).UTC().Format(time.RFC3339Nano), now.Add(-time.Hour).UTC().Format(time.RFC3339Nano), 1)
	if old == string(raw) {
		t.Fatal("failed to age state file")
	}
	if err := os.WriteFile(oldPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	expired, err := store.Expired()
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != "old" {
		t.Fatalf("expired = %#v", expired)
	}
	removed, err := store.Clean()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "old" {
		t.Fatalf("removed = %#v", removed)
	}
	names, _ := store.List()
	if len(names) != 1 || names[0] != "fresh" {
		t.Fatalf("names = %#v", names)
	}
}

func TestValidateName(t *testing.T) {
	for _, bad := range []string{"", "../escape", "a/b", "a\x00b", strings.Repeat("x", 129)} {
		if err := ValidateName(bad); err == nil {
			t.Fatalf("name %q accepted", bad)
		}
	}
	for _, good := range []string{"demo", "my-state_1", "A.B-c"} {
		if err := ValidateName(good); err != nil {
			t.Fatalf("name %q rejected: %v", good, err)
		}
	}
}

func TestCorruptFileRejected(t *testing.T) {
	store := newTestStore(t, 30*24*time.Hour, nil)
	if err := os.WriteFile(filepath.Join(store.Dir(), "bad.json"), []byte("not a state file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("bad"); err == nil {
		t.Fatal("corrupt file accepted")
	}
}

// fakeKeyProvider serves a fixed key with a configurable source.
type fakeKeyProvider struct {
	key    []byte
	source KeySource
	err    error
}

func (f *fakeKeyProvider) Key() ([]byte, KeySource, error) { return f.key, f.source, f.err }
func (f *fakeKeyProvider) Source() (KeySource, error)      { return f.source, f.err }

// countingKeyProvider counts calls to Key() and Source().
type countingKeyProvider struct {
	mu       sync.Mutex
	keyCalls int
	srcCalls int
	key      []byte
	source   KeySource
	err      error
}

func (c *countingKeyProvider) Key() ([]byte, KeySource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keyCalls++
	return c.key, c.source, c.err
}

func (c *countingKeyProvider) Source() (KeySource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.srcCalls++
	return c.source, c.err
}

func (c *countingKeyProvider) calls() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyCalls, c.srcCalls
}

func testKey() []byte {
	key, _ := hex.DecodeString(strings.Repeat("ab", 32))
	return key
}

func TestEncryptedRoundTripAndKeySource(t *testing.T) {
	provider := &fakeKeyProvider{key: testKey(), source: KeySourceEnv}
	store := newTestStore(t, 30*24*time.Hour, provider)
	if err := store.Save(sampleState("secret")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir(), "secret.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("plaintext leaked into encrypted state file")
	}
	got, err := store.Load("secret")
	if err != nil {
		t.Fatal(err)
	}
	if got.Origins["https://example.com"].Cookies[0].Value != "s3cret" {
		t.Fatal("round trip lost cookie value")
	}
	source, err := store.KeySource()
	if err != nil || source != KeySourceEnv {
		t.Fatalf("key source = %q, error = %v", source, err)
	}
}

func TestEncryptedFileNotLoadableWithoutKey(t *testing.T) {
	encrypted := newTestStore(t, 30*24*time.Hour, &fakeKeyProvider{key: testKey(), source: KeySourceEnv})
	if err := encrypted.Save(sampleState("secret")); err != nil {
		t.Fatal(err)
	}
	plain := newTestStore(t, 30*24*time.Hour, nil)
	if _, err := plain.Load("secret"); err == nil {
		t.Fatal("encrypted state loaded without key")
	}
	wrongKey := newTestStore(t, 30*24*time.Hour, &fakeKeyProvider{key: []byte(strings.Repeat("x", 32)), source: KeySourceEnv})
	if _, err := wrongKey.Load("secret"); err == nil {
		t.Fatal("encrypted state loaded with wrong key")
	}
}

func TestPlaintextFileSurvivesWithoutVault(t *testing.T) {
	// The fallback store (no symvault, no keychain, no env) must still work.
	store := newTestStore(t, 30*24*time.Hour, nil)
	if err := store.Save(sampleState("plain")); err != nil {
		t.Fatal(err)
	}
	source, err := store.KeySource()
	if err != nil || source != KeySourceNone {
		t.Fatalf("key source = %q, error = %v", source, err)
	}
	if _, err := store.Load("plain"); err != nil {
		t.Fatal(err)
	}
}

func TestKeyResolverOrderAndValidation(t *testing.T) {
	resolver := NewKeyResolver()
	key, source, err := resolver.Key()
	if err != nil {
		t.Fatal(err)
	}
	_ = key
	if source != KeySourceNone {
		t.Fatalf("unexpected source without any key configured: %q", source)
	}
	if _, err := parseHexKey("short"); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := parseHexKey("zz" + strings.Repeat("ab", 31)); err == nil {
		t.Fatal("non-hex key accepted")
	}
	key, err = parseHexKey(strings.Repeat("cd", 32))
	if err != nil || len(key) != 32 {
		t.Fatalf("valid key rejected: %v", err)
	}
}

func TestKeyResolverVaultFallback(t *testing.T) {
	// symvault present but entry missing -> continue to env.
	resolver := &KeyResolver{
		LookPath:    func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVault:    func(string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
		KeychainGet: func(service, account string) ([]byte, bool, error) { return nil, false, nil },
		Env: func(name string) string {
			if name == EnvKeyName {
				return strings.Repeat("ef", 32)
			}
			return ""
		},
	}
	key, source, err := resolver.Key()
	if err != nil {
		t.Fatal(err)
	}
	if source != KeySourceEnv || len(key) != 32 {
		t.Fatalf("source = %q, key len = %d", source, len(key))
	}
}

func TestKeyResolverVaultPresent(t *testing.T) {
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVault: func(string, ...string) ([]byte, error) {
			return []byte(`{"name":"symbrowse/encryption-key","value":"` + strings.Repeat("ab", 32) + `"}`), nil
		},
		KeychainGet: func(service, account string) ([]byte, bool, error) { return nil, false, nil },
		Env:         func(string) string { return "" },
	}
	key, source, err := resolver.Key()
	if err != nil {
		t.Fatal(err)
	}
	if source != KeySourceVault || len(key) != 32 {
		t.Fatalf("source = %q, key len = %d", source, len(key))
	}
}

func TestKeyResolverKeychainHexValue(t *testing.T) {
	resolver := &KeyResolver{
		KeychainGet: func(service, account string) ([]byte, bool, error) {
			if service != KeychainService || account != KeychainAccount {
				t.Fatalf("unexpected keychain lookup: %s/%s", service, account)
			}
			return []byte(strings.Repeat("ab", 32)), true, nil
		},
		Env: func(string) string { return "" },
	}
	key, source, err := resolver.Key()
	if err != nil {
		t.Fatal(err)
	}
	if source != KeySourceKeychain || len(key) != 32 {
		t.Fatalf("source = %q, key len = %d", source, len(key))
	}
}

func TestKeyResolverInvalidKeychainValue(t *testing.T) {
	resolver := &KeyResolver{
		KeychainGet: func(string, string) ([]byte, bool, error) {
			return []byte("not-a-key"), true, nil
		},
	}
	if _, _, err := resolver.Key(); err == nil || !strings.Contains(err.Error(), "keychain value") {
		t.Fatalf("invalid keychain value error = %v", err)
	}
}

func TestKeychainFallbackToEnv(t *testing.T) {
	resolver := &KeyResolver{
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		RunVault:    func(string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
		KeychainGet: func(service, account string) ([]byte, bool, error) { return nil, false, nil },
		Env:         func(name string) string { return strings.Repeat("01", 32) },
	}
	key, source, err := resolver.Key()
	if err != nil {
		t.Fatal(err)
	}
	if source != KeySourceEnv || len(key) != 32 {
		t.Fatalf("source = %q, key len = %d", source, len(key))
	}
}

func TestKeyResolverMemoization(t *testing.T) {
	var envCalls int
	resolver := &KeyResolver{
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env: func(name string) string {
			if name == EnvKeyName {
				envCalls++
				return strings.Repeat("ab", 32)
			}
			return ""
		},
	}

	for i := 0; i < 5; i++ {
		key, source, err := resolver.Key()
		if err != nil {
			t.Fatal(err)
		}
		if len(key) != 32 || source != KeySourceEnv {
			t.Fatalf("unexpected key/source: len=%d source=%s", len(key), source)
		}
	}
	for i := 0; i < 5; i++ {
		source, err := resolver.Source()
		if err != nil {
			t.Fatal(err)
		}
		if source != KeySourceEnv {
			t.Fatalf("unexpected source: %s", source)
		}
	}
	if envCalls != 1 {
		t.Fatalf("expected 1 env resolution call, got %d", envCalls)
	}
}

func TestKeyResolverInvalidationAndReset(t *testing.T) {
	var envCalls int
	resolver := &KeyResolver{
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env: func(name string) string {
			if name == EnvKeyName {
				envCalls++
				return strings.Repeat("cd", 32)
			}
			return ""
		},
	}

	if _, _, err := resolver.Key(); err != nil {
		t.Fatal(err)
	}
	if envCalls != 1 {
		t.Fatalf("calls = %d, want 1", envCalls)
	}

	// Invalidate clears cache
	resolver.Invalidate()
	if _, _, err := resolver.Key(); err != nil {
		t.Fatal(err)
	}
	if envCalls != 2 {
		t.Fatalf("calls after Invalidate = %d, want 2", envCalls)
	}

	// Reset also clears cache
	resolver.Reset()
	if _, err := resolver.Source(); err != nil {
		t.Fatal(err)
	}
	if envCalls != 3 {
		t.Fatalf("calls after Reset = %d, want 3", envCalls)
	}
}

func TestKeyResolverTransientErrorNotCached(t *testing.T) {
	attempts := 0
	resolver := &KeyResolver{
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env: func(name string) string {
			attempts++
			if attempts == 1 {
				return "invalid-hex"
			}
			return strings.Repeat("ef", 32)
		},
	}

	// First call should fail with invalid hex error
	if _, _, err := resolver.Key(); err == nil {
		t.Fatal("expected error on first call")
	}

	// Second call should succeed because error was not cached
	key, source, err := resolver.Key()
	if err != nil {
		t.Fatalf("expected success on retry: %v", err)
	}
	if len(key) != 32 || source != KeySourceEnv {
		t.Fatalf("unexpected key/source on retry: len=%d source=%s", len(key), source)
	}

	// Third call should use cached result (attempts remains 2)
	if _, _, err := resolver.Key(); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 provider calls, got %d", attempts)
	}
}

func TestKeyResolverConcurrentAccess(t *testing.T) {
	var envCalls int32
	resolver := &KeyResolver{
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env: func(name string) string {
			if name == EnvKeyName {
				atomic.AddInt32(&envCalls, 1)
				return strings.Repeat("12", 32)
			}
			return ""
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				key, source, err := resolver.Key()
				if err != nil || len(key) != 32 || source != KeySourceEnv {
					t.Errorf("concurrent Key() failed: %v", err)
				}
				src, err := resolver.Source()
				if err != nil || src != KeySourceEnv {
					t.Errorf("concurrent Source() failed: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&envCalls) != 1 {
		t.Fatalf("expected 1 resolution call under concurrency, got %d", envCalls)
	}
}

func TestStoreSavesAndLoadsResolveOnce(t *testing.T) {
	var envCalls int
	resolver := &KeyResolver{
		LookPath:    func(string) (string, error) { return "", os.ErrNotExist },
		KeychainGet: func(string, string) ([]byte, bool, error) { return nil, false, nil },
		Env: func(name string) string {
			if name == EnvKeyName {
				envCalls++
				return strings.Repeat("34", 32)
			}
			return ""
		},
	}
	store := newTestStore(t, 30*24*time.Hour, resolver)

	// Save 5 states and Load 5 states
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("state-%d", i)
		if err := store.Save(sampleState(name)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("state-%d", i)
		got, err := store.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != name {
			t.Fatalf("got name %q, want %q", got.Name, name)
		}
	}

	if envCalls != 1 {
		t.Fatalf("expected exactly 1 key resolution across all saves and loads, got %d", envCalls)
	}
}

func TestCleanAndExpiredWithoutKeyProviderOnV2(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	provider := &fakeKeyProvider{key: testKey(), source: KeySourceEnv}
	store := newTestStore(t, 30*24*time.Hour, provider)
	store.now = func() time.Time { return now }

	if err := store.Save(sampleState("fresh")); err != nil {
		t.Fatal(err)
	}
	rewriteStateSchema(t, store.Dir(), "fresh", 2)
	if err := store.Save(sampleState("expired-1")); err != nil {
		t.Fatal(err)
	}
	rewriteStateSchema(t, store.Dir(), "expired-1", 2)

	// Age expired-1 in unencrypted header
	p := filepath.Join(store.Dir(), "expired-1.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	aged := strings.Replace(string(raw), now.Add(30*24*time.Hour).UTC().Format(time.RFC3339Nano), now.Add(-time.Hour).UTC().Format(time.RFC3339Nano), 1)
	if aged == string(raw) {
		t.Fatal("failed to replace timestamp in header")
	}
	if err := os.WriteFile(p, []byte(aged), 0o600); err != nil {
		t.Fatal(err)
	}

	// Now create a store with NO key provider (keys: nil) and check Clean/Expired
	plainStore, err := NewStore(StoreOptions{Dir: store.Dir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	expiredList, err := plainStore.Expired()
	if err != nil {
		t.Fatalf("Expired() failed without key provider: %v", err)
	}
	if len(expiredList) != 1 || expiredList[0] != "expired-1" {
		t.Fatalf("Expired() = %v, want [expired-1]", expiredList)
	}

	cleaned, err := plainStore.Clean()
	if err != nil {
		t.Fatalf("Clean() failed without key provider: %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "expired-1" {
		t.Fatalf("Clean() = %v, want [expired-1]", cleaned)
	}

	remaining, _ := plainStore.List()
	if len(remaining) != 1 || remaining[0] != "fresh" {
		t.Fatalf("remaining states = %v, want [fresh]", remaining)
	}
}

func TestCleanAndExpiredAuthenticatesEncryptedHeader(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	provider := &countingKeyProvider{key: testKey(), source: KeySourceEnv}
	store := newTestStore(t, 30*24*time.Hour, provider)
	store.now = func() time.Time { return now }

	if err := store.Save(sampleState("s1")); err != nil {
		t.Fatal(err)
	}
	keyCallsAfterSave, _ := provider.calls()

	// v3 authenticates the header before retention decisions.
	if _, err := store.Expired(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Clean(); err != nil {
		t.Fatal(err)
	}

	keyCallsAfterClean, _ := provider.calls()
	if keyCallsAfterClean != keyCallsAfterSave+2 {
		t.Fatalf("Clean/Expired invoked Key() %d times, expected 2", keyCallsAfterClean-keyCallsAfterSave)
	}
}

func TestCleanOlderThanWithoutKeyProvider(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	provider := &fakeKeyProvider{key: testKey(), source: KeySourceVault}
	store := newTestStore(t, 30*24*time.Hour, provider)
	store.now = func() time.Time { return now }

	if err := store.Save(sampleState("recent")); err != nil {
		t.Fatal(err)
	}
	rewriteStateSchema(t, store.Dir(), "recent", 2)
	store.now = func() time.Time { return now.Add(-40 * 24 * time.Hour) }
	if err := store.Save(sampleState("ancient")); err != nil {
		t.Fatal(err)
	}
	rewriteStateSchema(t, store.Dir(), "ancient", 2)

	// Clean older than 10 days without a key provider.
	plainStore, err := NewStore(StoreOptions{Dir: store.Dir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := plainStore.CleanOlderThan(10 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanOlderThan failed: %v", err)
	}
	if len(removed) != 1 || removed[0] != "ancient" {
		t.Fatalf("removed = %v, want [ancient]", removed)
	}
}

func TestV1FileCompatibilityAndCleanErrorOnDecryptFailure(t *testing.T) {
	storeDir := t.TempDir()
	v1Key := testKey()
	codec := &gcmCodec{keys: &fakeKeyProvider{key: v1Key, source: KeySourceEnv}}

	// 1. Create a v1 plaintext file: fileMagic + JSON (no header newline, schema_version 1)
	v1PlainState := sampleState("v1plain")
	v1PlainState.SchemaVersion = 1
	v1PlainJSON, _ := json.Marshal(v1PlainState)
	v1PlainData := append([]byte{}, fileMagic...)
	v1PlainData = append(v1PlainData, v1PlainJSON...)
	if err := os.WriteFile(filepath.Join(storeDir, "v1plain.json"), v1PlainData, 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Create a v1 encrypted file: fileMagic + GCM ciphertext (no header newline)
	v1EncState := sampleState("v1enc")
	v1EncState.SchemaVersion = 1
	v1EncJSON, _ := json.Marshal(v1EncState)
	v1EncCiphertext, err := codec.Encrypt(v1EncJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	v1EncData := append([]byte{}, fileMagic...)
	v1EncData = append(v1EncData, v1EncCiphertext...)
	if err := os.WriteFile(filepath.Join(storeDir, "v1enc.json"), v1EncData, 0o600); err != nil {
		t.Fatal(err)
	}

	// Test loading with matching key
	matchingStore, err := NewStore(StoreOptions{Dir: storeDir, Keys: &fakeKeyProvider{key: v1Key, source: KeySourceEnv}})
	if err != nil {
		t.Fatal(err)
	}

	loadedPlain, err := matchingStore.Load("v1plain")
	if err != nil {
		t.Fatalf("failed to load v1 plaintext file: %v", err)
	}
	if loadedPlain.SchemaVersion != 1 || loadedPlain.Origins["https://example.com"].Cookies[0].Value != "s3cret" {
		t.Fatalf("v1plain load content mismatch: %#v", loadedPlain)
	}

	loadedEnc, err := matchingStore.Load("v1enc")
	if err != nil {
		t.Fatalf("failed to load v1 encrypted file: %v", err)
	}
	if loadedEnc.SchemaVersion != 1 || loadedEnc.Origins["https://example.com"].Cookies[0].Value != "s3cret" {
		t.Fatalf("v1enc load content mismatch: %#v", loadedEnc)
	}

	// Test Clean with wrong key on v1 encrypted file -> MUST report error
	wrongStore, err := NewStore(StoreOptions{Dir: storeDir, Keys: &fakeKeyProvider{key: []byte(strings.Repeat("w", 32)), source: KeySourceEnv}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongStore.Clean(); err == nil {
		t.Fatal("expected Clean() to fail on v1 encrypted file with wrong key, but it succeeded")
	}
}

func TestMalformedFileCleanAndExpiredErrors(t *testing.T) {
	storeDir := t.TempDir()
	store, err := NewStore(StoreOptions{Dir: storeDir})
	if err != nil {
		t.Fatal(err)
	}

	// Write a file with invalid magic
	if err := os.WriteFile(filepath.Join(storeDir, "badmagic.json"), []byte("NOT_A_STATE_FILE"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Clean(); err == nil {
		t.Fatal("Clean() should return error for bad magic file")
	}
	if _, err := store.Expired(); err == nil {
		t.Fatal("Expired() should return error for bad magic file")
	}
}

func TestHeaderNeverExposesSensitiveValuesDetailed(t *testing.T) {
	provider := &fakeKeyProvider{key: testKey(), source: KeySourceVault}
	store := newTestStore(t, 30*24*time.Hour, provider)

	sensitiveState := &State{
		Name: "topsecret",
		Origins: map[string]OriginState{
			"https://secure.bank.com": {
				Cookies: []engine.Cookie{
					{Name: "auth_token", Value: "super_secret_cookie_value_12345", Domain: ".bank.com"},
				},
				LocalStorage:   map[string]string{"secret_jwt": "jwt_token_payload_xyz"},
				SessionStorage: map[string]string{"session_key": "sess_val_999"},
			},
		},
	}

	if err := store.Save(sensitiveState); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(store.Dir(), "topsecret.json"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasPrefix(raw, fileMagic) {
		t.Fatal("missing file magic")
	}

	content := raw[len(fileMagic):]
	newlineIdx := bytes.IndexByte(content, '\n')
	if newlineIdx == -1 {
		t.Fatal("missing header newline delimiter")
	}

	headerBytes := content[:newlineIdx]
	var hdr stateHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		t.Fatalf("failed to unmarshal unencrypted header: %v", err)
	}

	if hdr.SchemaVersion != SchemaVersion || hdr.SavedAt == "" || hdr.ExpiresAt == "" || hdr.KeySource != string(KeySourceVault) {
		t.Fatalf("header fields mismatch: %#v", hdr)
	}

	// Verify header bytes contain no sensitive strings
	headerStr := string(headerBytes)
	sensitiveStrings := []string{
		"super_secret_cookie_value_12345",
		"auth_token",
		"secret_jwt",
		"jwt_token_payload_xyz",
		"session_key",
		"sess_val_999",
		"secure.bank.com",
		"origins",
		"cookies",
		"local_storage",
		"session_storage",
	}
	for _, s := range sensitiveStrings {
		if strings.Contains(headerStr, s) {
			t.Fatalf("unencrypted header leaked sensitive data %q: %s", s, headerStr)
		}
	}

	// Verify entire encrypted file body contains no sensitive values
	rawStr := string(raw)
	for _, s := range []string{"super_secret_cookie_value_12345", "jwt_token_payload_xyz", "sess_val_999"} {
		if strings.Contains(rawStr, s) {
			t.Fatalf("raw encrypted file leaked sensitive data %q", s)
		}
	}
}

func TestKeyResolverVaultCancellationReachesRunner(t *testing.T) {
	started := make(chan struct{})
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVaultContext: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := resolver.resolveCached(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("resolution error = %v, want context cancellation", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("vault runner was not called")
	}
}

func TestStoreSaveResolverErrorDoesNotWritePlaintext(t *testing.T) {
	lookupErr := errors.New("keychain unavailable")
	store := newTestStore(t, 30*24*time.Hour, &fakeKeyProvider{err: lookupErr})
	if err := store.Save(sampleState("no-downgrade")); err == nil || !errors.Is(err, lookupErr) {
		t.Fatalf("Save() error = %v, want resolver error", err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "no-downgrade.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file error = %v, want no file", err)
	}
	if _, err := store.KeySource(); !errors.Is(err, lookupErr) {
		t.Fatalf("KeySource() error = %v, want resolver error", err)
	}
}

func TestKeyResolverVaultLookupErrorPropagates(t *testing.T) {
	lookupErr := errors.New("vault lookup failed")
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVaultContext: func(context.Context, string, ...string) ([]byte, error) {
			return nil, lookupErr
		},
	}
	if _, _, err := resolver.Key(); !errors.Is(err, lookupErr) {
		t.Fatalf("Key() error = %v, want vault lookup error", err)
	}
	if _, err := resolver.Source(); !errors.Is(err, lookupErr) {
		t.Fatalf("Source() error = %v, want vault lookup error", err)
	}
}

func TestStoreResaveEncryptedStateWithoutKeyWarnsAndDowngradesExplicitly(t *testing.T) {
	dir := t.TempDir()
	encrypted, err := NewStore(StoreOptions{Dir: dir, Keys: &fakeKeyProvider{key: testKey(), source: KeySourceEnv}})
	if err != nil {
		t.Fatal(err)
	}
	if err := encrypted.Save(sampleState("resave")); err != nil {
		t.Fatal(err)
	}
	plain, err := NewStore(StoreOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Save(sampleState("resave")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "resave.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "s3cret") {
		t.Fatal("re-saved plaintext state does not contain expected plaintext payload")
	}
}

func TestStoreResaveLoadedEncryptedStateClearsKeySourceHeader(t *testing.T) {
	dir := t.TempDir()
	keyed, err := NewStore(StoreOptions{Dir: dir, Keys: &fakeKeyProvider{key: testKey(), source: KeySourceEnv}})
	if err != nil {
		t.Fatal(err)
	}
	if err := keyed.Save(sampleState("loaded-resave")); err != nil {
		t.Fatal(err)
	}
	loaded, err := keyed.Load("loaded-resave")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := NewStore(StoreOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Save(loaded); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "loaded-resave.json"))
	if err != nil {
		t.Fatal(err)
	}
	data := raw[len(fileMagic):]
	newlineIdx := bytes.IndexByte(data, '\n')
	if newlineIdx == -1 {
		t.Fatal("missing state header")
	}
	var header stateHeader
	if err := json.Unmarshal(data[:newlineIdx], &header); err != nil {
		t.Fatal(err)
	}
	if header.KeySource != string(KeySourceNone) {
		t.Fatalf("key source header = %q, want none", header.KeySource)
	}
	if _, err := plain.Load("loaded-resave"); err != nil {
		t.Fatalf("plain re-saved state could not be loaded: %v", err)
	}
}

func TestKeyResolverConcurrentSourceAndKeyShareVaultResolution(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	resolver := &KeyResolver{
		LookPath: func(string) (string, error) { return "/usr/bin/symvault", nil },
		RunVaultContext: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			calls.Add(1)
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
				return []byte(strings.Repeat("ab", 32)), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	keyResult := make(chan error, 1)
	sourceResult := make(chan error, 1)
	go func() {
		key, source, err := resolver.Key()
		if err == nil && (len(key) != 32 || source != KeySourceVault) {
			err = fmt.Errorf("key len=%d source=%s", len(key), source)
		}
		keyResult <- err
	}()
	<-started
	go func() {
		source, err := resolver.Source()
		if err == nil && source != KeySourceVault {
			err = fmt.Errorf("source=%s", source)
		}
		sourceResult <- err
	}()
	close(release)
	if err := <-keyResult; err != nil {
		t.Fatalf("Key() error = %v", err)
	}
	if err := <-sourceResult; err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("vault calls = %d, want 1", got)
	}
}
