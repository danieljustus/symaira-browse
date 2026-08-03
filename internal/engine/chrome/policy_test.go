package chrome

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/gorilla/websocket"
)

// fakeCDPServer simulates the subset of Chrome DevTools the engine uses and
// records every command it receives so tests can assert the network policy
// wiring. Tests inject protocol events through emit.
type fakeCDPServer struct {
	t        *testing.T
	ws       *websocket.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	commands []cdpCommand
}

type cdpCommand struct {
	sessionID string
	method    string
	params    json.RawMessage
}

func newFakeCDPServer(t *testing.T) (*fakeCDPServer, string) {
	t.Helper()
	server := &fakeCDPServer{t: t}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		server.ws = ws
		server.serve()
	}))
	t.Cleanup(httpServer.Close)
	endpoint := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/devtools/browser/fake"
	return server, endpoint
}

func (s *fakeCDPServer) serve() {
	defer func() { _ = s.ws.Close() }()
	for {
		var request rpcRequest
		if err := s.ws.ReadJSON(&request); err != nil {
			return
		}
		s.record(request.SessionID, request.Method, request.Params)
		s.respond(request)
	}
}

func (s *fakeCDPServer) record(sessionID, method string, params any) {
	raw, _ := json.Marshal(params)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, cdpCommand{sessionID: sessionID, method: method, params: raw})
}

func (s *fakeCDPServer) respond(request rpcRequest) {
	var result any
	switch request.Method {
	case "Target.createBrowserContext":
		result = map[string]any{"browserContextId": "ctx-1"}
	case "Target.createTarget":
		result = map[string]any{"targetId": "target-1"}
	case "Target.attachToTarget":
		result = map[string]any{"sessionId": "page-session"}
	case "Page.navigate":
		result = map[string]any{"frameId": "frame-1", "loaderId": "loader-1"}
	case "Fetch.continueRequest", "Fetch.failRequest":
		result = map[string]any{}
	case "Page.enable", "Runtime.enable", "DOM.enable", "Accessibility.enable",
		"Network.enable", "Fetch.enable", "Target.setAutoAttach", "Browser.close":
		result = map[string]any{}
	default:
		s.t.Errorf("fake CDP server received unexpected command %s", request.Method)
		result = map[string]any{}
	}
	response := rpcResponse{ID: request.ID, Result: mustMarshal(result)}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.ws.WriteJSON(response); err != nil {
		s.t.Errorf("fake CDP server write: %v", err)
	}
}

func (s *fakeCDPServer) emit(sessionID, method string, params any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.ws.WriteJSON(rpcMessage{Method: method, Params: mustMarshal(params), SessionID: sessionID}); err != nil {
		s.t.Errorf("fake CDP server emit %s: %v", method, err)
	}
}

func (s *fakeCDPServer) commandsFor(method string) []cdpCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []cdpCommand
	for _, command := range s.commands {
		if command.method == method {
			result = append(result, command)
		}
	}
	return result
}

