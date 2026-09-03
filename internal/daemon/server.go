package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danieljustus/symaira-browse/internal/budget"
)

const maxFrameBytes = 1 << 20

var ErrIdleTimeout = errors.New("daemon idle timeout")

// Handler executes one application command. It must honor ctx so cancellation
// after an operation timeout can stop expensive work.
type Handler func(context.Context, Frame) (any, []Warning, error)

// PolicyStatus reports the network-policy configuration of a running daemon.
// It is part of the daemon.status payload so clients (notably the MCP server)
// can verify that a pre-existing daemon enforces the policy they require.
type PolicyStatus struct {
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	SSRFEnabled    bool     `json:"ssrf_enabled"`
	AllowPrivate   bool     `json:"allow_private"`
}

// Options configures a Server. Zero durations use the production defaults.
type Options struct {
	SocketPath       string
	Session          string
	Handler          Handler
	Registry         *SessionRegistry
	IdleTimeout      time.Duration
	OperationTimeout time.Duration
	ReadTimeout      time.Duration
	PeerValidator    func(net.Conn) error
	Policy           PolicyStatus
	// CacheDir is the truncate-and-store output cache root (issue #23).
	// Frame max_tokens budgets are enforced against it; empty disables
	// budgets (the daemon fails closed when a budget is requested).
	CacheDir string
	// CacheTTL is the output cache entry lifetime (issue #23; default 24h).
	CacheTTL time.Duration
}

// Server serves newline-delimited JSON frames over a protected Unix socket.
type Server struct {
	options          Options
	listener         net.Listener
	closeOnce        sync.Once
	mu               sync.RWMutex
	startedAt        time.Time
	lastRequestNanos atomic.Int64
	registry         *SessionRegistry
}

// NewServer constructs a daemon server without binding its socket.
func NewServer(options Options) *Server {
	if options.IdleTimeout == 0 {
		options.IdleTimeout = DefaultIdleTimeout
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = DefaultOperationTimeout
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = DefaultReadTimeout
	}
	if options.PeerValidator == nil {
		options.PeerValidator = validatePeerUID
	}
	if options.Session == "" {
		options.Session = "default"
	}
	if options.Registry == nil {
		options.Registry = NewSessionRegistry(SessionRegistryOptions{})
	}
	return &Server{options: options, registry: options.Registry}
}

// SocketPath returns the configured socket path.
func (s *Server) SocketPath() string { return s.options.SocketPath }

// Registry returns the daemon-local session registry.
func (s *Server) Registry() *SessionRegistry { return s.registry }

// Policy returns the network policy this server reports through daemon.status.
func (s *Server) Policy() PolicyStatus { return s.options.Policy }

// bindLocked clears a provably dead socket and binds a fresh one. It must run
// under the startup lock so the liveness probe and the bind cannot interleave
// with another starter (issue #371).
func (s *Server) bindLocked() (net.Listener, error) {
	if err := removeStaleSocket(s.options.SocketPath); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", s.options.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", s.options.SocketPath, err)
	}
	return listener, nil
}

// ListenAndServe binds the socket and serves until ctx is canceled, Close is
// called, or the configured idle timeout expires.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if s.options.SocketPath == "" {
		return errors.New("socket path is required")
	}
	if s.options.Handler == nil {
		s.options.Handler = func(context.Context, Frame) (any, []Warning, error) {
			return nil, nil, fmt.Errorf("no command handler is configured")
		}
	}
	if err := prepareSocketDir(filepathDir(s.options.SocketPath)); err != nil {
		return err
	}
	// Stale-socket handling and bind must be atomic across processes: two
	// MCP clients recovering the same session concurrently would otherwise
	// both find the socket free, and the second would unlink and rebind the
	// first one's socket (issue #371). The lock is released as soon as this
	// process owns the listener.
	release, err := acquireStartupLock(startupLockPath(s.options.SocketPath))
	if err != nil {
		return err
	}
	listener, err := s.bindLocked()
	release()
	if err != nil {
		return err
	}
	if err := os.Chmod(s.options.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.options.SocketPath)
		return fmt.Errorf("secure socket: %w", err)
	}
	startedAt := time.Now()
	if _, err := s.registry.Ensure(s.options.Session); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.options.SocketPath)
		return err
	}
	s.mu.Lock()
	s.listener = listener
	s.startedAt = startedAt
	s.lastRequestNanos.Store(startedAt.UnixNano())
	s.mu.Unlock()
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.options.SocketPath)
		s.mu.Lock()
		s.listener = nil
		s.mu.Unlock()
		s.registry.Clear()
	}()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if s.options.IdleTimeout > 0 && time.Since(s.lastActivity()) >= s.options.IdleTimeout {
			return ErrIdleTimeout
		}
		if deadlineListener, ok := listener.(interface{ SetDeadline(time.Time) error }); ok {
			_ = deadlineListener.SetDeadline(time.Now().Add(250 * time.Millisecond))
		}
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return fmt.Errorf("accept daemon connection: %w", err)
		}
		go s.serveConn(conn)
	}
}

// Close stops accepting new connections. Existing handlers are allowed to
// finish their current frame and connections close naturally.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.RLock()
		listener := s.listener
		s.mu.RUnlock()
		if listener != nil {
			err = listener.Close()
		}
	})
	return err
}

