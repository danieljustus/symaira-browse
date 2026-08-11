package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine/doctor"
)

func TestVersionJSON(t *testing.T) {
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"version", "--json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	// The versionkit contract is exact bytes: {tool, version,
	// schema_version} on a single line, no envelope, no extra fields.
	want := `{"tool":"symbrowse","version":"dev","schema_version":1}` + "\n"
	if output.String() != want {
		t.Fatalf("version --json = %q, want %q", output.String(), want)
	}
}

func TestConfigShowUsesCommandWriterAndFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SYMBROWSE_LOG_LEVEL", "debug")
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"config", "show", "--json", "--log-level", "error"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("config show produced no output")
	}
	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Fields map[string]struct {
				Value  string `json:"value"`
				Source string `json:"source"`
			} `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if !envelope.Success {
		t.Fatalf("envelope = %#v: expected success", envelope)
	}
	if got := envelope.Data.Fields["log_level"]; got.Value != "error" || got.Source != "flag" {
		t.Fatalf("log_level = %#v", got)
	}
}

// TestGlobalJSONErrorEnvelope verifies that a failing command produces the
// unified failure envelope with a documented error code when --json is set.
func TestGlobalJSONErrorEnvelope(t *testing.T) {
	var output, errOutput bytes.Buffer
	exitCode := runCLI([]string{"open", "--json"}, &output, &errOutput)
	if exitCode == 0 {
		t.Fatal("expected open without a URL to fail")
	}
	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("output = %q: %v", output.String(), err)
	}
	if envelope.Success {
		t.Fatalf("envelope = %#v: expected failure", envelope)
	}
	if envelope.Error.Code == "" {
		t.Fatalf("envelope = %#v: missing error code", envelope)
	}
}

// TestHumanErrorGoesToStderr verifies that without --json the error text is
// printed to stderr and stdout stays clean.
func TestHumanErrorGoesToStderr(t *testing.T) {
	var output, errOutput bytes.Buffer
	exitCode := runCLI([]string{"open"}, &output, &errOutput)
	if exitCode == 0 {
		t.Fatal("expected open without a URL to fail")
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", output.String())
	}
	if errOutput.Len() == 0 {
		t.Fatal("expected an error message on stderr")
	}
}

func TestDoctorFailureMessage(t *testing.T) {
	cases := []struct {
		name   string
		report doctor.Report
		check  string
		want   string
	}{
		{
			name: "matching failed check returns its message",
			report: doctor.Report{Checks: []doctor.Check{
				{Name: "chrome", Status: doctor.StatusFail, Message: "no chrome executable found"},
			}},
			check: "chrome",
			want:  "no chrome executable found",
		},
		{
			name: "passing check of the same name falls back",
			report: doctor.Report{Checks: []doctor.Check{
				{Name: "chrome", Status: doctor.StatusPass, Message: "chrome ok"},
			}},
			check: "chrome",
			want:  "doctor check failed",
		},
		{
			name: "unknown check name falls back",
			report: doctor.Report{Checks: []doctor.Check{
				{Name: "chrome", Status: doctor.StatusFail, Message: "no chrome executable found"},
			}},
			check: "network",
			want:  "doctor check failed",
		},
		{
			name:   "empty report falls back",
			report: doctor.Report{},
			check:  "chrome",
			want:   "doctor check failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := doctorFailureMessage(tc.report, tc.check); got != tc.want {
				t.Fatalf("doctorFailureMessage = %q, want %q", got, tc.want)
			}
		})
	}
}
