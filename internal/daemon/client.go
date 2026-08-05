package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// StartDaemonFunc starts a daemon process and returns once it has been
// launched. Tests can inject an in-process starter or a deterministic hook.
type StartDaemonFunc func(context.Context) error

// ClientOptions configures a daemon client.
type ClientOptions struct {
	SocketPath     string
	Session        string
	StartupTimeout time.Duration
	ReadTimeout    time.Duration
	StartDaemon    StartDaemonFunc
}

// Client sends requests to a daemon, optionally autostarting it when the
// socket is unavailable.
type Client struct {
	options ClientOptions
}

// NewClient constructs a client. The default starter launches the current
// executable with the daemon subcommand. Setting SYMBROWSE_NO_AUTOSTART=1
// disables autostart entirely (useful for scripts and tests that manage
// the daemon lifecycle themselves). SYMBROWSE_READ_TIMEOUT overrides the
// per-request socket deadline (default 30s) for slow first commands such
// as a cold Chrome launch.
func NewClient(options ClientOptions) *Client {
	if options.StartupTimeout == 0 {
		options.StartupTimeout = 5 * time.Second
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = DefaultReadTimeout
		if raw := os.Getenv("SYMBROWSE_READ_TIMEOUT"); raw != "" {
			if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
				options.ReadTimeout = time.Duration(seconds) * time.Second
			}
		}
	}
	if options.StartDaemon == nil && os.Getenv("SYMBROWSE_NO_AUTOSTART") != "1" {
		options.StartDaemon = func(ctx context.Context) error {
			return StartDaemonProcess(ctx, os.Args[0], options.Session)
		}
	}
	return &Client{options: options}
}

// Request sends one request and waits for one response. A failed initial dial
// triggers the configured autostart hook and bounded startup retries.
func (c *Client) Request(ctx context.Context, frame Frame) (Response, error) {
	if frame.Session == "" {
		frame.Session = c.options.Session
	}
	response, err := c.requestOnce(ctx, frame)
	if err == nil {
		return response, nil
	}
	if c.options.StartDaemon == nil {
		return Response{}, err
	}
	if startErr := c.options.StartDaemon(ctx); startErr != nil {
		return Response{}, fmt.Errorf("start daemon: %w", startErr)
	}
	deadline := time.Now().Add(c.options.StartupTimeout)
	var lastErr error
	for {
		response, lastErr = c.requestOnce(ctx, frame)
		if lastErr == nil {
			return response, nil
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return Response{}, fmt.Errorf("daemon did not become ready: %w", lastErr)
}

// RequestWithoutAutostart sends one request and never starts a process. It is
// used by status and stop so those lifecycle commands do not create a daemon.
func (c *Client) RequestWithoutAutostart(ctx context.Context, frame Frame) (Response, error) {
	return c.requestOnce(ctx, frame)
}

func (c *Client) requestOnce(ctx context.Context, frame Frame) (Response, error) {
	if c.options.SocketPath == "" {
		return Response{}, errors.New("socket path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.options.SocketPath)
	if err != nil {
		return Response{}, fmt.Errorf("dial daemon: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(c.options.ReadTimeout)); err != nil {
		return Response{}, fmt.Errorf("set daemon deadline: %w", err)
	}
	if err := json.NewEncoder(conn).Encode(frame); err != nil {
		return Response{}, fmt.Errorf("write daemon frame: %w", err)
	}
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 4096), maxFrameBytes)
	if !reader.Scan() {
		if scanErr := reader.Err(); scanErr != nil {
			return Response{}, fmt.Errorf("read daemon response: %w", scanErr)
		}
		return Response{}, errors.New("daemon closed connection without a response")
	}
	var response Response
	if err := json.Unmarshal(reader.Bytes(), &response); err != nil {
		return Response{}, fmt.Errorf("decode daemon response: %w", err)
	}
	return response, nil
}

// StartDaemonProcess launches an independent daemon process using executable.
func StartDaemonProcess(ctx context.Context, executable, session string) error {
	args := []string{"daemon"}
	if session != "" {
		args = append(args, "--session", session)
	}
	return StartDaemonProcessArgs(ctx, executable, args...)
}

// StartDaemonProcessArgs launches an independent daemon process with an
// explicit argument list (e.g. "daemon", "--session", "default", "--ssrf").
// Callers that need policy flags beyond the session use this form.
func StartDaemonProcessArgs(ctx context.Context, executable string, args ...string) error {
	if executable == "" {
		return errors.New("daemon executable path is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) == 0 || args[0] != "daemon" {
		return errors.New("daemon process must start with the daemon subcommand")
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
