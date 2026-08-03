package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// PolicyRuntime exposes the local policy engine over the daemon protocol.
type PolicyRuntime struct {
	policy *policy.Policy
	mode   policy.Mode
}

// NewPolicyRuntime loads the policy from <state-dir>/policy.toml (missing
// file = built-in defaults). The mode distinguishes MCP and TTY defaults.
func NewPolicyRuntime(stateDir string, mode policy.Mode) *PolicyRuntime {
	path := filepath.Join(stateDir, "policy.toml")
	p, err := policy.LoadPolicy(path)
	if err != nil {
		// Invalid user policy must not silently fall back to defaults:
		// surface it as a missing-policy marker so explain can report it.
		p = &policy.Policy{Source: path}
	}
	return &PolicyRuntime{policy: p, mode: mode}
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
		return map[string]any{"explanation": explanation, "source": r.policy.Source}, nil, nil
	default:
		return nil, nil, errors.New("unknown policy command")
	}
}

// Policy returns the loaded policy (for the OOB approval gate).
func (r *PolicyRuntime) Policy() *policy.Policy { return r.policy }

// PolicyFilePath returns where the policy file is expected.
func (r *PolicyRuntime) PolicyFilePath() string { return r.policy.Source }

// policyFileExists reports whether the user has a custom policy file.
func (r *PolicyRuntime) policyFileExists() bool {
	_, err := os.Stat(r.policy.Source)
	return err == nil
}
