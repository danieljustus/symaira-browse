package flows

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/testserver"
)

// TestRunAgainstFormFixtureEndToEnd drives the real engine path (fixture
// server + real Chrome via the daemon navigation runtime) with a recorded
// form flow. It is skipped when no Chrome executable is available, mirroring
// the E2E smoke convention.
func TestRunAgainstFormFixtureEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	fixture := testserver.NewServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, e2eRuntimeOptions())
	defer func() { _ = runtime.Close() }()

	executor := func(ctx context.Context, frame daemon.Frame) (daemon.Response, error) {
		data, warnings, err := runtime.Handle(ctx, frame)
		if err != nil {
			if protocolErr, ok := err.(*daemon.Error); ok {
				return daemon.Response{Success: false, Error: protocolErr, Warnings: warnings}, nil
			}
			return daemon.Response{Success: false, Error: daemon.NewError("operation_failed", err.Error()), Warnings: warnings}, nil
		}
		return daemon.SuccessResponse(data, warnings), nil
	}

	flow := parseFlow(t, `name: form-e2e
version: 1
domains: ["`+fixtureHost(fixture.URLFor(testserver.Form))+`"]
inputs: [user]
steps:
  - open: { url: "`+fixture.URLFor(testserver.Form)+`" }
  - fill: { role: "textbox", name: "Text", value: "{{user}}", exact: true }
  - assert: { visible: "Submit form" }
  - click: { role: "button", name: "Submit form", exact: true }
outputs:
  - { name: final_url, from: url }
`)
	report, err := Run(ctx, executor, RunOptions{
		Flow:    flow,
		Inputs:  map[string]string{"user": "e2e-user"},
		Session: "e2e",
	})
	if err != nil {
		t.Fatalf("flow run against fixture failed: %v", err)
	}
	if !report.Success {
		t.Fatalf("report.Success = false: %s", report.Error)
	}
	if len(report.Steps) != 4 {
		t.Fatalf("len(Steps) = %d, want 4", len(report.Steps))
	}
	for index, step := range report.Steps {
		if !step.Success {
			t.Errorf("step %d (%s) failed: %s", index, step.Action, step.Error)
		}
	}
	if report.Outputs["final_url"] == "" {
		t.Error("final_url output is empty")
	}
}

// TestRunRejectsForeignDomainEndToEnd verifies the hard domain constraint on
// the real engine path: a flow whose open URL points at a foreign domain is
// refused before any navigation happens.
func TestRunRejectsForeignDomainEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	// No fixture server needed: the domain gate rejects the open step before
	// any navigation happens.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{})
	if _, err := registry.Ensure("e2e-domains"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, e2eRuntimeOptions())
	defer func() { _ = runtime.Close() }()

	executor := func(ctx context.Context, frame daemon.Frame) (daemon.Response, error) {
		data, warnings, err := runtime.Handle(ctx, frame)
		if err != nil {
			if protocolErr, ok := err.(*daemon.Error); ok {
				return daemon.Response{Success: false, Error: protocolErr, Warnings: warnings}, nil
			}
			return daemon.Response{Success: false, Error: daemon.NewError("operation_failed", err.Error()), Warnings: warnings}, nil
		}
		return daemon.SuccessResponse(data, warnings), nil
	}

	flow := parseFlow(t, `name: foreign-e2e
version: 1
domains: ["allowed.example.com"]
steps:
  - open: { url: "http://127.0.0.1:1/blocked" }
`)
	_, err := Run(ctx, executor, RunOptions{Flow: flow, Session: "e2e-domains"})
	if err == nil {
		t.Fatal("flow run allowed a foreign domain on the real engine path")
	}
}

// freshUserDataRoot returns a temp dir whose cleanup retries briefly so a
// closing Chrome process can release its profile files.
func freshUserDataRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		for attempt := 0; attempt < 20; attempt++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	return dir
}

// chromeExecutable resolves the browser binary used by the E2E tests. The
// tests run on developer machines by default (Chrome present, no CI env)
// and in the dedicated CI E2E job, which opts in via SYMBROWSE_E2E=1 (like
// e2e/smoke_test.go). The regular CI test jobs skip them.
func chromeExecutable(t *testing.T) string {
	t.Helper()
	if os.Getenv("CI") != "" && os.Getenv("SYMBROWSE_E2E") != "1" {
		return ""
	}
	if path := os.Getenv("SYMBROWSE_EXECUTABLE_PATH"); path != "" {
		return path
	}
	for _, candidate := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if path, err := exec.LookPath("google-chrome"); err == nil {
		return path
	}
	if path, err := exec.LookPath("chromium"); err == nil {
		return path
	}
	return ""
}

// e2eRuntimeOptions returns the navigation runtime options used by the
// real-Chrome E2E tests: a generous per-command CDP budget (Chrome
// round-trips can stall for seconds on loaded machines right after a
// sibling tab is created, which previously made these tests flaky) and
// headless mode by default so automated runs never pop a visible Chrome
// window (issue #97). Set SYMBROWSE_HEADED=1 to opt into a headed
// browser for interactive debugging; SYMBROWSE_HEADLESS=1 still forces
// headless (e.g. the CI E2E job) even when SYMBROWSE_HEADED is set.
func e2eRuntimeOptions() daemon.NavigationRuntimeOptions {
	// Screenshot captures (issue #16) need an allowed output root; a fresh
	// temp dir keeps parallel E2E tests isolated.
	shots, err := os.MkdirTemp("", "symbrowse-e2e-shots-")
	if err != nil {
		shots = os.TempDir()
	}
	return daemon.NavigationRuntimeOptions{
		RequestTimeout: 30 * time.Second,
		Headless:       os.Getenv("SYMBROWSE_HEADLESS") == "1" || os.Getenv("SYMBROWSE_HEADED") != "1",
		ScreenshotDirs: []string{shots},
	}
}

