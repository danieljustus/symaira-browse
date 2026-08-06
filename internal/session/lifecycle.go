// Package session defines the resumable browser-session ownership contract.
// It is intentionally independent from the daemon and transport packages so
// CLI, MCP and OOB callers use the same state machine and hard stops.
package session

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ControlState identifies who may issue browser actions.
type ControlState string

const (
	ControlAgent          ControlState = "agent"
	ControlAgentDelegated ControlState = "agent_delegated"
	ControlUser           ControlState = "user"
)

// Completion records the explicit keep/skip decision made when a session ends.
type Completion struct {
	Keep      bool      `json:"keep"`
	Completed time.Time `json:"completed_at"`
}

// Session is the durable identity and current lifecycle snapshot. ID and
// ControlID survive daemon restart and client reconnect; no transport creates a
// second identity for the same session.
type Session struct {
	ID            string       `json:"id"`
	ControlID     string       `json:"control_id"`
	ControlState  ControlState `json:"control_state"`
	Active        bool         `json:"active"`
	HandoffReason string       `json:"handoff_reason,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at"`
	Completion    *Completion  `json:"completion,omitempty"`
}

// Transition is the append-only audit record for one ownership/lifecycle
// change. A caller can adapt it to the existing journal Entry format.
type Transition struct {
	SessionID string       `json:"session_id"`
	Action    string       `json:"action"`
	From      ControlState `json:"from"`
	To        ControlState `json:"to"`
	ControlID string       `json:"control_id"`
	Reason    string       `json:"reason,omitempty"`
	Confirmed bool         `json:"confirmed"`
	Keep      *bool        `json:"keep,omitempty"`
	At        time.Time    `json:"at"`
}

// Options configures a Manager. Journal is called before a state mutation so
// a persistence failure cannot leave an unjournaled ownership change visible.
type Options struct {
	Now     func() time.Time
	Journal func(Transition) error
}

// Manager owns session snapshots and serializes transitions.
type Manager struct {
	mu      sync.RWMutex
	now     func() time.Time
	journal func(Transition) error
	s       map[string]*Session
}

// NewManager creates an empty lifecycle manager.
func NewManager(options Options) *Manager {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{now: now, journal: options.Journal, s: make(map[string]*Session)}
}

