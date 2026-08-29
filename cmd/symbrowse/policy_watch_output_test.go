package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/output"
)

func TestPolicyExplainHonorsOutputFormats(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, path, func([]byte) []byte {
		return []byte(`{"success":true,"data":{"explanation":"allow\ndecider: policy","source":"built-in","decider":"policy","guard_active":false}}`)
	})

	t.Run("text", func(t *testing.T) {
		var stdout bytes.Buffer
		command := newRootCommand()
		command.SetOut(&stdout)
		command.SetArgs([]string{"policy", "explain", "read", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") {
			t.Fatalf("text output is JSON: %q", stdout.String())
		}
		if !strings.Contains(stdout.String(), "allow") {
			t.Fatalf("text output = %q", stdout.String())
		}
	})

	t.Run("json", func(t *testing.T) {
		var stdout bytes.Buffer
		command := newRootCommand()
		command.SetOut(&stdout)
		command.SetArgs([]string{"policy", "explain", "read", "--session", session, "--json"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var envelope output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("json output = %q: %v", stdout.String(), err)
		}
		if !envelope.Success {
			t.Fatalf("envelope = %#v", envelope)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		var stdout bytes.Buffer
		command := newRootCommand()
		command.SetOut(&stdout)
		command.SetArgs([]string{"policy", "explain", "read", "--session", session, "--output", "yaml"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") || !strings.Contains(stdout.String(), "success:") {
			t.Fatalf("yaml output = %q", stdout.String())
		}
	})
}

func TestWatchTakeOverHonorsOutputFormats(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, path, func([]byte) []byte {
		return []byte(`{"success":true,"data":{"handoff_id":"handoff-1","status":"accepted"}}`)
	})

	t.Run("text", func(t *testing.T) {
		var stdout bytes.Buffer
		command := newRootCommand()
		command.SetOut(&stdout)
		command.SetArgs([]string{"watch", "--take-over", "--reason", "operator review", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") || !strings.Contains(stdout.String(), "handoff accepted") {
			t.Fatalf("text output = %q", stdout.String())
		}
	})

	for _, formatArgs := range [][]string{{"--json"}, {"--output", "yaml"}} {
		name := strings.Join(formatArgs, "-")
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			command := newRootCommand()
			command.SetOut(&stdout)
			args := []string{"watch", "--take-over", "--reason", "operator review", "--session", session}
			args = append(args, formatArgs...)
			command.SetArgs(args)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if formatArgs[0] == "--json" {
				var envelope output.Envelope
				if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
					t.Fatalf("json output = %q: %v", stdout.String(), err)
				}
				if !envelope.Success {
					t.Fatalf("envelope = %#v", envelope)
				}
				return
			}
			if strings.HasPrefix(strings.TrimSpace(stdout.String()), "{") || !strings.Contains(stdout.String(), "success:") {
				t.Fatalf("yaml output = %q", stdout.String())
			}
		})
	}
}
