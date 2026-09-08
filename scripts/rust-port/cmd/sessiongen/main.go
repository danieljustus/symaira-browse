// Command sessiongen emits deterministic session lifecycle vectors from Go production code.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-browse/internal/session"
)

const oracleCommit = "652453d1595fc302bd69c328e7da8a21dbee28b9"

type hardStop struct {
	Code                     string `json:"code"`
	Message                  string `json:"message"`
	Retryable                bool   `json:"retryable"`
	RequiresUserConfirmation bool   `json:"requires_user_confirmation"`
	ResumeHint               string `json:"resume_hint"`
}

type fixture struct {
	OracleCommit string               `json:"oracle_commit"`
	GeneratedBy  string               `json:"generated_by"`
	Snapshots    map[string]any       `json:"snapshots"`
	Errors       map[string]hardStop  `json:"errors"`
	Transitions  []session.Transition `json:"transitions"`
}

func main() {
	output := flag.String("output", "testdata/port/session/lifecycle.json", "fixture path")
	check := flag.Bool("check", false, "verify without writing")
	flag.Parse()

	data, err := generate()
	if err != nil {
		fatal("generate: %v", err)
	}
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(current, data) {
			fatal("session lifecycle fixture drift")
		}
		fmt.Println("PASS session lifecycle fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, data, 0644); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("wrote %s\n", *output)
}

func generate() ([]byte, error) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var transitions []session.Transition
	manager := session.NewManager(session.Options{
		Now: func() time.Time { return now },
		Journal: func(transition session.Transition) error {
			transitions = append(transitions, transition)
			return nil
		},
	})

	snapshots := map[string]any{}
	errorsByName := map[string]hardStop{}
	created, err := manager.Create("session-1", "agent-a")
	if err != nil {
		return nil, err
	}
	snapshots["created"] = created
	delegated, err := manager.Handoff("session-1", "agent-a", "2FA required")
	if err != nil {
		return nil, err
	}
	snapshots["delegated"] = delegated
	if err := manager.CheckAgentAccess("session-1", "agent-a"); err != nil {
		errorsByName["delegated_access"] = normalizeHardStop(err)
	}
	claimed, err := manager.Claim("session-1", "human-a")
	if err != nil {
		return nil, err
	}
	snapshots["claimed"] = claimed
	if _, err := manager.Takeover("session-1", "agent-b", false); err != nil {
		errorsByName["unconfirmed_takeover"] = normalizeHardStop(err)
	}
	snapshots["after_denied_takeover"], _ = manager.Snapshot("session-1")
	taken, err := manager.Takeover("session-1", "agent-b", true)
	if err != nil {
		return nil, err
	}
	snapshots["taken_over"] = taken
	completed, err := manager.Complete("session-1", "agent-b", true)
	if err != nil {
		return nil, err
	}
	snapshots["completed"] = completed
	if err := manager.CheckAgentAccess("session-1", "agent-b"); err != nil {
		errorsByName["completed_access"] = normalizeHardStop(err)
	}
	restored := session.NewManager(session.Options{Now: func() time.Time { return now }})
	if err := restored.Restore(*completed); err != nil {
		return nil, err
	}
	snapshots["restored"], _ = restored.Reconnect("session-1")

	timeoutManager := session.NewManager(session.Options{
		Now: func() time.Time { return now },
		Journal: func(transition session.Transition) error {
			transitions = append(transitions, transition)
			return nil
		},
	})
	if _, err := timeoutManager.Create("session-timeout", "agent-a"); err != nil {
		return nil, err
	}
	if _, err := timeoutManager.Handoff("session-timeout", "agent-a", "CAPTCHA"); err != nil {
		return nil, err
	}
	timedOut, timeoutErr := timeoutManager.Timeout("session-timeout")
	snapshots["timed_out"] = timedOut
	errorsByName["timeout"] = normalizeHardStop(timeoutErr)

	result := fixture{
		OracleCommit: oracleCommit,
		GeneratedBy:  "scripts/rust-port/cmd/sessiongen",
		Snapshots:    snapshots,
		Errors:       errorsByName,
		Transitions:  transitions,
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func normalizeHardStop(err error) hardStop {
	var hard *session.HardStopError
	if !errors.As(err, &hard) {
		fatal("expected hard stop, got %T: %v", err, err)
	}
	return hardStop{
		Code:                     hard.ErrorCode(),
		Message:                  hard.Error(),
		Retryable:                hard.RetryableError(),
		RequiresUserConfirmation: hard.RequiresConfirmation(),
		ResumeHint:               hard.ResumeGuidance(),
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
