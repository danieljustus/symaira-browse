package chrome

import (
	"context"
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
	args := chromeArgs("/tmp/private-profile", false)
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
	for _, forbidden := range []string{"--profile-directory=Default", "--remote-debugging-port=9222", "--enable-telemetry", "--disable-webrtc"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("args contain forbidden %q: %s", forbidden, joined)
		}
	}
}

func TestChromeArgsDisableWebRTCOnlyWithAllowlist(t *testing.T) {
	withPolicy := strings.Join(chromeArgs("/tmp/private-profile", true), " ")
	if !strings.Contains(withPolicy, "--disable-webrtc") {
		t.Fatalf("allowlist mode must disable WebRTC: %s", withPolicy)
	}
	if strings.Count(withPolicy, "about:blank") != 1 {
		t.Fatalf("about:blank must appear exactly once: %s", withPolicy)
	}
	withoutPolicy := strings.Join(chromeArgs("/tmp/private-profile", false), " ")
	if strings.Contains(withoutPolicy, "--disable-webrtc") {
		t.Fatalf("default mode must keep WebRTC enabled: %s", withoutPolicy)
	}
}

func TestWaitForEndpointReadsEphemeralActivePort(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte("43123\n/devtools/browser/test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := waitForEndpoint(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://127.0.0.1:43123/devtools/browser/test" {
		t.Fatalf("endpoint = %q", got)
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
