//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// socketOwnershipSupported reports whether this platform can decide session
// socket ownership: it needs both a cross-process startup lock and a Unix
// socket whose dial errors distinguish "nothing is listening" from other
// failures (issue #371).
const socketOwnershipSupported = true

// acquireStartupLock takes an exclusive advisory lock on path. Concurrent
// daemon starts for one session serialize on it, so socket ownership is
// decided by exactly one process at a time (issue #371).
//
// The lock is advisory and held on an open file descriptor, so it is released
// automatically when the process exits — a crashed starter never wedges a
// session.
func acquireStartupLock(path string) (releaseFunc, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon startup lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire daemon startup lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
