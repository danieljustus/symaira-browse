package state

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
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
	if store.KeySource() != KeySourceEnv {
		t.Fatalf("key source = %q", store.KeySource())
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
	if store.KeySource() != KeySourceNone {
		t.Fatalf("key source = %q", store.KeySource())
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
