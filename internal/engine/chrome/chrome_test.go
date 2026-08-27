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

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/static"
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

func TestLaunchAttachesToExistingEndpoint(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// An attached engine must never send Browser.close: keep the
		// connection open until the test closes it.
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var request map[string]any
		if err := ws.ReadJSON(&request); err != nil {
			return
		}
		t.Errorf("attached engine sent a CDP command (%v); it must only detach", request)
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")

	e := New(Options{CDPEndpoint: endpoint, StartupTimeout: 3 * time.Second, RequestTimeout: time.Second})
	if err := e.Launch(context.Background()); err != nil {
		t.Fatalf("Launch(attach) = %v", err)
	}
	if e.cmd != nil {
		t.Fatalf("attach must not start a Chrome process (cmd = %v)", e.cmd)
	}
	e.mu.Lock()
	attached, dataDir := e.attached, e.dataDir
	e.mu.Unlock()
	if !attached {
		t.Fatal("engine must be marked attached")
	}
	if dataDir != "" {
		t.Fatalf("attach must not create a private profile (dataDir = %q)", dataDir)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestCapabilitiesChrome(t *testing.T) {
	caps := New(Options{}).Capabilities()
	if caps.Kind != "chrome" {
		t.Fatalf("kind = %q, want chrome", caps.Kind)
	}
	if len(caps.Interfaces) != len(engine.OptionalInterfaceNames) {
		t.Fatalf("chrome must implement every optional interface: %d of %d", len(caps.Interfaces), len(engine.OptionalInterfaceNames))
	}
	if len(caps.Unsupported) != 0 {
		t.Fatalf("chrome unsupported = %v, want none", caps.Unsupported)
	}
	if caps.LaunchMode != "launch" {
		t.Fatalf("launch_mode = %q, want launch", caps.LaunchMode)
	}

	attached := New(Options{CDPEndpoint: "ws://127.0.0.1:1"}).Capabilities()
	if attached.LaunchMode != "launch" {
		t.Fatalf("pre-launch launch_mode = %q, want launch (attach applies only after Launch)", attached.LaunchMode)
	}
}

func TestCapabilitiesStatic(t *testing.T) {
	caps := static.New().Capabilities()
	if caps.Kind != "static" {
		t.Fatalf("kind = %q, want static", caps.Kind)
	}
	for _, want := range []string{"InspectionEngine", "NavigationStateProvider"} {
		if !stringListContains(caps.Interfaces, want) {
			t.Fatalf("static interfaces = %v, missing %q", caps.Interfaces, want)
		}
	}
	for _, forbidden := range []string{"NetworkEvents", "CookieEngine", "FileTransfer", "TabManager"} {
		if stringListContains(caps.Interfaces, forbidden) {
			t.Fatalf("static must not claim %q (interfaces = %v)", forbidden, caps.Interfaces)
		}
	}
}

func stringListContains(list []string, needle string) bool {
	for _, item := range list {
		if item == needle {
			return true
		}
	}
	return false
}
