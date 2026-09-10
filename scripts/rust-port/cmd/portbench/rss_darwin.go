//go:build darwin

package main

import (
	"os"
	"syscall"
)

func peakRSS(state *os.ProcessState) int64 {
	if state == nil {
		return 0
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	return usage.Maxrss
}
