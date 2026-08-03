package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const SessionSchemaVersion = 1

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrInvalidSession  = errors.New("invalid session name")
)

// SessionRegistryOptions configures the in-memory registry and its private
// browser-profile root. UserDataRoot is injectable so tests never need to
// touch a real browser profile.
type SessionRegistryOptions struct {
	UserDataRoot string
	PID          int
	Now          func() time.Time
}

// SessionInfo is the stable machine-readable shape used by session list/info.
type SessionInfo struct {
	Name             string `json:"name"`
	PID              int    `json:"pid"`
	StartedAt        string `json:"started_at"`
	ActiveTabs       int    `json:"active_tabs"`
	LastActivity     string `json:"last_activity"`
	UserDataDir      string `json:"user_data_dir"`
	BrowserContextID string `json:"browser_context_id"`
	RefCount         int    `json:"ref_count"`
}

// SessionListData is the stable payload returned by session.list.
type SessionListData struct {
	SchemaVersion int           `json:"schema_version"`
	Sessions      []SessionInfo `json:"sessions"`
}

// Session is a daemon-owned browser context. Its ref table is deliberately
// private: callers can only mutate it through the registry, which keeps each
// session's references isolated.
type Session struct {
	Name             string
	PID              int
	UserDataDir      string
	BrowserContextID string
	startedAt        time.Time
	lastActivity     time.Time
	activeTabs       int
	refs             map[string]string
}

// SessionRegistry owns all session state for one daemon instance. The current
// B-06 architecture uses one socket per session, so a production daemon
// normally contains one entry; keeping the registry daemon-local makes the
// boundary protocol-neutral and allows future shared-socket listing without a
// second state store.
type SessionRegistry struct {
	mu           sync.RWMutex
	userDataRoot string
	pid          int
	now          func() time.Time
	sessions     map[string]*Session
}

// NewSessionRegistry constructs an empty registry. Session directories are
// created lazily by Ensure, so construction itself has no filesystem effects.
func NewSessionRegistry(options SessionRegistryOptions) *SessionRegistry {
	if options.PID == 0 {
		options.PID = os.Getpid()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	root := options.UserDataRoot
	if root == "" {
		root = defaultUserDataRoot()
	}
	return &SessionRegistry{
		userDataRoot: filepath.Clean(root),
		pid:          options.PID,
		now:          options.Now,
		sessions:     make(map[string]*Session),
	}
}

// UserDataRoot returns the root under which session-specific profiles live.
func (r *SessionRegistry) UserDataRoot() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.userDataRoot
}

// Ensure returns the named session, creating its private profile and ref table
// if needed. Names are validated before being used as filesystem paths.
func (r *SessionRegistry) Ensure(name string) (*Session, error) {
	if !validSession.MatchString(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSession, name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if session, ok := r.sessions[name]; ok {
		return session, nil
	}
	now := r.now()
	userDataDir := filepath.Join(r.userDataRoot, name)
	if err := os.MkdirAll(userDataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create user data directory for session %q: %w", name, err)
	}
	if err := os.Chmod(userDataDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure user data directory for session %q: %w", name, err)
	}
	session := &Session{
		Name:             name,
		PID:              r.pid,
		UserDataDir:      userDataDir,
		BrowserContextID: "context-" + name,
		startedAt:        now,
		lastActivity:     now,
		refs:             make(map[string]string),
	}
	r.sessions[name] = session
	return session, nil
}

// Get returns a snapshot of one session's stable metadata.
func (r *SessionRegistry) Get(name string) (SessionInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[name]
	if !ok {
		return SessionInfo{}, fmt.Errorf("%w: %q", ErrSessionNotFound, name)
	}
	return session.info(), nil
}

// List returns session metadata in name order for deterministic JSON output.
func (r *SessionRegistry) List() []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.sessions))
	for name := range r.sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]SessionInfo, 0, len(names))
	for _, name := range names {
		result = append(result, r.sessions[name].info())
	}
	return result
}

// ListData is the protocol payload for session.list.
func (r *SessionRegistry) ListData() SessionListData {
	return SessionListData{SchemaVersion: SessionSchemaVersion, Sessions: r.List()}
}

// Touch updates the last activity timestamp without changing session start
// time. It is safe to call for an unknown session; request handling uses
// Ensure first when a command creates or uses a session.
func (r *SessionRegistry) Touch(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrSessionNotFound, name)
	}
	session.lastActivity = r.now()
	return nil
}

// SetActiveTabs records the number of active tabs owned by a session.
func (r *SessionRegistry) SetActiveTabs(name string, count int) error {
	if count < 0 {
		return errors.New("active tab count cannot be negative")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrSessionNotFound, name)
	}
	session.activeTabs = count
	return nil
}

// SetRef associates a stable ref key with a session-local reference.
func (r *SessionRegistry) SetRef(name, key, ref string) error {
	if key == "" || ref == "" {
		return errors.New("ref key and ref are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrSessionNotFound, name)
	}
	session.refs[key] = ref
	return nil
}

// Ref resolves a session-local ref key.
func (r *SessionRegistry) Ref(name, key string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrSessionNotFound, name)
	}
	ref, ok := session.refs[key]
	if !ok {
		return "", fmt.Errorf("ref %q not found in session %q", key, name)
	}
	return ref, nil
}

// RefTable returns a copy of the session's ref table.
func (r *SessionRegistry) RefTable(name string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, name)
	}
	refs := make(map[string]string, len(session.refs))
	for key, ref := range session.refs {
		refs[key] = ref
	}
	return refs, nil
}

// Clear drops all live session state while leaving profile directories intact.
// It is called when a daemon exits, including idle shutdown; a subsequent
// daemon has a fresh registry and must explicitly re-open a session.
func (r *SessionRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = make(map[string]*Session)
}

func (s *Session) info() SessionInfo {
	return SessionInfo{
		Name:             s.Name,
		PID:              s.PID,
		StartedAt:        s.startedAt.UTC().Format(time.RFC3339Nano),
		ActiveTabs:       s.activeTabs,
		LastActivity:     s.lastActivity.UTC().Format(time.RFC3339Nano),
		UserDataDir:      s.UserDataDir,
		BrowserContextID: s.BrowserContextID,
		RefCount:         len(s.refs),
	}
}

func defaultUserDataRoot() string {
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		return filepath.Join(os.TempDir(), "symbrowse", "sessions")
	}
	return filepath.Join(cache, "symbrowse", "sessions")
}
