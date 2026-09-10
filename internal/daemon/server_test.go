package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/session"
)

func startTestServer(t *testing.T, handler Handler) (*Server, string, context.CancelFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "default.sock")
	ctx, cancel := context.WithCancel(context.Background())
	server := NewServer(Options{SocketPath: path, Handler: handler, IdleTimeout: -1, OperationTimeout: 25 * time.Millisecond, PeerValidator: func(net.Conn) error { return nil }})
	ready := make(chan error, 1)
	go func() { ready <- server.ListenAndServe(ctx) }()
	deadline := time.Now().Add(time.Second)
	for {
		if info, err := os.Stat(path); err == nil {
			// Listen creates the socket before ListenAndServe applies its
			// restrictive mode. Wait for the security boundary too, rather
			// than racing the chmod in the server goroutine.
			if runtime.GOOS == "windows" || info.Mode().Perm() == 0o600 {
				break
			}
		}
		select {
		case err := <-ready:
			t.Fatalf("test daemon stopped before ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("test daemon did not create its socket")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { cancel(); _ = server.Close(); <-ready })
	return server, path, cancel
}

func TestDecodeFrameAndStableResponseSchema(t *testing.T) {
	frame, err := DecodeFrame([]byte(`{"cmd":"daemon.status","session":"default","request_id":"r1"}`))
	if err != nil || frame.Cmd != "daemon.status" || frame.RequestID != "r1" {
		t.Fatalf("frame = %#v, err = %v", frame, err)
	}
	if _, err := DecodeFrame([]byte(`{"session":"default"}`)); err == nil {
		t.Fatal("missing command accepted")
	}
	encoded, err := json.Marshal(SuccessResponse(map[string]any{"running": true}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["success"] != true || got["data"] == nil {
		t.Fatalf("response schema = %#v", got)
	}
}

func TestSocketPathValidationAndMode(t *testing.T) {
	if _, err := SocketPathIn(t.TempDir(), "../escape"); err == nil {
		t.Fatal("path traversal session accepted")
	}
	_, path, _ := startTestServer(t, func(context.Context, Frame) (any, []Warning, error) { return map[string]any{"ok": true}, nil, nil })
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX mode bits (chmod only toggles read-only).
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o", info.Mode().Perm())
	}
}

func TestOperationTimeoutKeepsConnectionUsable(t *testing.T) {
	_, path, _ := startTestServer(t, func(ctx context.Context, frame Frame) (any, []Warning, error) {
		if frame.Cmd == "slow" {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return map[string]any{"pong": true}, nil, nil
	})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(Frame{Cmd: "slow"}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(Frame{Cmd: "daemon.ping"}); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 128), maxFrameBytes)
	var first, second Response
	if !reader.Scan() || json.Unmarshal(reader.Bytes(), &first) != nil {
		t.Fatal("missing timeout response")
	}
	if !reader.Scan() || json.Unmarshal(reader.Bytes(), &second) != nil {
		t.Fatal("missing follow-up response")
	}
	if first.Success || first.Error == nil || first.Error.Code != ErrorOperationTimeout {
		t.Fatalf("first response = %#v", first)
	}
	if !second.Success {
		t.Fatalf("connection unusable after timeout: %#v", second)
	}
}

func TestStatusAndStop(t *testing.T) {
	_, path, _ := startTestServer(t, func(context.Context, Frame) (any, []Warning, error) {
		return nil, nil, errors.New("unexpected handler")
	})
	client := NewClient(ClientOptions{SocketPath: path, Session: "default", StartDaemon: nil})
	status, err := client.RequestWithoutAutostart(context.Background(), Frame{Cmd: "daemon.status"})
	if err != nil || !status.Success {
		t.Fatalf("status = %#v, err = %v", status, err)
	}
	if data, ok := status.Data.(map[string]any); !ok || data["running"] != true {
		t.Fatalf("status data = %#v", status.Data)
	}
	stop, err := client.RequestWithoutAutostart(context.Background(), Frame{Cmd: "daemon.stop"})
	if err != nil || !stop.Success {
		t.Fatalf("stop = %#v, err = %v", stop, err)
	}
}

func TestClientAutostartHook(t *testing.T) {
	_, path, _ := startTestServer(t, func(context.Context, Frame) (any, []Warning, error) { return map[string]any{"pong": true}, nil, nil })
	called := false
	client := NewClient(ClientOptions{SocketPath: path, Session: "default", StartDaemon: func(context.Context) error { called = true; return nil }})
	response, err := client.Request(context.Background(), Frame{Cmd: "daemon.ping"})
	if err != nil || !response.Success {
		t.Fatalf("request = %#v, err = %v", response, err)
	}
	if called {
		t.Fatal("autostart hook called while socket was available")
	}
}

func TestHandlerErrorResponsePreservesHardStopMetadata(t *testing.T) {
	hardStop := &session.HardStopError{
		Code:                     session.CodeSessionUserControl,
		Message:                  "human controls session",
		RequiresUserConfirmation: true,
		ResumeHint:               "confirm takeover",
	}
	response := handlerErrorResponse(hardStop)
	if response.Success || response.Error == nil {
		t.Fatalf("response = %#v", response)
	}
	if response.Error.Code != session.CodeSessionUserControl || response.Error.Retryable == nil || *response.Error.Retryable || response.Error.RequiresUserConfirmation == nil || !*response.Error.RequiresUserConfirmation || response.Error.ResumeHint != "confirm takeover" {
		t.Fatalf("hard-stop response = %#v", response.Error)
	}
}

// TestConcurrentStartupYieldsOneOwner guards issue #371: several MCP clients
// recovering the same session at once must not each bind the socket. Before
// the fix every starter unconditionally unlinked the existing socket, so two
// daemons could serve one session with split browser state.
func TestConcurrentStartupYieldsOneOwner(t *testing.T) {
	if !socketOwnershipSupported {
		t.Skip("socket ownership is not decidable on this platform; startup keeps the historical replace behavior")
	}
	// Unix socket paths are length-limited (104 bytes on macOS), so the long
	// t.TempDir() name cannot be used here.
	dir, err := os.MkdirTemp("", "sb-race-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "race.sock")

	const starters = 6
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	servers := make([]*Server, 0, starters)
	results := make(chan error, starters)
	for i := 0; i < starters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server := NewServer(Options{
				SocketPath:  socketPath,
				Session:     "race",
				IdleTimeout: -1,
				Handler: func(context.Context, Frame) (any, []Warning, error) {
					return map[string]any{"pong": true}, nil, nil
				},
			})
			mu.Lock()
			servers = append(servers, server)
			mu.Unlock()
			results <- server.ListenAndServe(ctx)
		}()
	}
	// Every starter must be shut down before the test returns, whether it won
	// or lost the race.
	t.Cleanup(func() {
		cancel()
		mu.Lock()
		running := append([]*Server(nil), servers...)
		mu.Unlock()
		for _, server := range running {
			_ = server.Close()
		}
		wg.Wait()
	})

	// Give the starters time to race, then verify exactly one socket serves.
	deadline := time.After(10 * time.Second)
	var losers int
	for losers == 0 {
		select {
		case err := <-results:
			if !errors.Is(err, ErrDaemonAlreadyRunning) {
				t.Fatalf("a starter returned %v, want ErrDaemonAlreadyRunning", err)
			}
			losers++
		case <-deadline:
			t.Fatal("no starter reported ErrDaemonAlreadyRunning; the socket was taken over")
		}
	}

	client := NewClient(ClientOptions{SocketPath: socketPath, Session: "race"})
	response, err := client.RequestWithoutAutostart(context.Background(), Frame{Cmd: "daemon.ping", RequestID: "r1"})
	if err != nil || !response.Success {
		t.Fatalf("ping the surviving daemon: response = %#v, err = %v", response, err)
	}

}

// TestStaleSocketIsReplaced verifies the complement of the single-owner rule:
// a socket file with nothing behind it must not block a fresh daemon.
func TestStaleSocketIsReplaced(t *testing.T) {
	if !socketOwnershipSupported {
		// Without a liveness probe there is nothing platform-specific left to
		// assert here: the socket file is always replaced.
		t.Skip("socket ownership is not decidable on this platform")
	}
	dir, err := os.MkdirTemp("", "sb-stale-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "stale.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	// Close the listener but keep the socket file, exactly as an unclean
	// daemon exit leaves it.
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatalf("stale socket file missing: %v", err)
	}
	if err := removeStaleSocket(socketPath); err != nil {
		t.Fatalf("removeStaleSocket on a dead socket = %v, want nil", err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("stale socket was not removed: %v", err)
	}
}
