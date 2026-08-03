package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/state"
)

// ---------------------------------------------------------------------------
// SessionRegistry: lifecycle, refs and listing.
// ---------------------------------------------------------------------------

func TestSessionRegistryEnsureAndLifecycle(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1, UserDataRoot: t.TempDir()})

	// Invalid names are rejected before touching the filesystem.
	if _, err := registry.Ensure("../escape"); err == nil {
		t.Fatal("expected invalid-session error")
	}

	session, err := registry.Ensure("default")
	if err != nil {
		t.Fatal(err)
	}
	if session.Name != "default" || session.PID != 1 {
		t.Fatalf("session = %+v", session)
	}
	if _, err := os.Stat(session.UserDataDir); err != nil {
		t.Fatalf("user data dir missing: %v", err)
	}

	// Ensure is idempotent.
	again, err := registry.Ensure("default")
	if err != nil {
		t.Fatal(err)
	}
	if again != session {
		t.Fatal("Ensure returned a different session object")
	}

	info, err := registry.Get("default")
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "default" || info.ActiveTabs != 0 {
		t.Fatalf("info = %+v", info)
	}
	if _, err := registry.Get("nope"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestSessionRegistryListAndListData(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1, UserDataRoot: t.TempDir()})
	_, _ = registry.Ensure("beta")
	_, _ = registry.Ensure("alpha")

	names := registry.List()
	if len(names) != 2 || names[0].Name != "alpha" || names[1].Name != "beta" {
		t.Fatalf("list = %+v, want sorted alpha,beta", names)
	}
	data := registry.ListData()
	if data.SchemaVersion != SessionSchemaVersion || len(data.Sessions) != 2 {
		t.Fatalf("list data = %+v", data)
	}
}

func TestSessionRegistryTouchAndTabs(t *testing.T) {
	now := time.Now()
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1, UserDataRoot: t.TempDir(), Now: func() time.Time { return now }})
	_, _ = registry.Ensure("default")

	if err := registry.Touch("missing"); err == nil {
		t.Fatal("expected touch error for unknown session")
	}
	if err := registry.Touch("default"); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetActiveTabs("default", 2); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetActiveTabs("default", -1); err == nil {
		t.Fatal("expected negative-count error")
	}
	info, _ := registry.Get("default")
	if info.ActiveTabs != 2 {
		t.Fatalf("active tabs = %d, want 2", info.ActiveTabs)
	}
}

func TestSessionRegistryRefs(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1, UserDataRoot: t.TempDir()})
	_, _ = registry.Ensure("default")

	if err := registry.SetRef("default", "", "ref"); err == nil {
		t.Fatal("expected empty-key error")
	}
	if err := registry.SetRef("default", "k", ""); err == nil {
		t.Fatal("expected empty-ref error")
	}
	if err := registry.SetRef("missing", "k", "v"); err == nil {
		t.Fatal("expected unknown-session error")
	}
	if err := registry.SetRef("default", "k", "v"); err != nil {
		t.Fatal(err)
	}
	ref, err := registry.Ref("default", "k")
	if err != nil || ref != "v" {
		t.Fatalf("ref = %q err = %v", ref, err)
	}
	if _, err := registry.Ref("default", "absent"); err == nil {
		t.Fatal("expected missing-ref error")
	}
	if _, err := registry.Ref("missing", "k"); err == nil {
		t.Fatal("expected unknown-session error")
	}
	table, err := registry.RefTable("default")
	if err != nil || table["k"] != "v" {
		t.Fatalf("ref table = %v err = %v", table, err)
	}
	if _, err := registry.RefTable("missing"); err == nil {
		t.Fatal("expected unknown-session error")
	}

	registry.Clear()
	if _, err := registry.Get("default"); err == nil {
		t.Fatal("expected not-found after Clear")
	}
}

// ---------------------------------------------------------------------------
// Protocol frame helpers.
// ---------------------------------------------------------------------------

