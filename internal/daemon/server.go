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
)

const maxFrameBytes = 1 << 20

var ErrIdleTimeout = errors.New("daemon idle timeout")

// Handler executes one application command. It must honor ctx so cancellation
// after an operation timeout can stop expensive work.
type Handler func(context.Context, Frame) (any, []Warning, error)

// Options configures a Server. Zero durations use the production defaults.
type Options struct {
	SocketPath       string
	Handler          Handler
	IdleTimeout      time.Duration
	OperationTimeout time.Duration
	ReadTimeout      time.Duration
	PeerValidator    func(net.Conn) error
}

// Server serves newline-delimited JSON frames over a protected Unix socket.
type Server struct {
	options          Options
	listener         net.Listener
	closeOnce        sync.Once
	mu               sync.RWMutex
	startedAt        time.Time
	lastRequestNanos atomic.Int64
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
	return &Server{options: options}
}

// SocketPath returns the configured socket path.
func (s *Server) SocketPath() string { return s.options.SocketPath }

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
	if err := removeStaleSocket(s.options.SocketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.options.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.options.SocketPath, err)
	}
	if err := os.Chmod(s.options.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.options.SocketPath)
		return fmt.Errorf("secure socket: %w", err)
	}
	s.mu.Lock()
	s.listener = listener
	s.startedAt = time.Now()
	s.lastRequestNanos.Store(s.startedAt.UnixNano())
	s.mu.Unlock()
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.options.SocketPath)
		s.mu.Lock()
		s.listener = nil
		s.mu.Unlock()
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
		return SuccessResponse(outcome.data, outcome.warnings)
	case <-ctx.Done():
		return ErrorResponse(ErrorOperationTimeout, "daemon operation exceeded its timeout")
	}
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
	return ErrorResponse("operation_failed", err.Error())
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
