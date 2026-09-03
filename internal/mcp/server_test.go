package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// socketBase creates a short-lived base directory for daemon sockets.
// macOS limits unix socket paths to 104 bytes, so t.TempDir() names (which
// embed the test name) are too long; the daemon package's own tests use the
// same short /tmp pattern.
func socketBase(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "mcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return base
}

// startFakeDaemon runs an in-process daemon server on a socket under base.
// Its handler answers the commands the MCP tools exercise and logs through
// slog during a tool call — the zero-stdout-pollution fixture.
func startFakeDaemon(t *testing.T, base, session string) {
	t.Helper()
	path, err := daemon.SocketPathIn(base, session)
	if err != nil {
		t.Fatal(err)
	}
	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{})
	// The truncate-and-store cache backs the MCP default token budget
	// (issue #23); without it budgeted frames would fail closed.
	cacheDir := filepath.Join(base, session, "cache-out")
	server := daemon.NewServer(daemon.Options{
		SocketPath: path,
		Session:    session,
		Registry:   registry,
		CacheDir:   cacheDir,
		CacheTTL:   time.Hour,
		// In-process fixture: peer-credential validation is unavailable on
		// Windows and irrelevant for a server inside the test binary.
		PeerValidator: func(net.Conn) error { return nil },
		Policy: daemon.PolicyStatus{
			SSRFEnabled:  true,
			AllowPrivate: false,
		},
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			switch frame.Cmd {
			case "open":
				// Log during a tool call: must never reach the MCP stdout.
				slog.Debug("fake daemon open", "url", string(frame.Args))
				return map[string]any{"action": "open", "url": "https://example.com/", "http_status": 200}, nil, nil
			case "snapshot":
				return "fake snapshot text", []daemon.Warning{{Kind: "network_policy", Severity: "warning", Message: "domain allowlist blocked 1 request(s)"}}, nil
			default:
				return nil, nil, daemon.NewError(daemon.ErrorUnknownCommand, "not implemented in fake daemon: "+frame.Cmd)
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe(ctx) }()
	// Cleanups run LIFO: the waiter below must be registered first so that
	// cancel() (registered last) runs before we wait for shutdown.
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("fake daemon did not shut down")
		}
	})
	t.Cleanup(cancel)
	// Wait until the socket accepts connections.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.Dial("unix", path)
		if dialErr == nil {
			_ = conn.Close()
			return
		}
		select {
		case serveErr := <-done:
			t.Fatalf("fake daemon exited before ready: %v", serveErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake daemon socket never became ready")
}

// newTestServer builds an MCP server whose daemon socket resolves under base.
func newTestServer(t *testing.T, base string) *Server {
	t.Helper()
	server, err := New(Options{
		Version:    "v0.3.0-test",
		Session:    "test",
		Executable: "symbrowse", // never executed: the fake daemon is pre-started
		Profiles:   "all",
		SocketPath: func(session string) (string, error) {
			return daemon.SocketPathIn(base, session)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// lockedBuffer is a race-free bytes.Buffer: ServeIO writes responses while
// the test consumes complete lines.
type lockedBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	read int
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// nextLine returns the next complete newline-terminated line, waiting up to
// timeout. It never returns a partial line.
func (b *lockedBuffer) nextLine(timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		data := b.buf.Bytes()
		if idx := bytes.IndexByte(data[b.read:], '\n'); idx >= 0 {
			end := b.read + idx
			line := string(data[b.read:end])
			b.read = end + 1
			b.mu.Unlock()
			return line, true
		}
		b.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return "", false
}

// serve runs the MCP server over an in-memory pipe and returns a sender that
// writes one JSON-RPC request and returns the parsed response.
func serve(t *testing.T, server *Server) func(request map[string]any) map[string]any {
	t.Helper()
	reader, writer := io.Pipe()
	var output lockedBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- server.Core().ServeIO(ctx, reader, &output) }()
	t.Cleanup(func() {
		cancel()
		_ = writer.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("ServeIO did not exit")
		}
	})

	return func(request map[string]any) map[string]any {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
		line, ok := output.nextLine(5 * time.Second)
		if !ok {
			t.Fatalf("timed out waiting for response to %v", request["method"])
			return nil
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("stdout line %q is not a JSON-RPC frame (zero-stdout violation): %v", line, err)
		}
		return response
	}
}

// TestZeroStdoutPollution is the automated AC for issue #30: no byte other
// than JSON-RPC frames may reach stdout. The fake daemon logs during a tool
// call; every line the server writes must still parse as a JSON-RPC frame.
func TestZeroStdoutPollution(t *testing.T) {
	base := socketBase(t)
	startFakeDaemon(t, base, "test")
	server := newTestServer(t, base)
	send := serve(t, server)

	initialize := send(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		},
	})
	if initialize["error"] != nil {
		t.Fatalf("initialize error: %v", initialize["error"])
	}

	// tools/list must enumerate the full registered surface: every
	// canonical tool plus its compatibility aliases (issue #2).
	registered := len(tools)
	for _, tool := range tools {
		registered += len(tool.Aliases)
	}
	toolsResponse := send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	result, _ := toolsResponse["result"].(map[string]any)
	toolList, _ := result["tools"].([]any)
	if len(toolList) != registered {
		t.Fatalf("tools/list returned %d tools, want %d", len(toolList), registered)
	}

	// A tool call whose daemon handler logs: the handshake must survive and
	// the response must be a clean JSON-RPC frame.
	call := send(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "open",
			"arguments": map[string]any{"url": "https://example.com/"},
		},
	})
	if call["error"] != nil {
		t.Fatalf("tools/call error: %v", call["error"])
	}
	callResult, _ := call["result"].(map[string]any)
	content, _ := callResult["content"].([]any)
	if len(content) == 0 {
		t.Fatal("tools/call returned no content")
	}

	// Daemon warnings must be surfaced on the tool result as {data,
	// warnings} so agents see what the network policy denied.
	snapshotCall := send(map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "snapshot", "arguments": map[string]any{}},
	})
	snapshotResult, _ := snapshotCall["result"].(map[string]any)
	snapshotContent, _ := snapshotResult["content"].([]any)
	if len(snapshotContent) == 0 {
		t.Fatal("snapshot tools/call returned no content")
	}
	var payload struct {
		Data     any `json:"data"`
		Warnings []struct {
			Kind string `json:"kind"`
		} `json:"warnings"`
	}
	textContent, _ := snapshotContent[0].(map[string]any)
	text, _ := textContent["text"].(string)
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("snapshot result %q: %v", text, err)
	}
	if payload.Data != "fake snapshot text" || len(payload.Warnings) != 1 || payload.Warnings[0].Kind != "network_policy" {
		t.Fatalf("snapshot payload = %+v, want data + network_policy warning", payload)
	}
}

