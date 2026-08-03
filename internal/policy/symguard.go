package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Guard delegates risk decisions to the external symguard binary (issue #52,
// ARCHITEKTUR.md §5.5/E5: "Browse klassifiziert, Guard entscheidet"). The
// guard is detected at runtime and invoked as a subprocess — there is no
// compile-time import of symaira-guard (standalone-first).
//
// Contract: a guard that is present and configured decides; a missing guard
// falls back to the local policy; a guard failure denies with a clear reason
// instead of silently allowing.
type Guard struct {
	// Executable is the resolved symguard binary path.
	Executable string
	// Timeout bounds a single guard invocation (default 5s).
	Timeout time.Duration
}

// GuardTimeout is the default per-decision budget.
const GuardTimeout = 5 * time.Second

// GuardEnvName overrides detection: set it to a binary path to force a
// specific guard, or to "0"/"none"/"off"/"false" to disable delegation even
// when symguard is on the PATH.
const GuardEnvName = "SYMBROWSE_SYMGUARD"

// DetectGuard resolves the guard from the environment: SYMBROWSE_SYMGUARD
// wins when set (an explicit disable value yields nil), otherwise symguard is
// looked up on the PATH. Returns nil when no guard is configured.
func DetectGuard() *Guard {
	override := strings.TrimSpace(os.Getenv(GuardEnvName))
	switch strings.ToLower(override) {
	case "0", "none", "off", "false", "disable":
		return nil
	}
	executable := override
	if executable == "" {
		path, err := exec.LookPath("symguard")
		if err != nil {
			return nil
		}
		executable = path
	}
	return &Guard{Executable: executable, Timeout: GuardTimeout}
}

// Active reports whether a guard is configured.
func (g *Guard) Active() bool { return g != nil && g.Executable != "" }

// GuardInput is the decision request sent to the guard: the classified
// command, its risk class, the target domain and any collected warnings.
type GuardInput struct {
	Command  string    `json:"command"`
	Class    RiskClass `json:"class"`
	Domain   string    `json:"domain"`
	Warnings []string  `json:"warnings,omitempty"`
	Mode     Mode      `json:"mode,omitempty"`
}

// GuardOutcome is the guard's verdict.
type GuardOutcome struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
}

// Decide asks symguard for the verdict via `symguard decide` with
// line-oriented JSON flags. Every failure mode — missing binary, non-zero
// exit, timeout, unparseable or invalid verdict — is returned as an error so
// the caller can deny instead of silently allowing.
func (g *Guard) Decide(ctx context.Context, input GuardInput) (GuardOutcome, error) {
	if !g.Active() {
		return GuardOutcome{}, fmt.Errorf("symguard is not configured")
	}
	warnings, _ := json.Marshal(input.Warnings)
	args := []string{
		"decide",
		"--command", input.Command,
		"--class", string(input.Class),
		"--domain", input.Domain,
		"--mode", string(input.Mode),
		"--warnings", string(warnings),
	}
	command := exec.CommandContext(ctx, g.Executable, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
		command = exec.CommandContext(ctx, g.Executable, args...)
		command.Stdout = &stdout
		command.Stderr = &stderr
	}
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return GuardOutcome{}, fmt.Errorf("symguard decide failed: %s", message)
	}
	var outcome GuardOutcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		return GuardOutcome{}, fmt.Errorf("symguard returned an unparseable verdict: %w", err)
	}
	switch outcome.Decision {
	case Allow, Confirm, Deny:
	default:
		return GuardOutcome{}, fmt.Errorf("symguard returned invalid decision %q", outcome.Decision)
	}
	return outcome, nil
}
