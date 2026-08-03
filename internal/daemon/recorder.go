package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// RecordedAction is one captured session action during flow recording. The
// recorder resolves session-bound @eN refs to semantic selectors immediately,
// so the draft generation (in cmd/symbrowse) never needs engine access.
type RecordedAction struct {
	Index    int    `json:"index"`
	Command  string `json:"command"`
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
}

// recorderState captures one session's actions between flow record start and
// stop. It is deliberately daemon-local: the recorder sees every frame that
// passes through the navigation runtime.
type recorderState struct {
	mu      sync.Mutex
	active  bool
	actions []RecordedAction
	index   int
}

// recordStart enables action recording for the session.
func (r *NavigationRuntime) recordStart(session string) (any, error) {
	state := r.recorder(session)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.actions = nil
	state.index = 0
	state.active = true
	return map[string]any{"recording": true, "session": session}, nil
}

// recordStop disables recording and returns the captured actions for draft
// generation.
func (r *NavigationRuntime) recordStop(session string) (any, error) {
	state := r.recorder(session)
	state.mu.Lock()
	state.active = false
	actions := make([]RecordedAction, len(state.actions))
	copy(actions, state.actions)
	state.mu.Unlock()
	return map[string]any{
		"recording": false,
		"session":   session,
		"actions":   actions,
	}, nil
}

// recordStatus reports whether recording is active for the session.
func (r *NavigationRuntime) recordStatus(session string) (any, error) {
	state := r.recorder(session)
	state.mu.Lock()
	defer state.mu.Unlock()
	return map[string]any{"recording": state.active, "session": session, "actions": len(state.actions)}, nil
}

// recordFrame captures one frame when recording is active. Navigation and
// interaction commands are kept; pure inspection commands are ignored. The
// session engine's ref table is consulted to resolve @eN selectors into
// semantic role+name selectors.
func (r *NavigationRuntime) recordFrame(ctx context.Context, session string, frame Frame) {
	if !isRecordableCommand(frame.Cmd) {
		return
	}
	state := r.recorder(session)
	state.mu.Lock()
	if !state.active {
		state.mu.Unlock()
		return
	}
	action := RecordedAction{Index: state.index, Command: frame.Cmd}
	state.index++
	state.mu.Unlock()

	action.Selector, action.Value = recordArgs(frame)
	if strings.HasPrefix(action.Selector, "@") {
		if service, err := r.service(ctx, session); err == nil {
			ref := strings.TrimPrefix(action.Selector, "@")
			if snapshotRef, ok := service.LookupRef(ref); ok {
				action.Role = snapshotRef.Role
				action.Name = snapshotRef.Name
			}
		}
	}
	state.mu.Lock()
	state.actions = append(state.actions, action)
	state.mu.Unlock()
}

// recorder returns (and lazily creates) the per-session recorder state.
func (r *NavigationRuntime) recorder(session string) *recorderState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recorders == nil {
		r.recorders = make(map[string]*recorderState)
	}
	state := r.recorders[session]
	if state == nil {
		state = &recorderState{}
		r.recorders[session] = state
	}
	return state
}

// recordArgs extracts the selector/url and value from a frame's args.
func recordArgs(frame Frame) (selector, value string) {
	var raw struct {
		URL      string `json:"url"`
		Selector string `json:"selector"`
		Value    string `json:"value"`
	}
	if len(frame.Args) > 0 {
		_ = json.Unmarshal(frame.Args, &raw)
	}
	if raw.URL != "" {
		return raw.URL, ""
	}
	if raw.Selector != "" {
		return raw.Selector, raw.Value
	}
	return "", raw.Value
}

// isRecordableCommand reports whether a command belongs in a recorded flow.
// find is deliberately excluded: the flow runner uses it internally to
// resolve semantic selectors before click/fill, so recording it would
// duplicate steps and produce empty selectors.
func isRecordableCommand(command string) bool {
	switch command {
	case "open", "goto", "click", "dblclick", "fill", "type", "press",
		"hover", "focus", "select", "check", "uncheck", "wait", "snapshot",
		"scroll", "scrollintoview":
		return true
	default:
		return false
	}
}

// handleRecordFrame dispatches the flow.record.* protocol commands.
func (r *NavigationRuntime) handleRecordFrame(ctx context.Context, frame Frame) (any, error) {
	switch frame.Cmd {
	case "flow.record.start":
		return r.recordStart(frame.Session)
	case "flow.record.stop":
		return r.recordStop(frame.Session)
	case "flow.record.status":
		return r.recordStatus(frame.Session)
	default:
		return nil, fmt.Errorf("unknown recording command %q", frame.Cmd)
	}
}
