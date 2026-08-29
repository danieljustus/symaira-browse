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
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// TransportError represents a client-side daemon transport, dial, or lifecycle failure.
type TransportError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Details map[string]any `json:"details,omitempty"`
	Err     error          `json:"-"`
}

// Error implements the error interface.
func (e *TransportError) Error() string {
	if e == nil {
		return "daemon transport error"
	}
	return e.Message
}

// ErrorCode exposes the stable error code for the unified output schema.
func (e *TransportError) ErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// ErrorHint exposes the remediation hint.
func (e *TransportError) ErrorHint() string {
	if e == nil {
		return ""
	}
	return e.Hint
}

// ErrorDetails exposes diagnostic context.
func (e *TransportError) ErrorDetails() map[string]any {
	if e == nil {
		return nil
	}
	return e.Details
}

// Unwrap returns the underlying error if any.
func (e *TransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ErrorHint exposes the protocol error hint for the output schema.
func (e *Error) ErrorHint() string {
	if e == nil {
		return ""
	}
	return e.Hint
}

// ErrorDetails exposes the protocol error details for the output schema.
func (e *Error) ErrorDetails() map[string]any {
	if e == nil {
		return nil
	}
	return e.Details
}

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
	DaemonLogPath  string
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
	if options.DaemonLogPath == "" {
		options.DaemonLogPath = DefaultDaemonLogPath()
	}
	if options.StartDaemon == nil && os.Getenv("SYMBROWSE_NO_AUTOSTART") != "1" {
		options.StartDaemon = func(ctx context.Context) error {
			return StartDaemonProcessArgsWithLog(ctx, os.Args[0], options.DaemonLogPath, "daemon", "--session", options.Session)
		}
	}
	return &Client{options: options}
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// Request sends one request and waits for one response. A failed initial dial
// triggers the configured autostart hook and bounded startup retries.
func (c *Client) Request(ctx context.Context, frame Frame) (Response, error) {
	if frame.Session == "" {
		frame.Session = c.options.Session
	}
	session := frame.Session
	if session == "" {
		session = "default"
	}
	response, err := c.requestOnce(ctx, frame)
	if err == nil {
		return response, nil
	}
	var terr *TransportError
	if errors.As(err, &terr) && terr.Code == ErrorOperationTimeout {
		return Response{}, err
	}
	if ctx != nil && ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if c.options.StartDaemon == nil {
		return Response{}, err
	}
	if startErr := c.options.StartDaemon(ctx); startErr != nil {
		return Response{}, &TransportError{
			Code:    ErrorDaemonUnavailable,
			Message: fmt.Sprintf("failed to start daemon for session %q: %v", session, startErr),
			Hint:    c.daemonHint(session, false),
			Details: map[string]any{
				"session":     session,
				"socket_path": c.options.SocketPath,
			},
			Err: startErr,
		}
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
	return Response{}, &TransportError{
		Code:    ErrorDaemonUnavailable,
		Message: fmt.Sprintf("daemon did not become ready for session %q", session),
		Hint:    c.daemonHint(session, false),
		Details: map[string]any{
			"session":     session,
			"socket_path": c.options.SocketPath,
		},
		Err: lastErr,
	}
}

// RequestWithoutAutostart sends one request and never starts a process. It is
// used by status and stop so those lifecycle commands do not create a daemon.
func (c *Client) RequestWithoutAutostart(ctx context.Context, frame Frame) (Response, error) {
	return c.requestOnce(ctx, frame)
}

func (c *Client) daemonHint(session string, autostartDisabled bool) string {
	hint := fmt.Sprintf("start daemon with 'symbrowse daemon --session %s'", session)
	if autostartDisabled {
		hint += " (autostart disabled via SYMBROWSE_NO_AUTOSTART)"
	}
	return fmt.Sprintf("%s; see daemon log at %s", hint, c.options.DaemonLogPath)
}

func (c *Client) requestOnce(ctx context.Context, frame Frame) (Response, error) {
	session := c.options.Session
	if session == "" {
		session = frame.Session
	}
	if session == "" {
		session = "default"
	}
	if c.options.SocketPath == "" {
		return Response{}, &TransportError{
			Code:    ErrorDaemonUnavailable,
			Message: "socket path is required",
			Hint:    c.daemonHint(session, false),
			Details: map[string]any{"session": session},
		}
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
		hint := c.daemonHint(session, false)
		if c.options.StartDaemon == nil || os.Getenv("SYMBROWSE_NO_AUTOSTART") == "1" {
			hint = c.daemonHint(session, true)
		}
		return Response{}, &TransportError{
			Code:    ErrorDaemonUnavailable,
			Message: fmt.Sprintf("daemon is unavailable for session %q", session),
			Hint:    hint,
			Details: map[string]any{
				"session":     session,
				"socket_path": c.options.SocketPath,
			},
			Err: err,
		}
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(c.options.ReadTimeout)); err != nil {
		return Response{}, &TransportError{
			Code:    ErrorDaemonUnavailable,
			Message: fmt.Sprintf("failed to set deadline for session %q", session),
			Hint:    c.daemonHint(session, false),
			Details: map[string]any{
				"session":     session,
				"socket_path": c.options.SocketPath,
			},
			Err: err,
		}
	}
	if err := json.NewEncoder(conn).Encode(frame); err != nil {
		return Response{}, &TransportError{
			Code:    ErrorDaemonUnavailable,
			Message: fmt.Sprintf("failed to write daemon frame for session %q", session),
			Hint:    c.daemonHint(session, false),
			Details: map[string]any{
				"session":     session,
				"socket_path": c.options.SocketPath,
			},
			Err: err,
		}
	}
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 4096), maxFrameBytes)
	if !reader.Scan() {
		if scanErr := reader.Err(); scanErr != nil {
			if isTimeout(scanErr) {
				return Response{}, &TransportError{
					Code:    ErrorOperationTimeout,
					Message: fmt.Sprintf("daemon response timed out after %v", c.options.ReadTimeout),
					Hint:    fmt.Sprintf("increase timeout with SYMBROWSE_READ_TIMEOUT or inspect daemon logs for session %q", session),
					Details: map[string]any{
						"session":         session,
						"socket_path":     c.options.SocketPath,
						"timeout_seconds": c.options.ReadTimeout.Seconds(),
					},
					Err: scanErr,
				}
			}
			return Response{}, &TransportError{
				Code:    ErrorDaemonUnavailable,
				Message: fmt.Sprintf("failed to read daemon response for session %q", session),
				Hint:    c.daemonHint(session, false),
				Details: map[string]any{
					"session":     session,
					"socket_path": c.options.SocketPath,
				},
				Err: scanErr,
			}
		}
		return Response{}, &TransportError{
			Code:    ErrorDaemonUnavailable,
			Message: fmt.Sprintf("daemon closed connection without a response for session %q", session),
			Hint:    c.daemonHint(session, false),
			Details: map[string]any{
				"session":     session,
				"socket_path": c.options.SocketPath,
			},
		}
	}
	var response Response
	if err := json.Unmarshal(reader.Bytes(), &response); err != nil {
		return Response{}, fmt.Errorf("decode daemon response: %w", err)
	}
	return response, nil
}