// Create registers a new agent-controlled session.
func (m *Manager) Create(id, controlID string) (*Session, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(controlID) == "" {
		return nil, errors.New("session id and control id are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.s[id]; exists {
		return nil, fmt.Errorf("session %q already exists", id)
	}
	now := m.now().UTC()
	snapshot := &Session{ID: id, ControlID: controlID, ControlState: ControlAgent, Active: true, UpdatedAt: now}
	if err := m.record(Transition{SessionID: id, Action: "create", From: "", To: ControlAgent, ControlID: controlID, Confirmed: true, At: now}); err != nil {
		return nil, err
	}
	m.s[id] = snapshot
	return clone(snapshot), nil
}

// Snapshot returns a copy suitable for reconnect or persistence.
func (m *Manager) Snapshot(id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.s[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return clone(s), nil
}

// Restore installs a previously persisted snapshot without changing its
// stable identity. It is used after daemon restart.
func (m *Manager) Restore(snapshot Session) error {
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s[snapshot.ID] = clone(&snapshot)
	return nil
}

// Reconnect returns the same snapshot; reconnecting never resets ownership or
// silently turns a human-controlled session back into agent control.
func (m *Manager) Reconnect(id string) (*Session, error) { return m.Snapshot(id) }

// CheckAgentAccess is the single guard used before an agent action.
func (m *Manager) CheckAgentAccess(id, controlID string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.s[id]
	if !ok || !s.Active {
		return inactiveError(id)
	}
	if s.ControlState != ControlAgent || s.ControlID != controlID {
		return userControlError(id)
	}
	return nil
}

// Handoff delegates the session to the human channel without changing the
// stable session identity.
func (m *Manager) Handoff(id, agentControlID, reason string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.agentSession(id, agentControlID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("handoff reason is required")
	}
	if err := m.change(s, Transition{SessionID: id, Action: "handoff", From: s.ControlState, To: ControlAgentDelegated, ControlID: agentControlID, Reason: reason, Confirmed: true, At: m.now().UTC()}, func() {
		s.ControlState = ControlAgentDelegated
		s.HandoffReason = reason
	}); err != nil {
		return nil, err
	}
	return clone(s), nil
}

// Claim moves a delegated session into explicit human control.
func (m *Manager) Claim(id, humanControlID string) (*Session, error) {
	if strings.TrimSpace(humanControlID) == "" {
		return nil, errors.New("human control id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookupActive(id)
	if err != nil {
		return nil, err
	}
	if s.ControlState == ControlUser {
		return nil, userControlError(id)
	}
	if s.ControlState != ControlAgentDelegated {
		return nil, fmt.Errorf("session %q is not awaiting a human claim", id)
	}
	if err := m.change(s, Transition{SessionID: id, Action: "claim", From: s.ControlState, To: ControlUser, ControlID: humanControlID, Confirmed: true, At: m.now().UTC()}, func() {
		s.ControlState = ControlUser
		s.ControlID = humanControlID
	}); err != nil {
		return nil, err
	}
	return clone(s), nil
}

// Takeover returns control to an agent only after explicit confirmation. A
// missing confirmation is a hard stop and does not mutate state.
func (m *Manager) Takeover(id, agentControlID string, confirmed bool) (*Session, error) {
	if strings.TrimSpace(agentControlID) == "" {
		return nil, errors.New("agent control id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookupActive(id)
	if err != nil {
		return nil, err
	}
	if s.ControlState == ControlAgent {
		return clone(s), nil
	}
	if !confirmed {
		return nil, userControlError(id)
	}
	if err := m.change(s, Transition{SessionID: id, Action: "takeover", From: s.ControlState, To: ControlAgent, ControlID: agentControlID, Confirmed: true, At: m.now().UTC()}, func() {
		s.ControlState = ControlAgent
		s.ControlID = agentControlID
		s.HandoffReason = ""
	}); err != nil {
		return nil, err
	}
	return clone(s), nil
}

// Complete ends a session with an explicit keep/skip decision.
func (m *Manager) Complete(id, controlID string, keep bool) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookupActive(id)
	if err != nil {
		return nil, err
	}
	if s.ControlID != controlID {
		return nil, userControlError(id)
	}
	now := m.now().UTC()
	if err := m.change(s, Transition{SessionID: id, Action: "complete", From: s.ControlState, To: s.ControlState, ControlID: controlID, Confirmed: true, Keep: &keep, At: now}, func() {
		s.Active = false
		s.Completion = &Completion{Keep: keep, Completed: now}
	}); err != nil {
		return nil, err
	}
	return clone(s), nil
}

// Timeout closes a pending handoff and returns a non-retryable hard stop. The
// transition is journaled even though the caller receives the error.
func (m *Manager) Timeout(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.lookupActive(id)
	if err != nil {
		return nil, err
	}
	if s.ControlState != ControlAgentDelegated {
		return nil, fmt.Errorf("session %q has no pending handoff", id)
	}
	if err := m.change(s, Transition{SessionID: id, Action: "timeout", From: s.ControlState, To: s.ControlState, ControlID: s.ControlID, Confirmed: false, At: m.now().UTC()}, func() {
		s.Active = false
	}); err != nil {
		return nil, err
	}
	return clone(s), timeoutError(id)
}

func (m *Manager) lookupActive(id string) (*Session, error) {
	s, ok := m.s[id]
	if !ok || !s.Active {
		return nil, inactiveError(id)
	}
	return s, nil
}

func (m *Manager) agentSession(id, controlID string) (*Session, error) {
	s, err := m.lookupActive(id)
	if err != nil {
		return nil, err
	}
	if s.ControlState != ControlAgent || s.ControlID != controlID {
		return nil, userControlError(id)
	}
	return s, nil
}

func (m *Manager) change(s *Session, transition Transition, mutate func()) error {
	if err := m.record(transition); err != nil {
		return err
	}
	mutate()
	s.UpdatedAt = transition.At
	return nil
}

func (m *Manager) record(transition Transition) error {
	if m.journal == nil {
		return nil
	}
	return m.journal(transition)
}

func validateSnapshot(snapshot Session) error {
	if strings.TrimSpace(snapshot.ID) == "" || strings.TrimSpace(snapshot.ControlID) == "" {
		return errors.New("session snapshot requires id and control id")
	}
	if snapshot.ControlState != ControlAgent && snapshot.ControlState != ControlAgentDelegated && snapshot.ControlState != ControlUser {
		return fmt.Errorf("invalid control state %q", snapshot.ControlState)
	}
	if snapshot.UpdatedAt.IsZero() {
		return errors.New("session snapshot requires updated_at")
	}
	return nil
}

func clone(s *Session) *Session {
	copy := *s
	if s.Completion != nil {
		completion := *s.Completion
		copy.Completion = &completion
	}
	return &copy
}
