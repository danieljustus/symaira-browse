// Package e2e runs the built symbrowse binary against the fixture server
// with a real Chrome (issue #67). The smoke test is opt-in: set
// SYMBROWSE_E2E=1 and provide Chrome via SYMBROWSE_EXECUTABLE_PATH or the
// default /usr/bin/google-chrome (CI runs this in the dedicated smoke job).
// The daemon under test runs Chrome headless by default (issue #97); set
// SYMBROWSE_HEADED=1 to debug against a visible Chrome window.
package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/testserver"
)

// sessionName is unique per test run so repeated runs (and parallel CI
// runners) never share a Chrome profile or socket.
var sessionName = fmt.Sprintf("smoke-%d", time.Now().UnixNano()%1000000)

// TestE2ESmokeOpenSnapshotFillSubmitRead covers the acceptance chain
// open → snapshot → fill → submit → read through the built binary against
// the fixture server (issue #67 AC).
func TestE2ESmokeOpenSnapshotFillSubmitRead(t *testing.T) {
	if os.Getenv("SYMBROWSE_E2E") != "1" {
		t.Skip("E2E smoke is opt-in: set SYMBROWSE_E2E=1")
	}
	// The test owns the daemon lifecycle: a CLI client that gives up
	// mid-request must not autostart a competing daemon (which would
	// inherit the CLI's stdout/stderr pipes and hang CombinedOutput
	// forever once the CLI is killed on its 60s budget).
	t.Setenv("SYMBROWSE_NO_AUTOSTART", "1")
	chrome := os.Getenv("SYMBROWSE_EXECUTABLE_PATH")
	if chrome == "" {
		chrome = "/usr/bin/google-chrome"
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skipf("Chrome binary not found (%s): %v", chrome, err)
	}

	bin := buildBinary(t)

	server := testserver.New(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	startDaemon(t, ctx, bin, chrome)

	socket, err := daemon.SocketPath(sessionName)
	if err != nil {
		t.Fatal(err)
	}
	waitForSocket(t, ctx, socket)

	run(t, ctx, bin, "open", server.URL+"/form")
	snapshot := run(t, ctx, bin, "snapshot")
	if !strings.Contains(snapshot, "Form fixture") && !strings.Contains(snapshot, "text-input") {
		t.Fatalf("snapshot lacks the form fixtures:\n%s", snapshot)
	}
	run(t, ctx, bin, "fill", "#text-input", "hello e2e")
	run(t, ctx, bin, "click", "#submit-button")
	read := run(t, ctx, bin, "read")
	if strings.TrimSpace(read) == "" {
		t.Fatal("read returned an empty document")
	}
	t.Logf("smoke chain ok; read output %d bytes", len(read))
}

// buildBinary compiles the CLI into a temp dir.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "symbrowse")
	command := exec.Command("go", "build", "-o", bin, "github.com/danieljustus/symaira-browse/cmd/symbrowse")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}
	return bin
}

// startDaemon launches the daemon for the smoke session; its output is
// captured so failures can be diagnosed from the test log. The daemon
// runs Chrome headless unless SYMBROWSE_HEADED=1 opts into a visible
// browser for interactive debugging (issue #97).
func startDaemon(t *testing.T, ctx context.Context, bin, chrome string) {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "daemon.log")
	logHandle, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, bin, "daemon", "--session", sessionName)
	daemonEnv := []string{
		"SYMBROWSE_EXECUTABLE_PATH=" + chrome,
		"SYMBROWSE_IDLE_TIMEOUT=120",
		"SYMBROWSE_OPERATION_TIMEOUT=90",
		"SYMBROWSE_CHROME_STARTUP_TIMEOUT=30",
		// The fixture server listens on 127.0.0.1; the daemon's SSRF guard
		// denies loopback by default, so the test daemon opts into private
		// targets (the smoke chain is not a policy test).
		"SYMBROWSE_ALLOW_PRIVATE=1",
	}
	if os.Getenv("SYMBROWSE_HEADED") != "1" {
		daemonEnv = append(daemonEnv, "SYMBROWSE_HEADLESS=1")
	}
	command.Env = append(os.Environ(), daemonEnv...)
	command.Stdout = logHandle
	command.Stderr = logHandle
	if err := command.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		// Graceful termination: the daemon closes the Chrome engine on
		// SIGTERM. SIGKILL would orphan Chrome, which then keeps the
		// profile lock and stalls the next test iteration.
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = command.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
		_ = logHandle.Close()
		if t.Failed() {
			raw, _ := os.ReadFile(logFile)
			t.Logf("daemon log:\n%s", raw)
		}
	})
}

// waitForSocket polls until the daemon accepts connections. Stat-ing the
// socket file is not enough: the daemon must be past bind() and into its
// accept loop, otherwise the first CLI request can fail and trigger the
// client autostart path.
func waitForSocket(t *testing.T, ctx context.Context, socket string) {
	t.Helper()
	for {
		if conn, err := net.Dial("unix", socket); err == nil {
			_ = conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("daemon socket %s never became ready: %v", socket, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// run executes one CLI command and returns its combined output. Every
// command gets its own 60s budget so a hanging daemon fails the test fast
// with the CLI output instead of blocking until the test timeout.
func run(t *testing.T, ctx context.Context, bin string, args ...string) string {
	t.Helper()
	commandCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	full := append([]string{"--session", sessionName}, args...)
	command := exec.CommandContext(commandCtx, bin, full...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("symbrowse %s: %v\n%s", fmt.Sprint(args), err, output)
	}
	return string(output)
}