// fixtureHost extracts the host:port from a fixture URL for the flow's
// domains list.
func fixtureHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "127.0.0.1"
	}
	return parsed.Host
}

// TestRecordThenReplayEndToEnd proves the core flow-record loop: a session
// against the form fixture is recorded, the generated draft contains no @eN
// refs, and replaying the draft against a fresh session succeeds.
func TestRecordThenReplayEndToEnd(t *testing.T) {
	executable := chromeExecutable(t)
	if executable == "" {
		t.Skip("no chrome executable found; set SYMBROWSE_EXECUTABLE_PATH")
	}
	fixture := testserver.NewServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry.Ensure("e2e-record"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, e2eRuntimeOptions())
	defer func() { _ = runtime.Close() }()
	executor := runtimeExecutor(runtime)

	// Record the session via the protocol (like the real client).
	if _, err := executor(ctx, daemon.Frame{Cmd: "flow.record.start", Session: "e2e-record"}); err != nil {
		t.Fatalf("flow.record.start: %v", err)
	}
	flow := parseFlow(t, `name: record-source
version: 1
domains: ["`+fixtureHost(fixture.URLFor(testserver.Form))+`"]
inputs: [user]
steps:
  - open: { url: "`+fixture.URLFor(testserver.Form)+`" }
  - fill: { role: "textbox", name: "Text", value: "{{user}}", exact: true }
  - click: { role: "button", name: "Submit form", exact: true }
`)
	if _, err := Run(ctx, executor, RunOptions{Flow: flow, Inputs: map[string]string{"user": "recorded-user"}, Session: "e2e-record"}); err != nil {
		t.Fatalf("record run failed: %v", err)
	}
	response, err := executor(ctx, daemon.Frame{Cmd: "flow.record.stop", Session: "e2e-record"})
	if err != nil {
		t.Fatalf("flow.record.stop: %v", err)
	}
	if !response.Success {
		t.Fatalf("flow.record.stop failed: %+v", response.Error)
	}
	rawPayload, _ := json.Marshal(response.Data)
	var stopped struct {
		Actions []daemon.RecordedAction `json:"actions"`
	}
	if err := json.Unmarshal(rawPayload, &stopped); err != nil {
		t.Fatalf("decode stop payload: %v", err)
	}
	actions := stopped.Actions
	if len(actions) < 3 {
		t.Fatalf("recorded %d actions, want >= 3", len(actions))
	}

	// Generate the draft and check it is replayable.
	flowActions := make([]RecordedAction, 0, len(actions))
	for _, action := range actions {
		flowActions = append(flowActions, RecordedAction{
			Index:    action.Index,
			Command:  action.Command,
			Selector: action.Selector,
			Value:    action.Value,
			Role:     action.Role,
			Name:     action.Name,
		})
	}
	draft, err := GenerateDraft(flowActions, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	raw, err := draft.RenderYAML()
	if err != nil {
		t.Fatalf("RenderYAML: %v", err)
	}
	if strings.Contains(string(raw), "@e") {
		t.Fatalf("draft contains session refs:\n%s", raw)
	}
	replayFlow, err := Parse(raw, "replay")
	if err != nil {
		t.Fatalf("generated draft does not parse: %v\n%s", err, raw)
	}

	// Replay against a fresh session.
	registry2 := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: freshUserDataRoot(t)})
	if _, err := registry2.Ensure("e2e-replay"); err != nil {
		t.Fatalf("Ensure replay session: %v", err)
	}
	runtime2 := daemon.NewNavigationRuntime(registry2, executable, e2eRuntimeOptions())
	defer func() { _ = runtime2.Close() }()
	executor2 := runtimeExecutor(runtime2)
	inputs := make(map[string]string, len(draft.Inputs))
	for index, name := range draft.Inputs {
		if index == 0 {
			inputs[name] = "replayed-user"
		} else {
			inputs[name] = "placeholder"
		}
	}
	report, err := Run(ctx, executor2, RunOptions{Flow: replayFlow, Inputs: inputs, Session: "e2e-replay"})
	if err != nil {
		t.Fatalf("replay run failed: %v\n--- draft ---\n%s", err, raw)
	}
	if !report.Success {
		t.Fatalf("replay report.Success = false: %s\n--- draft ---\n%s", report.Error, raw)
	}
}

// runtimeExecutor adapts a NavigationRuntime to the flows.Executor interface.
func runtimeExecutor(runtime *daemon.NavigationRuntime) func(ctx context.Context, frame daemon.Frame) (daemon.Response, error) {
	return func(ctx context.Context, frame daemon.Frame) (daemon.Response, error) {
		data, warnings, err := runtime.Handle(ctx, frame)
		if err != nil {
			if protocolErr, ok := err.(*daemon.Error); ok {
				return daemon.Response{Success: false, Error: protocolErr, Warnings: warnings}, nil
			}
			return daemon.Response{Success: false, Error: daemon.NewError("operation_failed", err.Error()), Warnings: warnings}, nil
		}
		return daemon.SuccessResponse(data, warnings), nil
	}
}
