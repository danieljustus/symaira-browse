package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// PolicyRuntime exposes the local policy engine and the optional symguard
// delegation over the daemon protocol (issue #52).
type PolicyRuntime struct {
	policy *policy.Policy
	mode   policy.Mode
	guard  *policy.Guard
}

// NewPolicyRuntime loads the policy from <state-dir>/policy.toml (missing
// file = built-in defaults) and detects the symguard binary. The mode
// distinguishes MCP and TTY defaults.
func NewPolicyRuntime(stateDir string, mode policy.Mode) *PolicyRuntime {
	return NewPolicyRuntimeWithGuard(stateDir, mode, policy.DetectGuard())
}

// NewPolicyRuntimeWithGuard is NewPolicyRuntime with an explicit guard (nil
// disables delegation). Tests use it to inject a fake guard.
func NewPolicyRuntimeWithGuard(stateDir string, mode policy.Mode, guard *policy.Guard) *PolicyRuntime {
	path := filepath.Join(stateDir, "policy.toml")
	p, err := policy.LoadPolicy(path)
	if err != nil {
		// Invalid user policy must not silently fall back to defaults:
		// surface it as a missing-policy marker so explain can report it.
		p = &policy.Policy{Source: path}
	}
	return &PolicyRuntime{policy: p, mode: mode, guard: guard}
}

// Handle executes policy frames.
func (r *PolicyRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	switch frame.Cmd {
	case "policy.explain":
		var request struct {
			Command string `json:"command"`
			URL     string `json:"url,omitempty"`
			Mode    string `json:"mode,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		mode := r.mode
		if request.Mode != "" {
			mode = policy.Mode(request.Mode)
		}
		explanation, err := r.policy.Explain(request.Command, request.URL, mode)
		if err != nil {
			return nil, nil, err
		}
		_, decider, reason, err := r.Decide(ctx, request.Command, request.URL, mode, nil)
		if err != nil {
			return nil, nil, err
		}
		explanation = r.explainWithDecider(explanation, decider, reason)
		return map[string]any{
			"explanation":  explanation,
			"source":       r.policy.Source,
			"decider":      decider,
			"guard_active": r.guard != nil && r.guard.Active(),
		}, nil, nil
	default:
		return nil, nil, errors.New("unknown policy command")
	}
}

// explainWithDecider appends the decider lines to a policy explanation.
func (r *PolicyRuntime) explainWithDecider(explanation, decider, reason string) string {
	guard := "not configured"
	if r.guard != nil && r.guard.Active() {
		guard = r.guard.Executable
	}
	return explanation + fmt.Sprintf("\ndecider:   %s\nguard:     %s\nreason:    %s", decider, guard, reason)
}

// Decide resolves the effective decision for one command. When symguard is
// present and configured the verdict is delegated to it (command, class,
// domain and warnings as input); the guard's decision wins. A guard failure
// denies with a clear reason — never a silent allow. The returned decider is
// "guard" when symguard decided and "policy" otherwise; the returned reason
// explains the origin (rule:domain, default, guard:<reason>).
func (r *PolicyRuntime) Decide(ctx context.Context, command, url string, mode policy.Mode, warnings []string) (policy.Decision, string, string, error) {
	class, err := policy.Classify(command)
	if err != nil {
		return "", "", "", err
	}
	host := hostOfURL(url)
	if r.guard != nil && r.guard.Active() {
		outcome, guardErr := r.guard.Decide(ctx, policy.GuardInput{
			Command:  command,
			Class:    class,
			Domain:   host,
			Warnings: warnings,
		})
		if guardErr != nil {
			return policy.Deny, "guard", fmt.Sprintf("guard failure: %v", guardErr), nil
		}
		reason := "guard:" + outcome.Reason
		if reason == "guard:" {
			reason = "guard"
		}
		return outcome.Decision, "guard", reason, nil
	}
	decision, origin := r.policy.Decide(class, host, mode)
	return decision, "policy", origin, nil
}

// DeciderFor reports who would decide a command ("guard" or "policy")
// without invoking the guard (used by the journal for its decider field).
func (r *PolicyRuntime) DeciderFor(command string) string {
	if r.guard != nil && r.guard.Active() {
		if _, err := policy.Classify(command); err == nil {
			return "guard"
		}
	}
	return "policy"
}

// Policy returns the loaded policy (for the OOB approval gate).
func (r *PolicyRuntime) Policy() *policy.Policy { return r.policy }

// PolicyFilePath returns where the policy file is expected.
func (r *PolicyRuntime) PolicyFilePath() string { return r.policy.Source }

// Guard returns the configured symguard delegation (nil when absent).
func (r *PolicyRuntime) Guard() *policy.Guard { return r.guard }