// TestToolsCallRejectsMissingRequiredArguments verifies that input-schema
// validation happens before any daemon traffic (typed input schemas AC).
func TestToolsCallRejectsMissingRequiredArguments(t *testing.T) {
	base := socketBase(t)
	startFakeDaemon(t, base, "test")
	server := newTestServer(t, base)
	send := serve(t, server)

	response := send(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "open", "arguments": map[string]any{}},
	})
	result, _ := response["result"].(map[string]any)
	isError, _ := result["isError"].(bool)
	if !isError {
		t.Fatal("open without url must fail with a tool error result")
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("tool error result carries no content")
	}
	textContent, _ := content[0].(map[string]any)
	message, _ := textContent["text"].(string)
	if !strings.Contains(message, "url") {
		t.Errorf("error message = %q, want mention of the missing argument", message)
	}
}

// TestToolTableProfilesCoversEveryTool guards the data-table invariant from
// issue #31: every tool belongs to exactly one known profile, and the all
// profile is derived as the union by the profile filter.
func TestToolTableProfilesCoversEveryTool(t *testing.T) {
	seen := map[Profile]int{}
	for _, tool := range tools {
		switch tool.Profile {
		case ProfileCore, ProfileNav, ProfileState, ProfileNetwork, ProfileDebug, ProfileFlows:
			seen[tool.Profile]++
		default:
			t.Errorf("tool %s has no valid profile: %q", tool.Name, tool.Profile)
		}
	}
	if len(seen) == 0 {
		t.Fatal("tool table is empty")
	}
}

