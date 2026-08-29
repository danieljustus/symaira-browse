//go:build windows

package daemon

import "syscall"

func daemonSysProcAttr() *syscall.SysProcAttr {
	return nil
}
