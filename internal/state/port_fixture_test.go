package state

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

const portFixtureKeyHex = "abababababababababababababababababababababababababababababababab"

type portFixtureKeyProvider struct{}

func (portFixtureKeyProvider) Key() ([]byte, KeySource, error) {
	key, err := hex.DecodeString(portFixtureKeyHex)
	return key, KeySourceEnv, err
}

func (portFixtureKeyProvider) Source() (KeySource, error) { return KeySourceEnv, nil }

type portFixtureManifest struct {
	SchemaVersion   int                         `json:"schema_version"`
	Oracle          portFixtureOracle           `json:"oracle"`
	KeyHex          string                      `json:"key_hex"`
	Cases           []portFixtureCase           `json:"cases"`
	ResolutionCases []portFixtureResolutionCase `json:"resolution_cases"`
	InitCases       []portFixtureInitCase       `json:"init_cases"`
}

type portFixtureOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type portFixtureCase struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Version    int    `json:"version"`
	Encrypted  bool   `json:"encrypted"`
	SHA256     string `json:"sha256"`
	Expected   State  `json:"expected"`
	HeaderJSON string `json:"header_json,omitempty"`
}

type portFixtureResolutionCase struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	KeySHA256 string `json:"key_sha256,omitempty"`
	Error     string `json:"error,omitempty"`
}

type portFixtureInitCase struct {
	Name               string `json:"name"`
	Action             string `json:"action,omitempty"`
	Configured         bool   `json:"configured"`
	Source             string `json:"source,omitempty"`
	InstructionPresent bool   `json:"instruction_present"`
	Error              string `json:"error,omitempty"`
}

type portFixtureExitError int

func (e portFixtureExitError) Error() string { return "provider failed" }
func (e portFixtureExitError) ExitCode() int { return int(e) }

func TestGeneratePortStateFixtures(t *testing.T) {
	dir := os.Getenv("SYMBROWSE_PORT_STATE_FIXTURE_DIR")
	if dir == "" {
		t.Skip("SYMBROWSE_PORT_STATE_FIXTURE_DIR is unset")
	}
	update := os.Getenv("SYMBROWSE_PORT_FIXTURE_UPDATE") == "1"
	files, manifest := buildPortStateFixtures(t)
	manifestRaw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw = append(manifestRaw, '\n')
	files["manifest.json"] = manifestRaw

	for name, expected := range files {
		path := filepath.Join(dir, name)
		if update {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, expected, 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("fixture %s differs from production Go oracle", name)
		}
	}
}

func buildPortStateFixtures(t *testing.T) (map[string][]byte, portFixtureManifest) {
	t.Helper()
	const savedAt = "2026-08-25T14:00:00Z"
	const expiresAt = "2026-09-24T14:00:00Z"
	files := make(map[string][]byte)
	manifest := portFixtureManifest{
		SchemaVersion: 1,
		Oracle: portFixtureOracle{
			Commit:  "652453d1595fc302bd69c328e7da8a21dbee28b9",
			Release: "v0.8.0",
		},
		KeyHex:          portFixtureKeyHex,
		ResolutionCases: buildPortResolutionCases(),
		InitCases:       buildPortInitCases(),
	}

	for version := 1; version <= SchemaVersion; version++ {
		for _, encrypted := range []bool{false, true} {
			state := portFixtureState(version, savedAt, expiresAt, encrypted)
			raw, header := encodePortFixture(t, state, version, encrypted)
			kind := "plaintext"
			if encrypted {
				kind = "encrypted"
			}
			name := fmt.Sprintf("%s-v%d.state", kind, version)
			files[name] = raw
			digest := sha256.Sum256(raw)
			manifest.Cases = append(manifest.Cases, portFixtureCase{
				Name:       kind + "-v" + fmt.Sprint(version),
				Path:       name,
				Version:    version,
				Encrypted:  encrypted,
				SHA256:     hex.EncodeToString(digest[:]),
				Expected:   *state,
				HeaderJSON: string(header),
			})
		}
	}
	return files, manifest
}

func buildPortResolutionCases() []portFixtureResolutionCase {
	hexAB := strings.Repeat("ab", 32)
	hexCD := strings.Repeat("cd", 32)
	scenarios := []struct {
		name     string
		resolver *KeyResolver
	}{
		{"vault", &KeyResolver{LookPath: func(string) (string, error) { return "/fixture/symvault", nil }, RunVault: func(string, ...string) ([]byte, error) { return []byte(`{"value":"` + hexAB + `"}`), nil }}},
		{"vault-missing-env", &KeyResolver{LookPath: func(string) (string, error) { return "/fixture/symvault", nil }, RunVault: func(string, ...string) ([]byte, error) { return nil, portFixtureExitError(vaultEntryNotFoundExitCode) }, Env: func(string) string { return hexCD }}},
		{"vault-uninitialized-env", &KeyResolver{LookPath: func(string) (string, error) { return "/fixture/symvault", nil }, RunVault: func(string, ...string) ([]byte, error) { return nil, portFixtureExitError(vaultNotInitializedExitCode) }, Env: func(string) string { return hexCD }}},
		{"vault-failure", &KeyResolver{LookPath: func(string) (string, error) { return "/fixture/symvault", nil }, RunVault: func(string, ...string) ([]byte, error) { return nil, portFixtureExitError(4) }, Env: func(string) string { return hexCD }}},
		{"keychain-raw", &KeyResolver{KeychainGet: func(string, string) ([]byte, bool, error) { return bytes.Repeat([]byte{0xab}, 32), true, nil }}},
		{"keychain-invalid", &KeyResolver{KeychainGet: func(string, string) ([]byte, bool, error) { return []byte("bad"), true, nil }}},
		{"environment", &KeyResolver{Env: func(string) string { return hexCD }}},
		{"environment-invalid", &KeyResolver{Env: func(string) string { return "bad" }}},
		{"none", &KeyResolver{}},
	}
	results := make([]portFixtureResolutionCase, 0, len(scenarios))
	for _, scenario := range scenarios {
		key, source, err := scenario.resolver.Key()
		result := portFixtureResolutionCase{Name: scenario.name, Source: string(source)}
		if err != nil {
			result.Error = err.Error()
		} else if len(key) > 0 {
			digest := sha256.Sum256(key)
			result.KeySHA256 = hex.EncodeToString(digest[:])
		}
		results = append(results, result)
	}
	return results
}

