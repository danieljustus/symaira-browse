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

// Guard delegates risk decisions to the external symguard binary (issue #52;
// "Browse klassifiziert, Guard entscheidet"). The
// guard is detected at runtime and invoked as a subprocess — there is no
// compile-time import of symaira-guard (standalone-first).
//
// Wire contract (symguard decide, stdin JSON → stdout JSON):
//
//	request:  {"command": "...", "risk_class": "low|medium|high|critical",
//	           "domain": "...", "warnings": ["..."]}
//	response: {"decision": "allow|confirm|deny", "reason": "..."}
//
// symbrowse classifies with its own risk vocabulary (read, navigate, …);
// the risk class is mapped to the guard's four levels before sending. Every
// failure mode — missing binary, non-zero exit, timeout, unparseable or
// invalid verdict — is returned as an error so the caller can deny instead
// of silently allowing.
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
	Command  string
	Class    RiskClass
	Domain   string
	Warnings []string
}

// guardRiskLevel maps a symbrowse risk class onto symguard's four risk
// levels. The mapping is deliberately conservative: classes that can change
// state or exfiltrate data are rated at least "high", code execution and
// credentials are "critical".
func guardRiskLevel(class RiskClass) (string, error) {
	switch class {
	case ClassRead:
		return "low", nil
	case ClassNavigate:
		return "medium", nil
	case ClassInteract:
		return "medium", nil
	case ClassDownload:
		return "medium", nil
	case ClassSubmit, ClassUpload:
		return "high", nil
	case ClassNetworkMock:
		return "high", nil
	case ClassEval, ClassCredential:
		return "critical", nil
	default:
		return "", fmt.Errorf("symguard: no risk level for class %q", class)
	}
}

// GuardOutcome is the guard's verdict.
type GuardOutcome struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
}

// Decide asks symguard for the verdict via `symguard decide`, sending the
// request as one JSON object on stdin. Every failure mode — missing binary,
// non-zero exit, timeout, unparseable or invalid verdict — is returned as an
// error so the caller can deny instead of silently allowing.
func (g *Guard) Decide(ctx context.Context, input GuardInput) (GuardOutcome, error) {
	if !g.Active() {
		return GuardOutcome{}, fmt.Errorf("symguard is not configured")
	}
	level, err := guardRiskLevel(input.Class)
	if err != nil {
		return GuardOutcome{}, err
	}
	payload := struct {
		Command   string   `json:"command"`
		RiskClass string   `json:"risk_class"`
		Domain    string   `json:"domain"`
		Warnings  []string `json:"warnings,omitempty"`
	}{
		Command:   input.Command,
		RiskClass: level,
		Domain:    input.Domain,
		Warnings:  input.Warnings,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return GuardOutcome{}, fmt.Errorf("symguard: marshal request: %w", err)
	}
	command := exec.CommandContext(ctx, g.Executable, "decide")
	command.Stdin = bytes.NewReader(raw)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
		command = exec.CommandContext(ctx, g.Executable, "decide")
		command.Stdin = bytes.NewReader(raw)
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
