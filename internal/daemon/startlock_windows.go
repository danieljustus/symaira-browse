//go:build windows

package daemon

// acquireStartupLock is a no-op on Windows: the daemon socket path is not a
// Unix socket there, so the cross-process startup lock has no target. Windows
// startups fall back to the bind itself being exclusive.
func acquireStartupLock(string) (releaseFunc, error) {
	return func() {}, nil
}
