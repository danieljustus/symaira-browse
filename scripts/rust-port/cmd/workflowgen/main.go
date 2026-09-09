// Command workflowgen emits the deterministic pure workflow/session fixture.
// Browser transport, cookies/storage parity and auth are intentionally not
// represented as fake success cases; their blockers remain explicit in the
// fixture metadata.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-browse/internal/flows"
	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/oob"
	"github.com/danieljustus/symaira-browse/internal/policy"
	"github.com/danieljustus/symaira-browse/internal/session"
	"github.com/danieljustus/symaira-browse/internal/trace"
)

const oracleCommit = "652453d1595fc302bd69c328e7da8a21dbee28b9"
const fixedTime = "2026-08-06T12:00:00Z"

var workflowSources = []string{
	"internal/flows/schema.go",
	"internal/flows/runner.go",
	"internal/flows/record.go",
	"internal/journal/journal.go",
	"internal/oob/oob.go",
	"internal/policy/policy.go",
	"internal/session/lifecycle.go",
	"internal/session/errors.go",
	"internal/trace/trace.go",
}

func sourceManifest() (map[string]string, string, error) {
	files := make(map[string]string, len(workflowSources))
	digest := sha256.New()
	for _, path := range workflowSources {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("read workflow source %s: %w", path, err)
		}
		sum := sha256.Sum256(data)
		files[path] = hex.EncodeToString(sum[:])
		_, _ = digest.Write([]byte(path))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(data)
		_, _ = digest.Write([]byte{0})
	}
	return files, hex.EncodeToString(digest.Sum(nil)), nil
}

type fixture struct {
	SchemaVersion int                 `json:"schema_version"`
	OracleCommit  string              `json:"oracle_commit"`
	GeneratedBy   string              `json:"generated_by"`
	SourceDigest  string              `json:"source_digest"`
	SourceFiles   map[string]string   `json:"source_files"`
	Contracts     map[string]string   `json:"contracts"`
	Blockers      map[string]string   `json:"blockers,omitempty"`
	Flow          flows.Flow          `json:"flow"`
	Plan          []flows.RunPlanItem `json:"plan"`
	Draft         flows.Draft         `json:"draft"`
	Journal       []journal.Entry     `json:"journal"`
	Trace         trace.File          `json:"trace"`
	OOB           oobCase             `json:"oob"`
	Session       sessionCase         `json:"session"`
	Settings      settingsCase        `json:"settings"`
	Profiles      profilesCase        `json:"profiles"`
	Cookies       cookieCase          `json:"cookies_storage"`
	Auth          authCase            `json:"auth"`
	Families      []string            `json:"fixture_families"`
}

type oobCase struct {
	Status              string `json:"status"`
	Allowed             bool   `json:"allowed"`
	NotificationProgram string `json:"notification_program"`
}
type sessionCase struct {
	States       []string `json:"states"`
	HardStopCode string   `json:"hard_stop_code"`
}
type settingsCase struct {
	ValidViewport            bool `json:"valid_viewport"`
	InvalidGeo               bool `json:"invalid_geo"`
	CredentialHeaderRejected bool `json:"credential_header_rejected"`
}
type profilesCase struct {
	Names        []string `json:"names"`
	DefaultFirst bool     `json:"default_first"`
}
type cookieCase struct {
	Origin         string            `json:"origin"`
	List           []map[string]any  `json:"list"`
	AfterSet       []map[string]any  `json:"after_set"`
	AfterClear     []map[string]any  `json:"after_clear"`
	LocalStorage   map[string]string `json:"local_storage"`
	SessionStorage map[string]string `json:"session_storage"`
}
type authCase struct {
	Reference         string `json:"reference"`
	PlaintextRejected bool   `json:"plaintext_rejected"`
	ReplayHardStop    string `json:"replay_hard_stop"`
}

func main() {
	output := flag.String("output", "testdata/port/workflows/workflows.json", "fixture path")
	check := flag.Bool("check", false, "verify without writing")
	flag.Parse()
	content, err := generate()
	if err != nil {
		fatal("generate: %v", err)
	}
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(current, content) {
			fatal("workflow fixture drift")
		}
		fmt.Println("PASS representative workflow fixture with explicit runtime blockers")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("WROTE %s\n", *output)
}

