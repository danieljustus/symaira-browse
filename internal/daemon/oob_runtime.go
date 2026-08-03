package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/oob"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// OOBRuntime wires the out-of-band channel (overlay + notification + blocking
// wait) into the daemon: handoff, approvals and watch all share it.
type OOBRuntime struct {
	manager  *oob.Manager
	notifier *oob.Notifier
	nav      *NavigationRuntime
	policy   *policy.Policy
	mode     policy.Mode
	// decide is the optional guard-aware decision hook (issue #52). When set
	// it replaces the local policy decision in the approval gate.
	decide func(ctx context.Context, command, url string, mode policy.Mode, warnings []string) (policy.Decision, string, string, error)
}

// NewOOBRuntime creates the OOB channel for one daemon.
func NewOOBRuntime(manager *oob.Manager, notifier *oob.Notifier, nav *NavigationRuntime, p *policy.Policy, mode policy.Mode) *OOBRuntime {
	return &OOBRuntime{manager: manager, notifier: notifier, nav: nav, policy: p, mode: mode}
}

// SetDecider installs the guard-aware decision hook used by the approval
// gate (see PolicyRuntime.Decide).
func (r *OOBRuntime) SetDecider(decide func(ctx context.Context, command, url string, mode policy.Mode, warnings []string) (policy.Decision, string, string, error)) {
	r.decide = decide
}

// StartHandoff runs the B-45 handoff: show the overlay with the reason,
// notify the human and block until completion, cancellation or timeout.
// Headless sessions fall back to notification + oob status.
func (r *OOBRuntime) StartHandoff(ctx context.Context, session, reason string, timeout time.Duration) (map[string]any, error) {
	prompt := r.manager.Create(oob.KindHandoff, "Symaira Browse: der Agent wartet", reason, timeout)
	if err := r.notifier.Notify(prompt); err != nil {
		// Notification failure must not block the handoff.
		_ = err
	}
	service, serviceErr := r.nav.serviceIfReady(session)
	if serviceErr == nil && service != nil {
		if err := service.InstallOverlay(ctx, engine.OverlayRequest{
			Title:  "Symaira Browse: der Agent wartet",
			Reason: reason,
			ID:     prompt.ID,
		}); err != nil {
			// Overlay failure falls back to notification-only handoff.
			_ = err
		}
	}
	result, err := r.manager.Wait(ctx, prompt.ID)
	if err != nil && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	payload := map[string]any{
		"status":      string(result.Status),
		"prompt_id":   result.ID,
		"duration_ms": time.Since(result.CreatedAt).Milliseconds(),
	}
	if result.Result != nil {
		for key, value := range result.Result {
			payload[key] = value
		}
	}
	return payload, nil
}

// RequestApproval runs the B-46 approval flow: the policy says "confirm", so
// the human is asked over the OOB channel. Timeout and cancellation both
// deny; only an explicit completion allows. The outcome is journaled by the
// caller.
func (r *OOBRuntime) RequestApproval(ctx context.Context, session, command, url string, class policy.RiskClass, warnings []string, timeout time.Duration) (bool, *oob.Prompt, error) {
	reason := fmt.Sprintf("%s (%s) gegen %s", command, class, url)
	if len(warnings) > 0 {
		reason = reason + "; Injection-Warnungen: " + joinWarnings(warnings)
	}
	prompt := r.manager.Create(oob.KindApproval, "Symaira Browse: Freigabe nötig", reason, timeout)
	if err := r.notifier.Notify(prompt); err != nil {
		_ = err
	}
	allowed, result, err := r.manager.ResolveWait(ctx, prompt.ID)
	if err != nil && !errors.Is(err, context.Canceled) {
		return false, result, err
	}
	return allowed, result, nil
}

// DecideAndConfirm is the policy gate used before executing a frame: when the
// effective decision is deny, the frame is refused; when it is confirm, the
// human is asked via the OOB channel. Returns (allowed, decision, decider,
// error); the decider is "guard" when symguard decided and "policy"
// otherwise (issue #52).
func (r *OOBRuntime) DecideAndConfirm(ctx context.Context, session, command, url string, timeout time.Duration) (bool, policy.Decision, string, error) {
	var decision policy.Decision
	var decider string
	if r.decide != nil {
		var err error
		decision, decider, _, err = r.decide(ctx, command, url, r.mode, nil)
		if err != nil {
			return false, "", "", err
		}
	} else {
		class, err := policy.Classify(command)
		if err != nil {
			return false, "", "", err
		}
		decision, _ = r.policy.Decide(class, hostOfURL(url), r.mode)
		decider = "policy"
	}
	switch decision {
	case policy.Allow:
		return true, decision, decider, nil
	case policy.Deny:
		return false, decision, decider, nil
	case policy.Confirm:
		allowed, _, err := r.RequestApproval(ctx, session, command, url, classOf(r, command), nil, timeout)
		if allowed {
			decider = "human"
		}
		return allowed, decision, decider, err
	}
	return false, decision, decider, nil
}

// classOf resolves the risk class for an approval prompt, falling back to
// the classification table.
func classOf(r *OOBRuntime, command string) policy.RiskClass {
	class, err := policy.Classify(command)
	if err != nil {
		return policy.ClassInteract
	}
	return class
}

func hostOfURL(raw string) string {
	rest := raw
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			rest = rest[i+2:]
			break
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == ':' {
			return rest[:i]
		}
	}
	return rest
}

func joinWarnings(warnings []string) string {
	result := ""
	for i, warning := range warnings {
		if i > 0 {
			result += "; "
		}
		result += warning
	}
	return result
}

// Handle executes OOB frames.
func (r *OOBRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	switch frame.Cmd {
	case "oob.status":
		prompt, err := r.manager.Active()
		if err != nil {
			return map[string]any{"active": false}, nil, nil
		}
		return map[string]any{"active": true, "prompt": prompt}, nil, nil
	case "oob.complete":
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		done, err := r.manager.Complete(request.ID, nil)
		if err != nil {
			return nil, nil, err
		}
		return done, nil, nil
	case "oob.cancel":
		var request struct {
			ID     string `json:"id"`
			Reason string `json:"reason,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		cancelled, err := r.manager.Cancel(request.ID, request.Reason)
		if err != nil {
			return nil, nil, err
		}
		return cancelled, nil, nil
	case "handoff":
		var request struct {
			Reason  string `json:"reason"`
			Timeout string `json:"timeout,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		timeout := 5 * time.Minute
		if request.Timeout != "" {
			parsed, err := time.ParseDuration(request.Timeout)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid timeout %q", request.Timeout)
			}
			timeout = parsed
		}
		payload, err := r.StartHandoff(ctx, frame.Session, request.Reason, timeout)
		if err != nil {
			return nil, nil, err
		}
		return payload, nil, nil
	default:
		return nil, nil, errors.New("unknown oob command")
	}
}
