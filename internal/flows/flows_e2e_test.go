package flows

import (
	"context"
	"net/url"
	"os"
	"os/exec"
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

	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{UserDataRoot: t.TempDir()})
	if _, err := registry.Ensure("e2e"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	runtime := daemon.NewNavigationRuntime(registry, executable, daemon.NavigationRuntimeOptions{})
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
	runtime := daemon.NewNavigationRuntime(registry, executable, daemon.NavigationRuntimeOptions{})
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

// chromeExecutable resolves the browser binary used by the E2E tests.
func chromeExecutable(t *testing.T) string {
	t.Helper()
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

// fixtureHost extracts the host:port from a fixture URL for the flow's
// domains list.
func fixtureHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "127.0.0.1"
	}
	return parsed.Host
}