func buildPortInitCases() []portFixtureInitCase {
	hexAB := strings.Repeat("ab", 32)
	var provisioned []byte
	scenarios := []struct {
		name     string
		resolver *KeyResolver
	}{
		{"existing", &KeyResolver{Env: func(string) string { return hexAB }}},
		{"environment-fallback", &KeyResolver{LookPath: func(string) (string, error) { return "", os.ErrNotExist }, Env: func(string) string { return "" }}},
		{"vault", &KeyResolver{
			LookPath: func(string) (string, error) { return "/fixture/symvault", nil },
			RunVault: func(string, ...string) ([]byte, error) {
				if len(provisioned) == 0 {
					return nil, os.ErrNotExist
				}
				return []byte(`{"value":"` + hex.EncodeToString(provisioned) + `"}`), nil
			},
			SetVaultContext: func(_ context.Context, _, _ string, key []byte) ([]byte, error) {
				provisioned = append([]byte(nil), key...)
				return nil, nil
			},
		}},
		{"vault-failure", &KeyResolver{
			LookPath: func(string) (string, error) { return "/fixture/symvault", nil },
			RunVault: func(string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
			SetVaultContext: func(context.Context, string, string, []byte) ([]byte, error) {
				return nil, portFixtureExitError(4)
			},
		}},
	}
	results := make([]portFixtureInitCase, 0, len(scenarios))
	for _, scenario := range scenarios {
		result := portFixtureInitCase{Name: scenario.name}
		initialized, err := scenario.resolver.Initialize(context.Background())
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Action = initialized.Action
			result.Configured = initialized.Configured
			result.Source = string(initialized.KeySource)
			result.InstructionPresent = initialized.Instruction != ""
		}
		results = append(results, result)
	}
	return results
}

func portFixtureState(version int, savedAt, expiresAt string, encrypted bool) *State {
	keySource := ""
	if version >= 2 {
		keySource = string(KeySourceNone)
		if encrypted {
			keySource = string(KeySourceEnv)
		}
	}
	return &State{
		SchemaVersion: version,
		Name:          fmt.Sprintf("fixture-v%d", version),
		SavedAt:       savedAt,
		ExpiresAt:     expiresAt,
		KeySource:     keySource,
		Origins: map[string]OriginState{
			"https://example.test": {
				Cookies: []engine.Cookie{{
					Name: "session", Value: "fixture-secret", Domain: ".example.test", Path: "/",
					Expires: -1, Size: 21, HTTPOnly: true, Secure: true, Session: true, SameSite: "Lax",
				}},
				LocalStorage:   map[string]string{"theme": "dark"},
				SessionStorage: map[string]string{"step": "2"},
			},
		},
	}
}

func encodePortFixture(t *testing.T, state *State, version int, encrypted bool) ([]byte, []byte) {
	t.Helper()
	if version == SchemaVersion {
		now, err := time.Parse(time.RFC3339, state.SavedAt)
		if err != nil {
			t.Fatal(err)
		}
		options := StoreOptions{
			Dir:      t.TempDir(),
			Now:      func() time.Time { return now },
			ExpireIn: 30 * 24 * time.Hour,
		}
		if encrypted {
			options.Keys = portFixtureKeyProvider{}
		}
		store, err := NewStore(options)
		if err != nil {
			t.Fatal(err)
		}
		oldReader := cryptorand.Reader
		if encrypted {
			cryptorand.Reader = bytes.NewReader(bytes.Repeat([]byte{byte(version)}, 12))
		}
		err = store.Save(state)
		cryptorand.Reader = oldReader
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(store.Dir(), state.Name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		data := raw[len(fileMagic):]
		newline := bytes.IndexByte(data, '\n')
		if newline < 0 {
			t.Fatal("production v3 encoder omitted header delimiter")
		}
		return raw, append([]byte(nil), data[:newline]...)
	}
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var header []byte
	if version >= 2 {
		header, err = json.Marshal(stateHeader{
			SchemaVersion: version,
			SavedAt:       state.SavedAt,
			ExpiresAt:     state.ExpiresAt,
			KeySource:     state.KeySource,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	body := payload
	if encrypted {
		aad := []byte(nil)
		if version >= 3 {
			aad = header
		}
		oldReader := cryptorand.Reader
		cryptorand.Reader = bytes.NewReader(bytes.Repeat([]byte{byte(version)}, 12))
		t.Cleanup(func() { cryptorand.Reader = oldReader })
		body, err = (&gcmCodec{keys: portFixtureKeyProvider{}}).Encrypt(payload, aad)
		cryptorand.Reader = oldReader
		if err != nil {
			t.Fatal(err)
		}
	}
	out := append([]byte{}, fileMagic...)
	if version >= 2 {
		out = append(out, header...)
		out = append(out, '\n')
	}
	out = append(out, body...)
	return out, header
}
