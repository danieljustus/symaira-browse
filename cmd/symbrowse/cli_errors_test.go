package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/output"
)

func TestCLIArgumentFailuresUseInvalidArgsEnvelope(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "open requires url", args: []string{"open", "--json"}},
		{name: "click requires selector", args: []string{"click", "--json"}},
		{name: "find requires operands", args: []string{"find", "--json"}},
		{name: "get attr requires operands", args: []string{"get", "attr", "--json"}},
		{name: "eval requires expression", args: []string{"eval", "--json"}},
		{name: "wait requires condition", args: []string{"wait", "--json"}},
		{name: "handoff requires reason", args: []string{"handoff", "--json"}},
		{name: "watch takeover requires reason", args: []string{"watch", "--take-over", "--json"}},
		{name: "diff screenshot requires baseline", args: []string{"diff", "screenshot", "--json"}},
		{name: "viewport requires dimensions", args: []string{"set", "viewport", "--json"}},
		{name: "cookies clear requires name", args: []string{"cookies", "clear", "--json"}},
		{name: "session rejects extra argument", args: []string{"session", "list", "extra", "--json"}},
		{name: "url diff requires two urls", args: []string{"diff", "url", "one", "--json"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(test.args, &stdout, &stderr)
			if code != int(exitcodes.ExitNoInput) {
				t.Fatalf("exit code = %d, want %d; stderr = %q", code, exitcodes.ExitNoInput, stderr.String())
			}
			var envelope output.Envelope
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode output: %v; stdout = %q", err, stdout.String())
			}
			if envelope.Success || envelope.Error == nil || envelope.Error.Code != string(output.CodeInvalidArgs) {
				t.Fatalf("envelope = %+v, want invalid_args failure", envelope)
			}
		})
	}
}

func TestWriteEnvelopeFromResponsePreservesWarnings(t *testing.T) {
	var stdout bytes.Buffer
	command := newRootCommand()
	command.SetOut(&stdout)
	if err := command.PersistentFlags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	response := daemon.SuccessResponse(map[string]any{"value": "ok"}, []daemon.Warning{{
		Kind:     "prompt_injection",
		Severity: "high",
		Message:  "page content requested an unsafe action",
		Ref:      "@e1",
	}})
	if err := writeEnvelopeFromResponse(command, response); err != nil {
		t.Fatal(err)
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Warnings) != 1 || envelope.Warnings[0].Kind != "prompt_injection" || envelope.Warnings[0].Ref != "@e1" {
		t.Fatalf("warnings = %+v, want daemon warning preserved", envelope.Warnings)
	}
}
