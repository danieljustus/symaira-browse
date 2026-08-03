//go:build linux

package daemon

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

func validatePeerUID(conn net.Conn) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("peer credentials require a Unix connection")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("get socket control: %w", err)
	}
	var peerUID uint32
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			controlErr = err
			return
		}
		peerUID = cred.Uid
	}); err != nil {
		return fmt.Errorf("inspect peer credentials: %w", err)
	}
	if controlErr != nil {
		return fmt.Errorf("inspect peer credentials: %w", controlErr)
	}
	if peerUID != uint32(os.Getuid()) {
		return fmt.Errorf("peer uid %d does not match daemon uid %d", peerUID, os.Getuid())
	}
	return nil
}
