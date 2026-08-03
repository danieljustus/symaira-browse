//go:build !darwin && !linux

package daemon

import (
	"errors"
	"net"
)

func validatePeerUID(net.Conn) error {
	return errors.New("peer credential checks are unavailable on this platform")
}
