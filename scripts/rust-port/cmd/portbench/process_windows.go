//go:build windows

package main

import (
	"fmt"
	"os/exec"
)

func configureProcessTree(_ *exec.Cmd) {}

func killProcessTree(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	if err := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(command.Process.Pid)).Run(); err == nil {
		return nil
	}
	return command.Process.Kill()
}
