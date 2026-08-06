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
