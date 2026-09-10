//go:build !darwin && !linux

package main

import "os"

func peakRSS(_ *os.ProcessState) int64 {
	return 0
}
