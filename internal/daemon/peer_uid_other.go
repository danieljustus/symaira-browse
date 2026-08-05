//go:build !darwin && !linux

package daemon

import (
	"net"
)

// validatePeerUID is a best-effort hardening check on platforms that expose
// peer credentials (Linux SO_PEERCRED, macOS LOCAL_PEERCRED). Windows does
// not expose an equivalent for AF_UNIX sockets, so the check degrades open:
// the socket itself stays protected by its 0600 mode inside the user's
// 0700 runtime directory, and the daemon remains usable there.
func validatePeerUID(net.Conn) error {
	return nil
}
