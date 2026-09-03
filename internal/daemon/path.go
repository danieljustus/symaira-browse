package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"
)

const (
	DefaultIdleTimeout      = 30 * 60 * 1e9
	DefaultOperationTimeout = 25 * 1e9
	DefaultReadTimeout      = 30 * 1e9
	// socketProbeTimeout bounds the liveness probe of an existing session
	// socket during startup (issue #371).
	socketProbeTimeout = 250 * time.Millisecond
)

// ErrDaemonAlreadyRunning reports that another daemon already serves this
// session socket. Startup stops instead of taking the socket over, so exactly
// one daemon owns a session (issue #371).
var ErrDaemonAlreadyRunning = errors.New("a daemon is already running for this session")

// releaseFunc releases an acquired startup lock.
type releaseFunc func()

var validSession = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SocketPath resolves the platform-specific default socket path for a session.
func SocketPath(session string) (string, error) {
	if !validSession.MatchString(session) {
		return "", fmt.Errorf("invalid session %q: use 1-64 letters, digits, '.', '_' or '-'", session)
	}
	base, err := socketBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, session+".sock"), nil
}

// SocketPathIn returns a socket path under base. It is intended for tests and
// callers that explicitly own a runtime directory.
func SocketPathIn(base, session string) (string, error) {
	if !validSession.MatchString(session) {
		return "", fmt.Errorf("invalid session %q", session)
	}
	if base == "" {
		return "", errors.New("socket base directory is empty")
	}
	return filepath.Join(base, session+".sock"), nil
}

func socketBaseDir() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("determine home directory: %w", err)
		}
		return filepath.Join(home, "Library", "Caches", "symbrowse", "run"), nil
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "symbrowse"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determine cache directory: %w", err)
	}
	return filepath.Join(cache, "symbrowse", "run"), nil
}

func prepareSocketDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure socket directory: %w", err)
	}
	return nil
}

// removeStaleSocket clears a leftover socket so a new daemon can bind it.
//
// A socket file alone does not prove the previous daemon is gone: unlinking a
// live one lets a second daemon bind the same path while the first keeps
// serving its already-accepted connections, which leaves two daemons with
// split browser state for one session (issue #371). The path is therefore
// probed first and ErrDaemonAlreadyRunning is returned when something answers,
// so the losing starter connects to the winner instead of replacing it.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("socket path exists and is not a Unix socket")
	}
	// Where ownership cannot be decided (see socketOwnershipSupported), a
	// leftover socket file must still be replaceable — refusing here would
	// wedge startup permanently instead of preventing a takeover.
	if socketOwnershipSupported && socketIsLive(path) {
		return ErrDaemonAlreadyRunning
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

// socketIsLive reports whether a daemon currently accepts connections on path.
// It is only meaningful where socketOwnershipSupported is true.
// A refused or unreachable socket is stale; any other dial error is treated as
// live so an unclear state never causes a takeover.
func socketIsLive(path string) bool {
	conn, err := net.DialTimeout("unix", path, socketProbeTimeout)
	if err == nil {
		_ = conn.Close()
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		return false
	}
	return true
}

// startupLockPath returns the advisory lock guarding concurrent daemon starts
// for one session socket.
func startupLockPath(socketPath string) string {
	return socketPath + ".lock"
}
