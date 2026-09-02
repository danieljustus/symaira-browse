package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/output"
)

func TestStructuredOutputForPreviouslyBareCommandPaths(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, path, func(frame []byte) []byte {
		var request struct {
			Cmd string `json:"cmd"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return []byte(`{"success":false,"error":{"code":"malformed_request","message":"invalid test frame"}}`)
		}
		if request.Cmd == "eval" {
			return []byte(`{"success":true,"data":{"exception_text":"ReferenceError: missing"}}`)
		}
		return []byte(`{"success":true,"data":{"ok":true}}`)
	})

	cases := []struct {
		name      string
		args      []string
		exception bool
	}{
		{name: "eval", args: []string{"eval", "1+1", "--session", session, "--json"}, exception: true},
		{name: "set action", args: []string{"set", "viewport", "800", "600", "--session", session, "--json"}},
		{name: "set device list", args: []string{"set", "device", "--list", "--json"}},
		{name: "auth login", args: []string{"auth", "login", "vault-entry", "--session", session, "--json"}},
		{name: "console clear", args: []string{"console", "clear", "--session", session, "--json"}},
		{name: "errors clear", args: []string{"errors", "clear", "--session", session, "--json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			command := newRootCommand()
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs(tc.args)
			if err := command.Execute(); err != nil {
				t.Fatalf("Execute: %v\nstderr=%s", err, stderr.String())
			}
			var envelope output.Envelope
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("stdout = %q: %v", stdout.String(), err)
			}
			if !envelope.Success {
				t.Fatalf("envelope = %#v, want success", envelope)
			}
			if tc.exception {
				data, ok := envelope.Data.(map[string]any)
				if !ok || data["exception_text"] != "ReferenceError: missing" {
					t.Fatalf("eval data = %#v, want exception_text", envelope.Data)
				}
			}
		})
	}
}
