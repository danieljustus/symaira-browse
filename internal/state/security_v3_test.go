package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV3EncryptedHeaderAuthentication(t *testing.T) {
	now := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	store := newTestStore(t, 30*24*time.Hour, &fakeKeyProvider{key: testKey(), source: KeySourceKeychain})
	store.now = func() time.Time { return now }
	if err := store.Save(sampleState("authenticated")); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(store.Dir(), "authenticated.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldExpiry := now.Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	newExpiry := now.Add(31 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	tampered := bytes.Replace(raw, []byte(oldExpiry), []byte(newExpiry), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatal("failed to tamper with the authenticated header")
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load("authenticated"); err == nil || !strings.Contains(err.Error(), "decrypt state file") {
		t.Fatalf("tampered header load error = %v", err)
	}
	if _, err := store.Expired(); err == nil || !strings.Contains(err.Error(), "authenticate state header") {
		t.Fatalf("tampered header expiry error = %v", err)
	}
}

func TestV3EncryptedStoreRejectsPlaintextDowngrade(t *testing.T) {
	provider := &fakeKeyProvider{key: testKey(), source: KeySourceEnv}
	store := newTestStore(t, 30*24*time.Hour, provider)
	if err := store.Save(sampleState("downgrade")); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(store.Dir(), "downgrade.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data := raw[len(fileMagic):]
	newlineIdx := bytes.IndexByte(data, '\n')
	if newlineIdx == -1 {
		t.Fatal("missing v3 header delimiter")
	}
	var header stateHeader
	if err := json.Unmarshal(data[:newlineIdx], &header); err != nil {
		t.Fatal(err)
	}
	header.KeySource = string(KeySourceNone)
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := json.Marshal(sampleState("downgrade"))
	if err != nil {
		t.Fatal(err)
	}
	forged := append([]byte{}, fileMagic...)
	forged = append(forged, headerBytes...)
	forged = append(forged, '\n')
	forged = append(forged, plaintext...)
	if err := os.WriteFile(path, forged, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load("downgrade"); err == nil || !strings.Contains(err.Error(), "decrypt state file") {
		t.Fatalf("plaintext downgrade was accepted: %v", err)
	}
}

func TestV2EncryptedFileRemainsReadable(t *testing.T) {
	provider := &fakeKeyProvider{key: testKey(), source: KeySourceEnv}
	codec := &gcmCodec{keys: provider}
	state := sampleState("legacy-v2")
	state.SchemaVersion = 2
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	header := stateHeader{
		SchemaVersion: 2,
		SavedAt:       "2026-08-25T14:00:00Z",
		ExpiresAt:     "2026-09-24T14:00:00Z",
		KeySource:     string(KeySourceEnv),
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	body, err := codec.Encrypt(payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte{}, fileMagic...)
	raw = append(raw, headerBytes...)
	raw = append(raw, '\n')
	raw = append(raw, body...)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "legacy-v2.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(StoreOptions{Dir: dir, Keys: provider})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("legacy-v2")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != 2 || loaded.Origins["https://example.com"].Cookies[0].Value != "s3cret" {
		t.Fatalf("legacy v2 state mismatch: %#v", loaded)
	}
}

func TestKeychainSourcedStoreRoundTrip(t *testing.T) {
	provider := &fakeKeyProvider{key: testKey(), source: KeySourceKeychain}
	store := newTestStore(t, 30*24*time.Hour, provider)
	if err := store.Save(sampleState("keychain")); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("keychain")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.KeySource != string(KeySourceKeychain) {
		t.Fatalf("key source = %q", loaded.KeySource)
	}
	if loaded.Origins["https://example.com"].Cookies[0].Value != "s3cret" {
		t.Fatal("keychain round trip lost cookie value")
	}
}
