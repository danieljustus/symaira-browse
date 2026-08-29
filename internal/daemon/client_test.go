package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startMockDaemon(t *testing.T, handle func(conn net.Conn, frame Frame)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sb-client-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "mock.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				scanner := bufio.NewScanner(c)
				if scanner.Scan() {
					var frame Frame
					if err := json.Unmarshal(scanner.Bytes(), &frame); err == nil {
						handle(c, frame)
					}
				}
			}(conn)
		}
	}()
	return path
}

func TestClientNormalRequest(t *testing.T) {
	socketPath := startMockDaemon(t, func(conn net.Conn, frame Frame) {
		resp := SuccessResponse(map[string]any{"status": "ok", "echo_cmd": frame.Cmd}, nil)
		data, _ := json.Marshal(resp)
		_, _ = conn.Write(append(data, '\n'))
	})

	client := NewClient(ClientOptions{
		SocketPath:  socketPath,
		Session:     "test-sess",
		StartDaemon: nil,
	})

	resp, err := client.Request(context.Background(), Frame{Cmd: "ping"})
	if err != nil {
		t.Fatalf("expected successful request, got: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected resp.Success == true, got: %#v", resp)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok || data["status"] != "ok" || data["echo_cmd"] != "ping" {
		t.Fatalf("unexpected response data: %#v", resp.Data)
	}
}

func TestClientNoSocketDaemonUnavailable(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	t.Setenv("SYMBROWSE_NO_AUTOSTART", "1")

	client := NewClient(ClientOptions{
		SocketPath:  socketPath,
		Session:     "mysession",
		StartDaemon: nil,
	})

	_, err := client.Request(context.Background(), Frame{Cmd: "status"})
	if err == nil {
		t.Fatal("expected error for nonexistent socket, got nil")
	}

	var terr *TransportError
	if !errors.As(err, &terr) {
		t.Fatalf("expected *TransportError, got: %T (%v)", err, err)
	}

	if terr.Code != ErrorDaemonUnavailable {
		t.Fatalf("code = %q, want %q", terr.Code, ErrorDaemonUnavailable)
	}
	if terr.ErrorCode() != "daemon_unavailable" {
		t.Fatalf("ErrorCode() = %q, want daemon_unavailable", terr.ErrorCode())
	}

	// Primary message must NOT contain the absolute socket path
	if strings.Contains(terr.Message, socketPath) {
		t.Fatalf("message %q must not contain absolute socket path %q", terr.Message, socketPath)
	}
	if !strings.Contains(terr.Message, "mysession") {
		t.Fatalf("message %q should mention the session name", terr.Message)
	}

	// Hint must contain how to start daemon and mention SYMBROWSE_NO_AUTOSTART
	if !strings.Contains(terr.Hint, "symbrowse daemon --session mysession") {
		t.Fatalf("hint %q should mention how to start daemon with session", terr.Hint)
	}
	if !strings.Contains(terr.Hint, "SYMBROWSE_NO_AUTOSTART") {
		t.Fatalf("hint %q should mention SYMBROWSE_NO_AUTOSTART when autostart is disabled", terr.Hint)
	}

	// Details must contain socket path and session
	if terr.Details == nil || terr.Details["socket_path"] != socketPath || terr.Details["session"] != "mysession" {
		t.Fatalf("unexpected details: %#v", terr.Details)
	}
}

func TestClientReadTimeoutOperationTimeout(t *testing.T) {
	socketPath := startMockDaemon(t, func(conn net.Conn, frame Frame) {
		// Intentionally hang without responding
		time.Sleep(200 * time.Millisecond)
	})

	client := NewClient(ClientOptions{
		SocketPath:  socketPath,
		Session:     "slow-sess",
		ReadTimeout: 30 * time.Millisecond,
		StartDaemon: nil,
	})

	_, err := client.Request(context.Background(), Frame{Cmd: "slow_cmd"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	var terr *TransportError
	if !errors.As(err, &terr) {
		t.Fatalf("expected *TransportError, got: %T (%v)", err, err)
	}

	if terr.Code != ErrorOperationTimeout {
		t.Fatalf("code = %q, want %q", terr.Code, ErrorOperationTimeout)
	}
	if terr.ErrorCode() != "operation_timeout" {
		t.Fatalf("ErrorCode() = %q, want operation_timeout", terr.ErrorCode())
	}

	// Primary message must NOT contain the absolute socket path
	if strings.Contains(terr.Message, socketPath) {
		t.Fatalf("message %q must not contain absolute socket path %q", terr.Message, socketPath)
	}

	// Hint should mention SYMBROWSE_READ_TIMEOUT
	if !strings.Contains(terr.Hint, "SYMBROWSE_READ_TIMEOUT") {
		t.Fatalf("hint %q should mention SYMBROWSE_READ_TIMEOUT", terr.Hint)
	}

	// Details must contain socket_path, session, and timeout_seconds
	if terr.Details == nil || terr.Details["socket_path"] != socketPath || terr.Details["session"] != "slow-sess" {
		t.Fatalf("unexpected details: %#v", terr.Details)
	}
}

func TestClientAutostartFailure(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "autostart-fail.sock")

	client := NewClient(ClientOptions{
		SocketPath: socketPath,
		Session:    "auto-fail",
		StartDaemon: func(context.Context) error {
			return errors.New("cannot launch executable")
		},
	})

	_, err := client.Request(context.Background(), Frame{Cmd: "ping"})
	if err == nil {
		t.Fatal("expected autostart failure error")
	}

	var terr *TransportError
	if !errors.As(err, &terr) {
		t.Fatalf("expected *TransportError, got: %T (%v)", err, err)
	}
	if terr.Code != ErrorDaemonUnavailable {
		t.Fatalf("code = %q, want %q", terr.Code, ErrorDaemonUnavailable)
	}
	if !strings.Contains(terr.Hint, "symbrowse daemon --session auto-fail") {
		t.Fatalf("hint %q should mention how to start daemon", terr.Hint)
	}
	if strings.Contains(terr.Message, socketPath) {
		t.Fatalf("message %q must not contain absolute socket path", terr.Message)
	}
}

func TestClientStartupTimeout(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "startup-timeout.sock")

	client := NewClient(ClientOptions{
		SocketPath:     socketPath,
		Session:        "timeout-sess",
		StartupTimeout: 50 * time.Millisecond,
		StartDaemon: func(context.Context) error {
			// pretend to start daemon successfully, but do not create the socket
			return nil
		},
	})

	_, err := client.Request(context.Background(), Frame{Cmd: "ping"})
	if err == nil {
		t.Fatal("expected startup timeout error")
	}

	var terr *TransportError
	if !errors.As(err, &terr) {
		t.Fatalf("expected *TransportError, got: %T (%v)", err, err)
	}
	if terr.Code != ErrorDaemonUnavailable {
		t.Fatalf("code = %q, want %q", terr.Code, ErrorDaemonUnavailable)
	}
	if !strings.Contains(terr.Message, "did not become ready") {
		t.Fatalf("message = %q, want 'did not become ready'", terr.Message)
	}
	if strings.Contains(terr.Message, socketPath) {
		t.Fatalf("message %q must not contain absolute socket path", terr.Message)
	}
}

func TestClientAutostartSuccessAfterRetries(t *testing.T) {
	dir, err := os.MkdirTemp("", "sb-autostart-ok-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "autostart-ok.sock")

	var started atomic.Bool
	client := NewClient(ClientOptions{
		SocketPath:     socketPath,
		Session:        "ok-sess",
		StartupTimeout: 500 * time.Millisecond,
		StartDaemon: func(context.Context) error {
			started.Store(true)
			// Start listening after a slight delay
			go func() {
				time.Sleep(30 * time.Millisecond)
				listener, err := net.Listen("unix", socketPath)
				if err != nil {
					return
				}
				t.Cleanup(func() { _ = listener.Close() })
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					resp := SuccessResponse(map[string]any{"started": true}, nil)
					data, _ := json.Marshal(resp)
					_, _ = conn.Write(append(data, '\n'))
					_ = conn.Close()
				}
			}()
			return nil
		},
	})

	resp, err := client.Request(context.Background(), Frame{Cmd: "ping"})
	if err != nil {
		t.Fatalf("expected successful request after autostart retry, got: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success response, got: %#v", resp)
	}
	if !started.Load() {
		t.Fatal("expected autostart hook to have been called")
	}
}

func TestClientClosedConnectionWithoutResponse(t *testing.T) {
	t.Setenv("SYMBROWSE_NO_AUTOSTART", "1")
	socketPath := startMockDaemon(t, func(conn net.Conn, frame Frame) {
		// Close immediately without writing anything
		_ = conn.Close()
	})

	client := NewClient(ClientOptions{
		SocketPath:  socketPath,
		Session:     "close-sess",
		StartDaemon: nil,
	})

	_, err := client.Request(context.Background(), Frame{Cmd: "ping"})
	if err == nil {
		t.Fatal("expected error on closed connection, got nil")
	}

	var terr *TransportError
	if !errors.As(err, &terr) {
		t.Fatalf("expected *TransportError, got: %T (%v)", err, err)
	}
	if terr.Code != ErrorDaemonUnavailable {
		t.Fatalf("code = %q, want %q", terr.Code, ErrorDaemonUnavailable)
	}
}

func TestStartDaemonProcessDetachesStreams(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "daemon-helper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf daemon-started >&2\nsleep 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "daemon.log")
	t.Setenv("SYMBROWSE_DAEMON_LOG", logPath)

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = readPipe.Close()
		_ = writePipe.Close()
	})
	originalStderr := os.Stderr
	os.Stderr = writePipe
	t.Cleanup(func() { os.Stderr = originalStderr })

	if err := StartDaemonProcessArgs(context.Background(), script, "daemon"); err != nil {
		t.Fatalf("start daemon process: %v", err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, readPipe)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("autostarted daemon kept the caller's stderr pipe open")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		data, readErr := os.ReadFile(logPath)
		if readErr == nil && strings.Contains(string(data), "daemon-started") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon log did not contain startup output: read error=%v, contents=%q", readErr, data)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