func (s *Server) serveConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := s.options.PeerValidator(conn); err != nil {
		return
	}
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 4096), maxFrameBytes)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(s.options.ReadTimeout)); err != nil {
			return
		}
		if !reader.Scan() {
			return
		}
		s.lastRequestNanos.Store(time.Now().UnixNano())
		response := s.handleLine(reader.Bytes())
		if err := conn.SetWriteDeadline(time.Now().Add(s.options.ReadTimeout)); err != nil {
			return
		}
		if err := json.NewEncoder(conn).Encode(response); err != nil {
			return
		}
		if response.Success && isStopFrame(reader.Bytes()) {
			_ = s.Close()
			return
		}
	}
}

func (s *Server) handleLine(line []byte) Response {
	frame, err := DecodeFrame(line)
	if err != nil {
		return ErrorResponse(ErrorMalformedRequest, err.Error())
	}
	if frame.Session == "" {
		frame.Session = s.options.Session
	}
	if !validSession.MatchString(frame.Session) {
		return ErrorResponse(ErrorInvalidSession, fmt.Sprintf("invalid session %q", frame.Session))
	}
	switch frame.Cmd {
	case "session.list":
		_ = s.registry.Touch(frame.Session)
		return SuccessResponse(s.registry.ListData(), nil)
	case "session.info":
		if err := s.registry.Touch(frame.Session); err != nil {
			return sessionErrorResponse(err)
		}
		info, err := s.registry.Get(frame.Session)
		if err != nil {
			return sessionErrorResponse(err)
		}
		return SuccessResponse(info, nil)
	}
	if _, err := s.registry.Ensure(frame.Session); err != nil {
		return ErrorResponse(ErrorInvalidSession, err.Error())
	}
	_ = s.registry.Touch(frame.Session)
	if frame.Cmd == "daemon.status" {
		return SuccessResponse(s.statusData(), nil)
	}
	if frame.Cmd == "daemon.stop" {
		return SuccessResponse(map[string]any{"stopping": true}, nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.options.OperationTimeout)
	defer cancel()
	result := make(chan handlerResult, 1)
	go func() {
		data, warnings, err := s.options.Handler(ctx, frame)
		result <- handlerResult{data: data, warnings: warnings, err: err}
	}()
	select {
	case outcome := <-result:
		if outcome.err != nil {
			return handlerErrorResponse(outcome.err)
		}
		data := outcome.data
		if frame.MaxTokens != nil && data != nil {
			// Token budget (issue #23, B-19): the serialized payload must
			// never exceed the budget. On truncation the full payload goes
			// to the output cache and the response carries head+foot plus
			// the cache handle. Fail closed when the cache is unavailable.
			budgeted, budgetErr := applyTokenBudget(s.options.CacheDir, s.options.CacheTTL, data, *frame.MaxTokens)
			if budgetErr != nil {
				return ErrorResponse(ErrorOperationFailed, budgetErr.Error())
			}
			data = budgeted
		}
		return SuccessResponse(data, outcome.warnings)
	case <-ctx.Done():
		return ErrorResponse(ErrorOperationTimeout, "daemon operation exceeded its timeout")
	}
}

// applyTokenBudget truncates data to maxTokens via internal/budget. A nil or
// empty cache directory disables the feature only for non-budgeted frames;
// budgeted frames fail closed.
func applyTokenBudget(cacheDir string, ttl time.Duration, data any, maxTokens int) (any, error) {
	if cacheDir == "" {
		return nil, errors.New("token budget requested but no output cache directory is configured")
	}
	cache := budget.NewCache(cacheDir, ttl)
	return budget.Apply(cache, data, maxTokens)
}

type handlerResult struct {
	data     any
	warnings []Warning
	err      error
}

func handlerErrorResponse(err error) Response {
	var protocolErr *Error
	if errors.As(err, &protocolErr) {
		return Response{Success: false, Error: protocolErr}
	}
	var metadata metadataError
	if errors.As(err, &metadata) {
		retryable := metadata.RetryableError()
		requiresConfirmation := metadata.RequiresConfirmation()
		return Response{Success: false, Error: &Error{
			Code:                     metadata.ErrorCode(),
			Message:                  err.Error(),
			Retryable:                &retryable,
			RequiresUserConfirmation: &requiresConfirmation,
			ResumeHint:               metadata.ResumeGuidance(),
		}}
	}
	return ErrorResponse("operation_failed", err.Error())
}

type metadataError interface {
	ErrorCode() string
	RetryableError() bool
	RequiresConfirmation() bool
	ResumeGuidance() string
}

func sessionErrorResponse(err error) Response {
	switch {
	case errors.Is(err, ErrSessionNotFound):
		return ErrorResponse(ErrorSessionNotFound, err.Error())
	case errors.Is(err, ErrInvalidSession):
		return ErrorResponse(ErrorInvalidSession, err.Error())
	default:
		return handlerErrorResponse(err)
	}
}

func (s *Server) statusData() map[string]any {
	s.mu.RLock()
	startedAt := s.startedAt
	s.mu.RUnlock()
	return map[string]any{
		"running":       true,
		"pid":           os.Getpid(),
		"socket":        s.options.SocketPath,
		"started_at":    startedAt.UTC().Format(time.RFC3339Nano),
		"last_activity": time.Unix(0, s.lastRequestNanos.Load()).UTC().Format(time.RFC3339Nano),
		"policy":        s.options.Policy,
	}
}

func (s *Server) lastActivity() time.Time {
	return time.Unix(0, s.lastRequestNanos.Load())
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}

func isStopFrame(line []byte) bool {
	frame, err := DecodeFrame(line)
	return err == nil && frame.Cmd == "daemon.stop"
}
