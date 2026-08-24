package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// TestSimpleFrameReachesRunningDaemon reproduces issue #235: tab/frame/dialog
// commands failed with "socket path is required" because sendSimpleFrame
// built the daemon client without resolving the session socket path. With
// the fake daemon listening, each command must reach it and succeed instead
// of failing client-side.
func TestSimpleFrameReachesRunningDaemon(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, path, func([]byte) []byte {
		return []byte(`{"success":true,"data":{"tabs":[]}}`)
	})

	cases := []struct {
		name string
		args []string
	}{
		{"tab list", []string{"tab", "list"}},
		{"tab new", []string{"tab", "new", "https://example.com"}},
		{"tab switch", []string{"tab", "switch", "t1"}},
		{"tab close", []string{"tab", "close", "t1"}},
		{"frame tree", []string{"frame", "tree"}},
		{"frame main", []string{"frame", "main"}},
		{"dialog status", []string{"dialog", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := newRootCommand()
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append(tc.args, "--session", session))
			if err := command.Execute(); err != nil {
				t.Fatalf("%v: %v", tc.args, err)
			}
			if strings.Contains(out.String(), "socket path is required") {
				t.Fatalf("%v still fails client-side: %s", tc.args, out.String())
			}
		})
	}
}

// TestSimpleFrameMissingDaemonFailsCleanly verifies the missing-daemon path
// fails with the transport/autostart error instead of hanging or succeeding.
// The context is canceled so the client's autostart hook gives up quickly.
func TestSimpleFrameMissingDaemonFailsCleanly(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := newRootCommand()
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"tab", "list", "--session", session})
	if err := command.Execute(); err == nil {
		t.Fatal("expected a missing-daemon failure")
	}
}