func mustMarshal(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// waitFor polls condition until it holds or the deadline passes.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// newTestEngine launches an engine whose "Chrome" is a sleeping helper process
// and whose DevTools endpoint is the fake CDP server. The profile directory is
// pre-populated with a DevToolsActivePort file pointing at the fake server.
func newTestEngine(t *testing.T, allowedDomains []string, lockProfile bool) (*Engine, *fakeCDPServer) {
	t.Helper()
	return newTestEnginePolicy(t, allowedDomains, false, false, lockProfile)
}

// newTestEnginePolicy is newTestEngine with the SSRF-guard options.
func newTestEnginePolicy(t *testing.T, allowedDomains []string, ssrfEnabled, allowPrivate, lockProfile bool) (*Engine, *fakeCDPServer) {
	t.Helper()
	server, endpoint := newFakeCDPServer(t)
	profile := t.TempDir()
	if lockProfile {
		// Simulate a Chrome process that is already running against this
		// profile: the allowlist cannot be guaranteed in that state.
		if err := os.WriteFile(filepath.Join(profile, "SingletonLock"), []byte("lock"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	activePort := filepath.Join(profile, "DevToolsActivePort")
	if err := os.WriteFile(activePort, []byte("1\n"+endpoint+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := New(Options{
		ExecutablePath: os.Args[0],
		UserDataDir:    profile,
		StartupTimeout: 3 * time.Second,
		RequestTimeout: 3 * time.Second,
		AllowedDomains: allowedDomains,
		SSRFEnabled:    ssrfEnabled,
		AllowPrivate:   allowPrivate,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = engine.Close()
	})
	if err := engine.Launch(ctx); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return engine, server
}

func helperChromeProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestChromeHelperProcess")
	cmd.Env = append(os.Environ(), "SYMBROWSE_CHROME_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	return cmd
}

func TestNetworkPolicyArmsInterceptionAndCountsBlockedRequests(t *testing.T) {
	helperChromeProcess(t)
	eng, server := newTestEngine(t, []string{"example.com"}, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank"); err != nil {
		t.Fatal(err)
	}

	// The page session must be armed with a catch-all Fetch interception and
	// target auto-attach so workers and popups inherit the policy.
	waitFor(t, "Fetch.enable", func() bool { return len(server.commandsFor("Fetch.enable")) >= 1 })
	waitFor(t, "Target.setAutoAttach", func() bool { return len(server.commandsFor("Target.setAutoAttach")) >= 1 })
	enable := server.commandsFor("Fetch.enable")[0]
	if enable.sessionID != "page-session" {
		t.Errorf("Fetch.enable session = %q, want page-session", enable.sessionID)
	}
	var fetchParams fetchEnableParams
	if err := json.Unmarshal(enable.params, &fetchParams); err != nil {
		t.Fatal(err)
	}
	if len(fetchParams.Patterns) != 1 || fetchParams.Patterns[0].URLPattern != "*" || fetchParams.Patterns[0].RequestStage != "Request" {
		t.Errorf("Fetch.enable patterns = %+v, want catch-all Request stage", fetchParams.Patterns)
	}

	// Allowed subresource: must be continued.
	server.emit("page-session", "Fetch.requestPaused", map[string]any{
		"requestId":    "req-allowed",
		"request":      map[string]any{"url": "https://example.com/app.js"},
		"resourceType": "Script",
	})
	waitFor(t, "continue for allowed request", func() bool { return len(server.commandsFor("Fetch.continueRequest")) == 1 })
	continued := server.commandsFor("Fetch.continueRequest")[0]
	var continueParams fetchContinueParams
	if err := json.Unmarshal(continued.params, &continueParams); err != nil {
		t.Fatal(err)
	}
	if continueParams.RequestID != "req-allowed" {
		t.Errorf("continueRequest id = %q, want req-allowed", continueParams.RequestID)
	}
	if len(server.commandsFor("Fetch.failRequest")) != 0 {
		t.Error("allowed request must not be failed")
	}

	// Foreign-domain subresource: must be failed and counted.
	server.emit("page-session", "Fetch.requestPaused", map[string]any{
		"requestId":    "req-foreign",
		"request":      map[string]any{"url": "https://evil.example.org/pixel.png"},
		"resourceType": "Image",
	})
	waitFor(t, "fail for blocked request", func() bool { return len(server.commandsFor("Fetch.failRequest")) == 1 })
	failed := server.commandsFor("Fetch.failRequest")[0]
	var failParams fetchFailParams
	if err := json.Unmarshal(failed.params, &failParams); err != nil {
		t.Fatal(err)
	}
	if failParams.RequestID != "req-foreign" || failParams.ErrorReason != "blockedByClient" {
		t.Errorf("failRequest = %+v, want req-foreign blockedByClient", failParams)
	}

	waitFor(t, "blocked count", func() bool { return len(eng.BlockedRequests()) == 1 })
	blocked := eng.BlockedRequests()
	if len(blocked) != 1 || blocked[0].URL != "https://evil.example.org/pixel.png" || blocked[0].Count != 1 {
		t.Errorf("BlockedRequests() = %+v", blocked)
	}

	// A second hit on the same URL increments the count.
	server.emit("page-session", "Fetch.requestPaused", map[string]any{
		"requestId":    "req-foreign-2",
		"request":      map[string]any{"url": "https://evil.example.org/pixel.png"},
		"resourceType": "Image",
	})
	waitFor(t, "second block", func() bool {
		blocked := eng.BlockedRequests()
		return len(blocked) == 1 && blocked[0].Count == 2
	})
}

func TestNetworkPolicyCoversWorkerTargets(t *testing.T) {
	helperChromeProcess(t)
	eng, server := newTestEngine(t, []string{"example.com"}, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "initial Fetch.enable", func() bool { return len(server.commandsFor("Fetch.enable")) == 1 })

	// A worker attaches: the policy must be enabled on its session too, so
	// requests made inside the worker cannot bypass the allowlist.
	server.emit("", "Target.attachedToTarget", map[string]any{
		"sessionId":  "worker-session",
		"targetInfo": map[string]any{"type": "worker", "targetId": "worker-1"},
	})
	waitFor(t, "Fetch.enable on worker session", func() bool {
		for _, command := range server.commandsFor("Fetch.enable") {
			if command.sessionID == "worker-session" {
				return true
			}
		}
		return false
	})
	waitFor(t, "Network.enable on worker session", func() bool {
		for _, command := range server.commandsFor("Network.enable") {
			if command.sessionID == "worker-session" {
				return true
			}
		}
		return false
	})

	// A foreign-domain fetch inside the worker must fail on the worker
	// session, not on the page session.
	server.emit("worker-session", "Fetch.requestPaused", map[string]any{
		"requestId":    "req-worker-foreign",
		"request":      map[string]any{"url": "https://evil.example.org/data.json"},
		"resourceType": "XHR",
	})
	waitFor(t, "fail on worker session", func() bool {
		for _, command := range server.commandsFor("Fetch.failRequest") {
			if command.sessionID == "worker-session" {
				return true
			}
		}
		return false
	})
	waitFor(t, "worker block counted", func() bool {
		blocked := eng.BlockedRequests()
		return len(blocked) == 1 && blocked[0].URL == "https://evil.example.org/data.json"
	})

	// Auxiliary targets (browser) must not be intercepted.
	server.emit("", "Target.attachedToTarget", map[string]any{
		"sessionId":  "browser-session",
		"targetInfo": map[string]any{"type": "browser", "targetId": "browser-1"},
	})
	time.Sleep(50 * time.Millisecond)
	for _, command := range server.commandsFor("Fetch.enable") {
		if command.sessionID == "browser-session" {
			t.Error("Fetch.enable must not be sent for browser targets")
		}
	}
}

func TestNetworkPolicyGatesNavigation(t *testing.T) {
	helperChromeProcess(t)
	eng, server := newTestEngine(t, []string{"example.com"}, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	before := len(server.commandsFor("Page.navigate"))

	// Navigating to a foreign domain fails deterministically before any CDP
	// command is sent.
	if _, err := eng.Navigate(ctx, page, "https://evil.example.org/"); err == nil {
		t.Fatal("Navigate to foreign domain succeeded, want allowlist error")
	} else if !strings.Contains(err.Error(), "domain allowlist") {
		t.Errorf("Navigate error = %q, want allowlist message", err)
	}
	if len(server.commandsFor("Page.navigate")) != before {
		t.Error("blocked navigation must not reach the CDP layer")
	}

	// Navigating to an allowed domain proceeds normally.
	if _, err := eng.Navigate(ctx, page, "https://example.com/"); err != nil {
		t.Fatalf("Navigate to allowed domain: %v", err)
	}
	if len(server.commandsFor("Page.navigate")) != before+1 {
		t.Error("allowed navigation must reach the CDP layer")
	}

	// The blocked navigation attempt itself is not a network request, so the
	// blocked count stays at zero unless interception reports one.
	if blocked := eng.BlockedRequests(); len(blocked) != 0 {
		t.Errorf("BlockedRequests() = %+v, want empty", blocked)
	}
}

func TestNetworkPolicyInactiveByDefault(t *testing.T) {
	helperChromeProcess(t)
	eng, server := newTestEngine(t, nil, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if got := server.commandsFor("Fetch.enable"); len(got) != 0 {
		t.Errorf("Fetch.enable sent without allowlist: %+v", got)
	}
	if got := server.commandsFor("Target.setAutoAttach"); len(got) != 0 {
		t.Errorf("Target.setAutoAttach sent without allowlist: %+v", got)
	}
	// A foreign navigation is allowed without a policy.
	if _, err := eng.Navigate(ctx, page, "https://anywhere.example/"); err != nil {
		t.Fatalf("Navigate without allowlist: %v", err)
	}
	if blocked := eng.BlockedRequests(); len(blocked) != 0 {
		t.Errorf("BlockedRequests() = %+v, want empty", blocked)
	}
	if limitations := eng.Limitations(); len(limitations) != 0 {
		t.Errorf("Limitations() = %+v, want empty", limitations)
	}
}

func TestNetworkPolicyReusedProfileLimitation(t *testing.T) {
	helperChromeProcess(t)
	eng, _ := newTestEngine(t, []string{"example.com"}, true)
	if limitations := eng.Limitations(); len(limitations) != 1 {
		t.Fatalf("Limitations() = %+v, want one profile-reuse warning", limitations)
	} else if !strings.Contains(limitations[0], "reusing an existing Chrome profile") {
		t.Errorf("Limitations()[0] = %q", limitations[0])
	}
}

func TestNetworkPolicyRejectsInvalidPatternsAtLaunch(t *testing.T) {
	engine := New(Options{ExecutablePath: os.Args[0], AllowedDomains: []string{"http://example.com"}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.Launch(ctx); err == nil {
		t.Fatal("Launch with invalid pattern succeeded, want error")
	} else if !strings.Contains(err.Error(), "network policy") {
		t.Errorf("Launch error = %q, want pattern validation message", err)
	}
}

func TestNetworkPolicyWarningsShape(t *testing.T) {
	// The daemon converts engine.BlockedRequest entries into protocol
	// warnings; this guards the JSON field names of the shared type.
	blocked := []engine.BlockedRequest{{URL: "https://evil.example.org/x", ResourceType: "Image", Count: 2, Reason: "denied by the SSRF guard"}}
	raw, err := json.Marshal(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"resource_type"`) || !strings.Contains(string(raw), `"url"`) {
		t.Errorf("BlockedRequest JSON = %s, want stable field names", raw)
	}
}

// TestSSRFGuardBlocksPrivateNavigation verifies the fail-fast Navigate gate:
// with the SSRF guard active, navigating to a loopback target fails before a
// single CDP navigation command is sent (MCP mode cannot open localhost
// without opt-in).
func TestSSRFGuardBlocksPrivateNavigation(t *testing.T) {
	helperChromeProcess(t)
	eng, server := newTestEnginePolicy(t, nil, true, false, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	// The guard arms interception and auto-attach just like the allowlist.
	if got := server.commandsFor("Fetch.enable"); len(got) != 1 {
		t.Errorf("Fetch.enable count = %d, want 1", len(got))
	}
	if got := server.commandsFor("Target.setAutoAttach"); len(got) != 1 {
		t.Errorf("Target.setAutoAttach count = %d, want 1", len(got))
	}
	if _, err := eng.Navigate(ctx, page, "http://127.0.0.1:8080/"); err == nil {
		t.Fatal("Navigate to loopback succeeded, want SSRF block")
	} else if !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("Navigate error = %q, want SSRF mention", err)
	}
	// No navigation command must have reached the browser.
	if got := server.commandsFor("Page.navigate"); len(got) != 0 {
		t.Errorf("Page.navigate sent despite SSRF gate: %+v", got)
	}
	if blocked := eng.BlockedRequests(); len(blocked) != 0 {
		t.Errorf("BlockedRequests() = %+v, want empty (gate denies before interception)", blocked)
	}
}

// TestSSRFGuardBlocksPrivateSubresource verifies the interception path: a
// paused request to a private address is failed with blockedByClient, counted
// per URL, and reported with the SSRF reason.
func TestSSRFGuardBlocksPrivateSubresource(t *testing.T) {
	helperChromeProcess(t)
	eng, server := newTestEnginePolicy(t, nil, true, false, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank"); err != nil {
		t.Fatal(err)
	}
	server.emit("page-session", "Fetch.requestPaused", map[string]any{
		"requestId":    "req-private",
		"request":      map[string]any{"url": "http://169.254.169.254/latest/meta-data"},
		"resourceType": "XHR",
	})
	waitFor(t, "ssrf block", func() bool {
		blocked := eng.BlockedRequests()
		return len(blocked) == 1 && blocked[0].Count == 1
	})
	// The blocked count is recorded before the CDP fail command is written,
	// so wait for the command itself before reading it.
	waitFor(t, "ssrf failRequest command", func() bool {
		return len(server.commandsFor("Fetch.failRequest")) >= 1
	})
	failed := server.commandsFor("Fetch.failRequest")[0]
	var failParams fetchFailParams
	if err := json.Unmarshal(failed.params, &failParams); err != nil {
		t.Fatal(err)
	}
	if failParams.RequestID != "req-private" || failParams.ErrorReason != "blockedByClient" {
		t.Errorf("failRequest = %+v, want req-private blockedByClient", failParams)
	}
	blocked := eng.BlockedRequests()
	if len(blocked) != 1 || blocked[0].URL != "http://169.254.169.254/latest/meta-data" {
		t.Fatalf("BlockedRequests() = %+v", blocked)
	}
	if !strings.Contains(blocked[0].Reason, "SSRF") {
		t.Errorf("blocked reason = %q, want SSRF mention", blocked[0].Reason)
	}
}

// TestSSRFGuardAllowPrivateOptIn verifies that --allow-private relaxes the
// guard: the same loopback navigation proceeds.
func TestSSRFGuardAllowPrivateOptIn(t *testing.T) {
	helperChromeProcess(t)
	eng, _ := newTestEnginePolicy(t, nil, true, true, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Navigate(ctx, page, "http://127.0.0.1:8080/"); err != nil {
		t.Fatalf("Navigate with allow-private: %v", err)
	}
	if blocked := eng.BlockedRequests(); len(blocked) != 0 {
		t.Errorf("BlockedRequests() = %+v, want empty", blocked)
	}
}

// TestSSRFGuardInactiveWithoutOption verifies the regular daemon default:
// without SSRFEnabled the network layer stays open to private targets (no
// regression for existing local workflows and fixture tests).
func TestSSRFGuardInactiveWithoutOption(t *testing.T) {
	helperChromeProcess(t)
	eng, server := newTestEnginePolicy(t, nil, false, false, false)

	ctx := context.Background()
	if _, err := eng.NewContext(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := eng.NewPage(ctx, engine.Context{ID: "ctx-1"}, "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if got := server.commandsFor("Fetch.enable"); len(got) != 0 {
		t.Errorf("Fetch.enable sent without SSRF option: %+v", got)
	}
	if _, err := eng.Navigate(ctx, page, "http://127.0.0.1:8080/"); err != nil {
		t.Fatalf("Navigate without SSRF guard: %v", err)
	}
}
