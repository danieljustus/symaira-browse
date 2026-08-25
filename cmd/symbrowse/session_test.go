package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

func TestSessionRequestInvalidSession(t *testing.T) {
	if _, err := requestNoAutostart(context.Background(), "bad session!", "session.list"); err == nil {
		t.Fatal("expected an invalid session name to fail the no-autostart request")
	}
}

func TestSessionRequestNoDaemon(t *testing.T) {
	redirectSocketDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := requestNoAutostart(ctx, uniqueSession(), "session.list"); err == nil {
		t.Fatal("expected a missing daemon socket to fail the request")
	}
}

func TestSessionInspectionNoDaemonDoesNotAutostart(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	for _, sub := range []string{"list", "info"} {
		command := newRootCommand()
		var out, errOut bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&errOut)
		command.SetArgs([]string{"session", sub, "--session", session})
		err := command.Execute()
		if err == nil {
			t.Fatalf("session %s: expected error when daemon is not running", sub)
		}
		if !strings.Contains(err.Error(), "daemon is unavailable") {
			t.Fatalf("session %s: error = %v, want daemon is unavailable", sub, err)
		}
	}
}

func TestSessionInspectionWithDaemon(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}

	fakeDaemon(t, path, func(frame []byte) []byte {
		if strings.Contains(string(frame), `"cmd":"session.list"`) {
			return []byte(`{"success":true,"data":[{"id":"default","active":true,"scope":"worktree"}]}`)
		}
		if strings.Contains(string(frame), `"cmd":"session.info"`) {
			return []byte(`{"success":true,"data":{"id":"default","active":true,"scope":"worktree"}}`)
		}
		return []byte(`{"success":false,"error":{"code":"unknown_command","message":"unknown"}}`)
	})

	t.Run("session list returns session data", func(t *testing.T) {
		var output bytes.Buffer
		command := newRootCommand()
		command.SetOut(&output)
		command.SetArgs([]string{"session", "list", "--json", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"scope":"worktree"`) {
			t.Fatalf("output = %q, want session list data", output.String())
		}
	})

	t.Run("session info returns session data", func(t *testing.T) {
		var output bytes.Buffer
		command := newRootCommand()
		command.SetOut(&output)
		command.SetArgs([]string{"session", "info", "--json", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"scope":"worktree"`) {
			t.Fatalf("output = %q, want session info data", output.String())
		}
	})
}
