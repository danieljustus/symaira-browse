//go:build darwin

package daemon

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"unsafe"
)

const (
	solLocal      = 0
	localPeerCred = 0x001
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
		// Darwin's xucred starts with a version word followed by the peer UID.
		// Keep this small raw syscall local so the package remains dependency-free.
		var credential [128]byte
		length := uint32(len(credential))
		_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd,
			uintptr(solLocal), uintptr(localPeerCred),
			uintptr(unsafe.Pointer(&credential[0])), uintptr(unsafe.Pointer(&length)), 0)
		if errno != 0 {
			controlErr = errno
			return
		}
		if length < 8 {
			controlErr = fmt.Errorf("short xucred response: %d bytes", length)
			return
		}
		peerUID = *(*uint32)(unsafe.Pointer(&credential[4]))
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
