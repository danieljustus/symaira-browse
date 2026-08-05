package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSessionRegistryIsolatesProfilesAndReferences(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	registry := NewSessionRegistry(SessionRegistryOptions{
		UserDataRoot: root,
		PID:          4242,
		Now:          func() time.Time { return now },
	})
	alpha, err := registry.Ensure("alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := registry.Ensure("beta")
	if err != nil {
		t.Fatal(err)
	}
	if alpha.UserDataDir == beta.UserDataDir {
		t.Fatal("sessions share a user-data directory")
	}
	if alpha.BrowserContextID == beta.BrowserContextID {
		t.Fatal("sessions share a browser context")
	}
	for _, dir := range []string{alpha.UserDataDir, beta.UserDataDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		// Windows has no POSIX mode bits (chmod only toggles read-only);
		// the restrictive-permission intent is enforced via ACLs there.
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("profile mode = %o", info.Mode().Perm())
		}
	}
	if err := registry.SetRef("alpha", "save", "@e1"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Ref("beta", "save"); err == nil {
		t.Fatal("reference leaked between sessions")
	}
	info, err := registry.Get("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if info.RefCount != 1 || info.PID != 4242 || info.StartedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("alpha info = %#v", info)
	}
	data := registry.ListData()
	if data.SchemaVersion != SessionSchemaVersion || len(data.Sessions) != 2 {
		t.Fatalf("list data = %#v", data)
	}
	if data.Sessions[0].Name != "alpha" || data.Sessions[1].Name != "beta" {
		t.Fatalf("list order = %#v", data.Sessions)
	}
	if err := registry.Touch("alpha"); err != nil {
		t.Fatal(err)
	}
	registry.Clear()
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("sessions survived clear: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "alpha")); err != nil {
		t.Fatal("profile directory removed during registry clear")
	}
}

func TestSessionCommandResponsesHaveStableData(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{UserDataRoot: t.TempDir(), PID: 99})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{Session: "default", Registry: registry, Handler: func(context.Context, Frame) (any, []Warning, error) { return nil, nil, nil }})
	list := server.handleLine([]byte(`{"cmd":"session.list","session":"default"}`))
	if !list.Success {
		t.Fatalf("list response = %#v", list)
	}
	payload, ok := list.Data.(SessionListData)
	if !ok || payload.SchemaVersion != SessionSchemaVersion || len(payload.Sessions) != 1 {
		t.Fatalf("list payload = %#v", list.Data)
	}
	info := server.handleLine([]byte(`{"cmd":"session.info","session":"default"}`))
	if !info.Success {
		t.Fatalf("info response = %#v", info)
	}
	if _, ok := info.Data.(SessionInfo); !ok {
		t.Fatalf("info payload = %#v", info.Data)
	}
}
