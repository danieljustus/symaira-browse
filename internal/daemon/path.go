package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

const (
	DefaultIdleTimeout      = 30 * 60 * 1e9
	DefaultOperationTimeout = 25 * 1e9
	DefaultReadTimeout      = 30 * 1e9
)

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
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}
