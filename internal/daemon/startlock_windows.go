//go:build windows

package daemon

// socketOwnershipSupported is false on Windows: there is no cross-process
// startup lock here and a dial against a dead AF_UNIX path does not report a
// distinguishable "connection refused", so a liveness probe cannot tell a
// stale socket from a live one. Startup therefore keeps the historical
// behavior of replacing the socket file (issue #371).
const socketOwnershipSupported = false

// acquireStartupLock is a no-op on Windows: the daemon socket path is not a
// Unix socket there, so the cross-process startup lock has no target. Windows
// startups fall back to the bind itself being exclusive.
func acquireStartupLock(string) (releaseFunc, error) {
	return func() {}, nil
}