// TestCoreProfileStaysUnderFifteenTools guards the "default profile
// registers < 15 tools" acceptance criterion of issue #31.
func TestCoreProfileStaysUnderFifteenTools(t *testing.T) {
	var coreCount int
	for _, tool := range tools {
		if tool.Profile == ProfileCore {
			coreCount++
		}
	}
	if coreCount >= 15 {
		t.Errorf("core profile has %d tools, want < 15", coreCount)
	}
}

// TestEveryToolSchemaAcceptsSession verifies the "every tool takes session"
// AC: the schema builder must add the session property to each tool.
func TestEveryToolSchemaAcceptsSession(t *testing.T) {
	for _, tool := range tools {
		properties, ok := tool.Schema["properties"].(map[string]any)
		if !ok {
			t.Errorf("tool %s schema has no properties object", tool.Name)
			continue
		}
		if _, ok := properties["session"]; !ok {
			t.Errorf("tool %s schema lacks the session argument", tool.Name)
		}
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	server, err := New(Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if server.options.Session != "default" {
		t.Fatalf("session = %q, want default", server.options.Session)
	}
	if server.options.Executable == "" {
		t.Fatal("executable default is empty")
	}
	if server.options.SocketPath == nil {
		t.Fatal("socket path default is nil")
	}
}

func TestDaemonArgsRespectAllowPrivate(t *testing.T) {
	server := &Server{options: Options{AllowPrivate: false}}
	if got := server.daemonArgs("session"); !reflect.DeepEqual(got, []string{"daemon", "--session", "session", "--ssrf"}) {
		t.Fatalf("default daemon args = %v", got)
	}
	server.options.AllowPrivate = true
	if got := server.daemonArgs("session"); !reflect.DeepEqual(got, []string{"daemon", "--session", "session", "--ssrf", "--allow-private"}) {
		t.Fatalf("private daemon args = %v", got)
	}
}

func TestInspectionCommandAndArgumentHelpers(t *testing.T) {
	for _, kind := range []string{"text", "html", "value", "attr", "title", "url", "count", "box", "styles"} {
		if got, err := inspectionCommand(kind); err != nil || got != "get."+kind {
			t.Errorf("inspectionCommand(%q) = %q, %v", kind, got, err)
		}
	}
	for _, kind := range []string{"visible", "enabled", "checked"} {
		if got, err := inspectionCommand(kind); err != nil || got != "is."+kind {
			t.Errorf("inspectionCommand(%q) = %q, %v", kind, got, err)
		}
	}
	if _, err := inspectionCommand("unknown"); err == nil {
		t.Fatal("unknown inspection kind accepted")
	}

	input := map[string]any{"text": "value", "enabled": true, "count": float64(3)}
	if got, err := requiredString(input, "text"); err != nil || got != "value" {
		t.Fatalf("requiredString = %q, %v", got, err)
	}
	for _, value := range []any{"", 3, nil} {
		if _, err := requiredString(map[string]any{"value": value}, "value"); err == nil {
			t.Errorf("requiredString accepted %v", value)
		}
	}
	if !boolValue(input, "enabled") || boolValue(input, "missing") {
		t.Fatal("boolValue returned the wrong value")
	}
	if intValue(input, "count") != 3 || intValue(input, "missing") != 0 {
		t.Fatal("intValue returned the wrong value")
	}
}

func TestToolArgumentBuilders(t *testing.T) {
	cases := map[string]map[string]any{
		"open":     {"url": "https://example.com"},
		"snapshot": {"selector": "main", "depth": float64(2), "interactive": true, "compact": true, "urls": true},
		"click":    {"selector": "#submit"},
		"fill":     {"selector": "#name", "value": "Ada"},
		"type":     {"selector": "#name", "value": " Lovelace"},
		"press":    {"selector": "#name", "key": "Enter"},
		"wait":     {"kind": "selector", "value": "#ready", "state": "visible", "load": "networkidle", "ms": float64(10)},
		"read":     {"url": "https://example.com"},
		"get":      {"kind": "text", "selector": "#result", "attribute": "aria-label"},
		"find":     {"kind": "role", "query": "button", "action": "click", "value": "ok", "name": "OK", "exact": true, "index": float64(1)},
	}
	for _, tool := range tools {
		input, ok := cases[tool.Name]
		if !ok || tool.Args == nil {
			continue
		}
		args, err := tool.Args(input)
		if err != nil {
			t.Errorf("%s args: %v", tool.Name, err)
			continue
		}
		if args == nil {
			t.Errorf("%s args are nil", tool.Name)
		}
		if tool.Command != nil {
			command, err := tool.Command(input)
			if err != nil {
				t.Errorf("%s command: %v", tool.Name, err)
			} else if command == "" {
				t.Errorf("%s command is empty", tool.Name)
			}
		}
	}
	if args, err := tools[1].Args(map[string]any{}); err != nil || args == nil {
		t.Fatalf("snapshot defaults = %#v, %v", args, err)
	}
}

func TestProxyToolValidationErrors(t *testing.T) {
	server := &Server{options: Options{Session: "default"}}
	commandErr := errors.New("command rejected")
	tool := ProxyTool{
		Name:    "test",
		Command: func(map[string]any) (string, error) { return "", commandErr },
	}
	if _, err := server.proxyTool(tool).Handler(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, commandErr) {
		t.Fatalf("command error = %v, want %v", err, commandErr)
	}
	argsErr := errors.New("arguments rejected")
	tool = ProxyTool{
		Name: "test",
		Args: func(map[string]any) (any, error) { return nil, argsErr },
	}
	if _, err := server.proxyTool(tool).Handler(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, argsErr) {
		t.Fatalf("args error = %v, want %v", err, argsErr)
	}
	if _, err := server.proxyTool(tool).Handler(context.Background(), json.RawMessage(`{`)); err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("invalid JSON error = %v", err)
	}
}

func TestDaemonToolError(t *testing.T) {
	if err := daemonToolError(daemon.Response{}); err == nil || err.Error() != "daemon request failed" {
		t.Fatalf("nil daemon error = %v", err)
	}
	err := daemonToolError(daemon.Response{Error: &daemon.Error{Code: "denied", Message: "blocked"}})
	if err == nil || err.Error() != "denied: blocked" {
		t.Fatalf("daemon error = %v", err)
	}
	retryable := false
	requiresConfirmation := true
	err = daemonToolError(daemon.Response{Error: &daemon.Error{
		Code:                     "session_user_control",
		Message:                  "human controls session",
		Retryable:                &retryable,
		RequiresUserConfirmation: &requiresConfirmation,
		ResumeHint:               "confirm takeover",
	}})
	metadata, ok := err.(interface {
		ErrorCode() string
		RetryableError() bool
		RequiresConfirmation() bool
		ResumeGuidance() string
	})
	if !ok || metadata.ErrorCode() != "session_user_control" || metadata.RetryableError() || !metadata.RequiresConfirmation() || metadata.ResumeGuidance() != "confirm takeover" {
		t.Fatalf("MCP hard-stop metadata = %#v", err)
	}
}

func TestMain(m *testing.M) {
	// Route slog output away so it can never be mistaken for server output
	// in the buffers under test.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Run()
}

// TestDaemonArgsForwardEngine guards issue #373: the engine selected for the
// MCP server must reach the daemon it auto-starts, otherwise every restart
// silently falls back to the default Chrome engine.
func TestDaemonArgsForwardEngine(t *testing.T) {
	server := &Server{options: Options{Engine: "safari-bidi"}}
	args := server.daemonArgs("hermes")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--engine safari-bidi") {
		t.Fatalf("daemonArgs = %v, want --engine safari-bidi", args)
	}
	if !strings.Contains(joined, "--ssrf") {
		t.Fatalf("daemonArgs = %v, want the MCP-mode SSRF default preserved", args)
	}

	withoutEngine := (&Server{options: Options{}}).daemonArgs("hermes")
	if strings.Contains(strings.Join(withoutEngine, " "), "--engine") {
		t.Fatalf("daemonArgs = %v, want no --engine without a configured engine", withoutEngine)
	}
}
