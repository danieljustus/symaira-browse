package oob

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Notifier posts human-facing notifications. macOS uses osascript; other
// platforms log to stderr (headless-safe).
type Notifier struct {
	// RunCommand is injectable for tests.
	RunCommand func(name string, args ...string) ([]byte, error)
	// Stderr receives the fallback message on non-macOS.
	Stderr func(string)
}

// NewNotifier creates the platform notifier.
func NewNotifier() *Notifier {
	return &Notifier{RunCommand: runNotifyCommand, Stderr: func(string) {}}
}

func runNotifyCommand(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Notify posts a notification for a prompt. A failure is returned but never
// fatal: the blocking wait continues either way.
func (n *Notifier) Notify(prompt *Prompt) error {
	if runtime.GOOS != "darwin" {
		n.Stderr(fmt.Sprintf("oob %s (%s): %s", prompt.ID, prompt.Kind, prompt.Title))
		return nil
	}
	title := "Symaira Browse: " + string(prompt.Kind)
	message := prompt.Title
	if prompt.Reason != "" {
		message = message + " — " + prompt.Reason
	}
	script := fmt.Sprintf("display notification %s with title %s", shellQuote(message), shellQuote(title))
	out, err := n.RunCommand("osascript", "-e", script)
	if err != nil {
		return fmt.Errorf("macOS notification failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shellQuote(value string) string {
	quoted, _ := json.Marshal(value)
	return string(quoted)
}