func TestDecodeFrameValidation(t *testing.T) {
	frame, err := DecodeFrame([]byte(`{"cmd":"snapshot","session":"s1","args":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Cmd != "snapshot" || frame.Session != "s1" {
		t.Fatalf("frame = %+v", frame)
	}
	if _, err := DecodeFrame([]byte(`{not json`)); err == nil {
		t.Fatal("expected malformed-json error")
	}
	if _, err := DecodeFrame([]byte(`{"session":"s1"}`)); err == nil {
		t.Fatal("expected missing-cmd error")
	}
}

func TestErrorHelpers(t *testing.T) {
	var nilErr *Error
	if nilErr.Error() != "daemon protocol error" || nilErr.ErrorCode() != "" {
		t.Fatal("nil Error handling broken")
	}
	err := NewError(ErrorPeerDenied, "denied")
	if err.Code != ErrorPeerDenied || err.Message != "denied" {
		t.Fatalf("err = %+v", err)
	}
	if err.Error() != "denied" || err.ErrorCode() != ErrorPeerDenied {
		t.Fatalf("err methods = %q %q", err.Error(), err.ErrorCode())
	}
	resp := ErrorResponse(ErrorUnknownCommand, "nope")
	if resp.Success || resp.Error == nil || resp.Error.Code != ErrorUnknownCommand {
		t.Fatalf("error response = %+v", resp)
	}
	ok := SuccessResponse(map[string]any{"a": 1}, []Warning{{Kind: "w", Message: "m"}})
	if !ok.Success || ok.Data.(map[string]any)["a"] != 1 || len(ok.Warnings) != 1 {
		t.Fatalf("success response = %+v", ok)
	}
}

// ---------------------------------------------------------------------------
// State store edge paths (Clean/CleanOlderThan/Expired/metadata).
// ---------------------------------------------------------------------------

func writeState(t *testing.T, store *state.Store, name string, savedAt, expiresAt time.Time) {
	t.Helper()
	if err := store.Save(&state.State{
		SchemaVersion: state.SchemaVersion,
		Name:          name,
		SavedAt:       savedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:     expiresAt.UTC().Format(time.RFC3339Nano),
		Origins:       map[string]state.OriginState{},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStateCleanOlderThanAndExpired(t *testing.T) {
	current := time.Now()
	store, err := state.NewStore(state.StoreOptions{Dir: t.TempDir(), Now: func() time.Time { return current }})
	if err != nil {
		t.Fatal(err)
	}
	// First save lands at T-48h, second at T (the clock is mutable, Save
	// stamps SavedAt/ExpiresAt from the store clock).
	current = current.Add(-48 * time.Hour)
	writeState(t, store, "old", current, current)
	current = time.Now()
	writeState(t, store, "fresh", current, current)

	removed, err := store.CleanOlderThan(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "old" {
		t.Fatalf("removed = %v, want [old]", removed)
	}
	if _, err := store.Load("old"); err == nil {
		t.Fatal("old state should be gone")
	}

	// Negative age is rejected.
	if _, err := store.CleanOlderThan(0); err == nil {
		t.Fatal("expected positive-age error")
	}

	// Expired() reports states whose ExpiresAt is in the past: with the
	// default retention (30d) nothing is expired yet.
	expired, err := store.Expired()
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("expired = %v, want none", expired)
	}
	// A state saved 31 days ago is expired.
	current = current.Add(-31 * 24 * time.Hour)
	writeState(t, store, "stale", current, current)
	current = time.Now()
	expired, err = store.Expired()
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != "stale" {
		t.Fatalf("expired = %v, want [stale]", expired)
	}

	cleaned, err := store.Clean()
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 1 || cleaned[0] != "stale" {
		t.Fatalf("cleaned = %v, want [stale]", cleaned)
	}
}

func TestStateStoreAccessors(t *testing.T) {
	store, err := state.NewStore(state.StoreOptions{Dir: t.TempDir(), ExpireIn: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if store.Dir() == "" || store.ExpireIn() != 7*24*time.Hour {
		t.Fatalf("dir=%q expireIn=%v", store.Dir(), store.ExpireIn())
	}
	if store.KeySource() != state.KeySourceNone {
		t.Fatalf("key source = %q, want none", store.KeySource())
	}
	if err := store.Save(&state.State{SchemaVersion: state.SchemaVersion, Name: "s", Origins: map[string]state.OriginState{}}); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Metadata("s")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != "s" {
		t.Fatalf("meta = %+v", meta)
	}
	if _, err := store.Metadata("absent"); err == nil {
		t.Fatal("expected missing-metadata error")
	}
	if err := store.Remove("s"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("s"); err != nil {
		t.Fatal(err) // removing twice must be fine (os.ErrNotExist tolerated)
	}
	names, err := store.List()
	if err != nil || len(names) != 0 {
		t.Fatalf("list = %v err = %v", names, err)
	}
}

func TestNewStoreRequiresDir(t *testing.T) {
	if _, err := state.NewStore(state.StoreOptions{}); err == nil {
		t.Fatal("expected missing-dir error")
	}
}

// ---------------------------------------------------------------------------
// Vault resolver edge paths.
// ---------------------------------------------------------------------------

func TestVaultResolverErrors(t *testing.T) {
	// symvault not on PATH -> structured unavailable error.
	resolver := &VaultResolver{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	if _, err := resolver.Resolve(context.Background(), "entry"); !errors.Is(err, ErrVaultUnavailable) {
		t.Fatalf("err = %v, want ErrVaultUnavailable", err)
	}

	// vault runs but returns garbage -> resolve error, never a panic.
	resolver = &VaultResolver{
		LookPath: func(string) (string, error) { return "/bin/symvault", nil },
		Run:      func(_ context.Context, _ string, _ ...string) ([]byte, error) { return []byte("not json"), nil },
	}
	if _, err := resolver.Resolve(context.Background(), "entry"); err == nil {
		t.Fatal("expected parse error")
	}

	// vault process fails -> error surfaces.
	resolver = &VaultResolver{
		LookPath: func(string) (string, error) { return "/bin/symvault", nil },
		Run:      func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, errors.New("vault exit 1") },
	}
	if _, err := resolver.Resolve(context.Background(), "entry"); err == nil {
		t.Fatal("expected run error")
	}
}

// ---------------------------------------------------------------------------
// Filesystem path helpers.
// ---------------------------------------------------------------------------

func TestJournalDirAndPathHelpers(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.New(journal.Options{Dir: dir, Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if got := journalDir(j); got != dir {
		t.Fatalf("journalDir(%q) = %q, want %q", j.Path(), got, dir)
	}
	if got := journalDir(&journal.Journal{}); got != "." {
		t.Fatalf("journalDir(empty) = %q, want .", got)
	}
}

func TestRedactSecretsRemovesValues(t *testing.T) {
	redacted := redactSecrets("login failed for hunter2-secret with ada", "hunter2-secret", "ada")
	if strings.Contains(redacted, "hunter2-secret") || strings.Contains(redacted, "ada") {
		t.Fatalf("redacted = %q, secrets still present", redacted)
	}
}

// ---------------------------------------------------------------------------
// JSON round trips used by the daemon wire protocol.
// ---------------------------------------------------------------------------

func TestFrameJSONRoundTrip(t *testing.T) {
	frame := Frame{Cmd: "click", Session: "default", Args: json.RawMessage(`{"selector":"#btn"}`)}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Cmd != "click" || decoded.Session != "default" || string(decoded.Args) != `{"selector":"#btn"}` {
		t.Fatalf("decoded = %+v", decoded)
	}
}