// DefaultDaemonLogPath returns the path used for detached daemon startup
// diagnostics. A caller may override it with SYMBROWSE_DAEMON_LOG.
func DefaultDaemonLogPath() string {
	if path := os.Getenv("SYMBROWSE_DAEMON_LOG"); path != "" {
		return path
	}
	stateDir := os.Getenv("SYMBROWSE_STATE_DIR")
	if stateDir == "" {
		if xdgStateHome := os.Getenv("XDG_STATE_HOME"); xdgStateHome != "" {
			stateDir = filepath.Join(xdgStateHome, "symbrowse")
		} else if home, err := os.UserHomeDir(); err == nil {
			stateDir = filepath.Join(home, ".local", "state", "symbrowse")
		}
	}
	if stateDir == "" {
		stateDir = filepath.Join(os.TempDir(), "symbrowse")
	}
	return filepath.Join(stateDir, "daemon.log")
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
	return StartDaemonProcessArgsWithLog(ctx, executable, DefaultDaemonLogPath(), args...)
}

// StartDaemonProcessArgsWithLog is StartDaemonProcessArgs with an explicit
// destination for detached daemon stdout and stderr.
func StartDaemonProcessArgsWithLog(ctx context.Context, executable, logPath string, args ...string) error {
	if executable == "" {
		return errors.New("daemon executable path is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) == 0 || args[0] != "daemon" {
		return errors.New("daemon process must start with the daemon subcommand")
	}
	if logPath == "" {
		logPath = DefaultDaemonLogPath()
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("create daemon log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