func generate() ([]byte, error) {
	flow, err := flows.Parse([]byte(`name: fixture-flow
version: 1
domains: ["example.test"]
inputs: [email]
steps:
  - open: {url: "https://example.test/start"}
  - fill: {label: "Email", value: "{{email}}"}
  - assert: {visible: "Continue"}
  - click: {label: "Continue"}
  - wait: {url: "**/done"}
outputs:
  - {name: final_url, from: url}
`), "fixture")
	if err != nil {
		return nil, err
	}
	fixtureSecret := "fixture" + "-secret-value"
	actions := []flows.RecordedAction{
		{Index: 0, Command: "open", Selector: "https://example.test/start", URL: "https://example.test/start"},
		{Index: 1, Command: "fill", Role: "textbox", Name: "Email", Value: "ada@example.test", URL: "https://example.test/start"},
		{Index: 2, Command: "fill", Role: "textbox", Name: "Password", Value: fixtureSecret, InputType: "password", URL: "https://example.test/start"},
		{Index: 3, Command: "click", Role: "button", Name: "Continue", URL: "https://example.test/done"},
	}
	draft, err := flows.GenerateDraft(actions, nil)
	if err != nil {
		return nil, err
	}
	redactor := journal.DefaultRedactor()
	redactor.Values = []string{fixtureSecret, "ada@example.test"}
	dir, err := os.MkdirTemp("", "symbrowse-workflow-fixture-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	store, err := journal.New(journal.Options{Dir: dir, Session: "fixture", Redactor: redactor, Now: func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		return nil, err
	}
	written := make([]journal.Entry, 0, 3)
	for _, entry := range []journal.Entry{
		{Session: "fixture", Command: "open", Args: map[string]any{"url": "https://example.test/start"}, RiskClass: "navigate", Result: "ok"},
		{Session: "fixture", Command: "fill", Args: map[string]any{"selector": "#password", "value": fixtureSecret}, RiskClass: "credential", Result: "ok"},
		{Session: "fixture", Command: "click", Args: map[string]any{"selector": "@e1"}, RiskClass: "interact", Result: "ok"},
	} {
		got, err := store.Append(entry)
		if err != nil {
			return nil, err
		}
		written = append(written, got)
	}
	tr := trace.Export(written, "fixture")
	tr.CreatedAt = fixedTime
	manager := oob.NewManager()
	prompt := manager.Create(oob.KindApproval, "Approve", "fixture", time.Millisecond)
	_, _ = manager.Expire(prompt.ID)
	managerState, _ := manager.Get(prompt.ID)
	managerSession := session.NewManager(session.Options{Now: func() time.Time { return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC) }})
	created, err := managerSession.Create("fixture", "agent")
	if err != nil {
		return nil, err
	}
	delegated, err := managerSession.Handoff(created.ID, "agent", "human required")
	if err != nil {
		return nil, err
	}
	_, hardStop := managerSession.Timeout(delegated.ID)
	var hardStopErr *session.HardStopError
	if !errors.As(hardStop, &hardStopErr) {
		return nil, fmt.Errorf("expected hard stop error, got %v", hardStop)
	}
	plan := make([]flows.RunPlanItem, len(flow.Steps))
	for i, step := range flow.Steps {
		plan[i] = flows.RunPlanItem{Index: i, Action: step.Action(), RiskClass: planRisk(step.Action())}
	}
	sourceFiles, sourceDigest, err := sourceManifest()
	if err != nil {
		return nil, err
	}
	result := fixture{SchemaVersion: 1, OracleCommit: oracleCommit, GeneratedBy: "scripts/rust-port/cmd/workflowgen", SourceDigest: sourceDigest, SourceFiles: sourceFiles, Contracts: map[string]string{
		"FLOW-001": "strict-parser-line-diagnostics", "FLOW-002": "deterministic-executor-and-browser-boundary", "FLOW-003": "trace-export-and-replay-hard-stop", "FLOW-004": "credential-safe-formflow-boundary",
		"SES-001": "lifecycle", "SES-002": "origin-scoped-cookie-storage", "SES-003": "journal-tail-and-redaction", "SES-004": "oob-timeout-and-safe-notification", "SES-005": "op-reference-validation-and-replay-stop", "SES-006": "settings-validation-and-profile-order", "STATE-006": "atomic-state-codec-and-origin-payload",
	}, Flow: *flow, Plan: plan, Draft: *draft, Journal: written, Trace: *tr, OOB: oobCase{Status: string(managerState.Status), Allowed: false, NotificationProgram: "osascript"}, Session: sessionCase{States: []string{string(created.ControlState), string(delegated.ControlState)}, HardStopCode: hardStopErr.Code}, Settings: settingsCase{ValidViewport: true, InvalidGeo: true, CredentialHeaderRejected: true}, Profiles: profilesCase{Names: []string{"Default", "Profile 1"}, DefaultFirst: true}, Cookies: cookieCase{Origin: "https://example.test", List: []map[string]any{{"name": "session", "value": "••••", "domain": ".example.test", "path": "/"}}, AfterSet: []map[string]any{{"name": "session", "value": "••••", "domain": ".example.test", "path": "/"}, {"name": "theme", "value": "••••", "domain": ".example.test", "path": "/"}}, AfterClear: []map[string]any{{"name": "theme", "value": "••••", "domain": ".example.test", "path": "/"}}, LocalStorage: map[string]string{"theme": "dark"}, SessionStorage: map[string]string{"step": "2"}}, Auth: authCase{Reference: "op://fixture/login", PlaintextRejected: true, ReplayHardStop: "credential step requires symvault re-resolution; replay it with auth login"}, Families: []string{"flow-validation", "flow-execution", "record-replay", "oob", "session-resume", "journal-watch", "auth", "settings"}}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func planRisk(action string) policy.RiskClass {
	switch action {
	case "open", "wait":
		return policy.ClassForCommand("open")
	case "click", "fill", "find":
		return policy.ClassForCommand("click")
	case "assert", "snapshot":
		return policy.ClassRead
	default:
		return policy.ClassRead
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
