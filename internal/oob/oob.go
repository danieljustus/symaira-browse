// Package oob implements the out-of-band human channel (ARCHITEKTUR.md §5.4):
// a page overlay plus macOS notification with blocking wait semantics. One
// mechanism serves handoff, approval and watch (issues B-44..B-47).
package oob

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Status is the lifecycle state of one OOB interaction.
type Status string

const (
	StatusPending   Status = "pending"
	StatusCompleted Status = "completed"
	StatusCancelled Status = "cancelled"
	StatusTimeout   Status = "timeout"
)

// Kind distinguishes the three OOB situations.
type Kind string

const (
	KindHandoff  Kind = "handoff"
	KindApproval Kind = "approval"
	KindWatch    Kind = "watch"
)

// Prompt is one blocking OOB interaction.
type Prompt struct {
	ID        string        `json:"id"`
	Kind      Kind          `json:"kind"`
	Title     string        `json:"title"`
	Reason    string        `json:"reason"`
	Status    Status        `json:"status"`
	CreatedAt time.Time     `json:"created_at"`
	Timeout   time.Duration `json:"timeout,omitempty"`
	// Result carries the structured outcome payload (e.g. handoff result).
	Result map[string]any `json:"result,omitempty"`
}

// Manager owns the lifecycle of OOB prompts. It is safe for concurrent use.
type Manager struct {
	mu      sync.Mutex
	next    int
	prompts map[string]*Prompt
	now     func() time.Time
}

// NewManager creates an empty prompt manager.
func NewManager() *Manager {
	return &Manager{prompts: make(map[string]*Prompt), now: time.Now}
}

// Create registers a new pending prompt and returns it.
func (m *Manager) Create(kind Kind, title, reason string, timeout time.Duration) *Prompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	prompt := &Prompt{
		ID:        fmt.Sprintf("oob-%d", m.next),
		Kind:      kind,
		Title:     title,
		Reason:    reason,
		Status:    StatusPending,
		CreatedAt: m.now(),
		Timeout:   timeout,
	}
	m.prompts[prompt.ID] = prompt
	return prompt
}

// Get returns one prompt by id.
func (m *Manager) Get(id string) (*Prompt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prompt, ok := m.prompts[id]
	if !ok {
		return nil, fmt.Errorf("oob prompt %q not found", id)
	}
	return clonePrompt(prompt), nil
}

// Active returns the newest pending prompt, if any.
func (m *Manager) Active() (*Prompt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var newest *Prompt
	for _, prompt := range m.prompts {
		if prompt.Status != StatusPending {
			continue
		}
		if newest == nil || prompt.CreatedAt.After(newest.CreatedAt) ||
			(prompt.CreatedAt.Equal(newest.CreatedAt) && prompt.ID > newest.ID) {
			newest = prompt
		}
	}
	if newest == nil {
		return nil, errors.New("no pending oob prompt")
	}
	return clonePrompt(newest), nil
}

// Complete marks a prompt completed with an optional structured result.
func (m *Manager) Complete(id string, result map[string]any) (*Prompt, error) {
	return m.finish(id, StatusCompleted, result)
}

// Cancel marks a prompt cancelled.
func (m *Manager) Cancel(id string, reason string) (*Prompt, error) {
	return m.finish(id, StatusCancelled, map[string]any{"reason": reason})
}

// Expire marks a prompt timed out. Timeout never results in a silent allow:
// callers interpret "timeout" as deny (issue B-46).
func (m *Manager) Expire(id string) (*Prompt, error) {
	return m.finish(id, StatusTimeout, nil)
}

func (m *Manager) finish(id string, status Status, result map[string]any) (*Prompt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prompt, ok := m.prompts[id]
	if !ok {
		return nil, fmt.Errorf("oob prompt %q not found", id)
	}
	if prompt.Status != StatusPending {
		return nil, fmt.Errorf("oob prompt %q is already %s", id, prompt.Status)
	}
	prompt.Status = status
	if result != nil {
		prompt.Result = result
	}
	return clonePrompt(prompt), nil
}

// Wait blocks until the prompt leaves pending, the timeout elapses, or ctx is
// cancelled. A timeout marks the prompt StatusTimeout and returns it (never a
// silent allow). A cancelled ctx returns the current prompt state and ctx.Err.
func (m *Manager) Wait(ctx context.Context, id string) (*Prompt, error) {
	deadline := time.Time{}
	if prompt, err := m.Get(id); err == nil && prompt.Timeout > 0 {
		deadline = prompt.CreatedAt.Add(prompt.Timeout)
	}
	for {
		prompt, err := m.Get(id)
		if err != nil {
			return nil, err
		}
		if prompt.Status != StatusPending {
			return prompt, nil
		}
		if !deadline.IsZero() && m.now().After(deadline) {
			expired, err := m.Expire(id)
			if err != nil {
				return prompt, nil
			}
			return expired, nil
		}
		select {
		case <-ctx.Done():
			return prompt, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ResolveWait is the approval-oriented Wait: it maps the terminal status to
// allow/deny. Only "completed" allows; cancelled and timeout deny. This is
// the B-46 contract (timeout => deny, never silent allow).
func (m *Manager) ResolveWait(ctx context.Context, id string) (bool, *Prompt, error) {
	prompt, err := m.Wait(ctx, id)
	if err != nil && !errors.Is(err, context.Canceled) {
		return false, prompt, err
	}
	return prompt.Status == StatusCompleted, prompt, nil
}

func clonePrompt(prompt *Prompt) *Prompt {
	clone := *prompt
	if prompt.Result != nil {
		clone.Result = make(map[string]any, len(prompt.Result))
		for key, value := range prompt.Result {
			clone.Result[key] = value
		}
	}
	return &clone
}

// DurationString renders the timeout for display.
func (p *Prompt) DurationString() string {
	if p.Timeout <= 0 {
		return ""
	}
	return p.Timeout.Round(time.Second).String()
}
