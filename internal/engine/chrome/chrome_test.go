package chrome

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestChromeHelperProcess(t *testing.T) {
	if os.Getenv("SYMBROWSE_CHROME_HELPER") != "1" {
		return
	}
	select {}
}

func TestChromeArgsUsePrivateEphemeralProfile(t *testing.T) {
	args := chromeArgs("/tmp/private-profile", false, false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--user-data-dir=/tmp/private-profile",
		"--remote-debugging-port=0",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-domain-reliability",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"--profile-directory=Default", "--remote-debugging-port=9222", "--enable-telemetry", "--disable-webrtc", "--headless"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("args contain forbidden %q: %s", forbidden, joined)
		}
	}
}

func TestChromeArgsDisableWebRTCOnlyWithAllowlist(t *testing.T) {
	withPolicy := strings.Join(chromeArgs("/tmp/private-profile", true, false), " ")
	if !strings.Contains(withPolicy, "--disable-webrtc") {
		t.Fatalf("allowlist mode must disable WebRTC: %s", withPolicy)
	}
	if strings.Count(withPolicy, "about:blank") != 1 {
		t.Fatalf("about:blank must appear exactly once: %s", withPolicy)
	}
	withoutPolicy := strings.Join(chromeArgs("/tmp/private-profile", false, false), " ")
	if strings.Contains(withoutPolicy, "--disable-webrtc") {
		t.Fatalf("default mode must keep WebRTC enabled: %s", withoutPolicy)
	}
	if strings.Contains(withoutPolicy, "--headless=new") {
		t.Fatalf("default mode must not be headless: %s", withoutPolicy)
	}
}

func TestChromeArgsHeadlessMode(t *testing.T) {
	args := chromeArgs("/tmp/private-profile", false, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--headless=new") {
		t.Fatalf("headless args missing --headless=new: %s", joined)
	}
	if !strings.Contains(joined, "about:blank") {
		t.Fatalf("headless args missing the startup URL: %s", joined)
	}
}

func TestWaitForEndpointReadsEphemeralActivePort(t *testing.T) {
	// The endpoint must actually listen: waitForEndpoint verifies the port
	// so a stale DevToolsActivePort from a reused profile cannot stall or
	// misroute the engine.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(fmt.Sprintf("%d\n/devtools/browser/test\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := waitForEndpoint(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/test", port); got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

// TestWaitForEndpointIgnoresStaleActivePort verifies the stale-file fix: a
// DevToolsActivePort whose port refuses connections must not be accepted;
// the fresh file (written later) wins.
func TestWaitForEndpointIgnoresStaleActivePort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	dir := t.TempDir()
	stale := filepath.Join(dir, "DevToolsActivePort")
	if err := os.WriteFile(stale, []byte("1\n/devtools/browser/stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(stale, []byte(fmt.Sprintf("%d\n/devtools/browser/fresh\n", port)), 0o600)
	}()
	got, err := waitForEndpoint(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/fresh", port); got != want {
		t.Fatalf("endpoint = %q, want %q (stale port must be ignored)", got, want)
	}
}

func TestRPCConnectionRoundTrip(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()
		var request map[string]any
		if err := ws.ReadJSON(&request); err != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{"id": request["id"], "result": map[string]any{"ok": true}})
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dial(ctx, endpoint, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var result struct {
		OK bool `json:"ok"`
	}
	if err := conn.Execute(ctx, "Target.getTargets", map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatal("round-trip result was not decoded")
	}
}

func TestCloseReapsProcessRemovesProfileAndIsIdempotent(t *testing.T) {
	profile := t.TempDir()
	marker := filepath.Join(profile, "owned")
	if err := os.WriteFile(marker, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestChromeHelperProcess")
	cmd.Env = append(os.Environ(), "SYMBROWSE_CHROME_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	e := New(Options{RequestTimeout: time.Second})
	e.mu.Lock()
	e.cmd, e.dataDir, e.removeDataDir = cmd, profile, true
	e.mu.Unlock()
	if err := e.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("profile still exists: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}
