// Package state implements named persistence of browser session state
// (cookies and per-origin web storage) under the symbrowse state directory.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-corekit/fsutil"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// SchemaVersion is the stable version of the on-disk state schema.
const SchemaVersion = 1

// DefaultExpireDays is the retention window applied when
// SYMBROWSE_STATE_EXPIRE_DAYS is unset or invalid.
const DefaultExpireDays = 30

// OriginState holds the session data for one origin. Storage is keyed per
// origin so state can never leak across origins.
type OriginState struct {
	Cookies        []engine.Cookie   `json:"cookies"`
	LocalStorage   map[string]string `json:"local_storage,omitempty"`
	SessionStorage map[string]string `json:"session_storage,omitempty"`
}

// State is one named, persistable browser session snapshot.
type State struct {
	SchemaVersion int                    `json:"schema_version"`
	Name          string                 `json:"name"`
	SavedAt       string                 `json:"saved_at"`
	ExpiresAt     string                 `json:"expires_at"`
	KeySource     string                 `json:"key_source,omitempty"`
	Origins       map[string]OriginState `json:"origins"`
}

// Store persists named states under one directory with 0600 permissions and
// atomic writes. When a KeyProvider is attached, state files are encrypted
// with AES-256-GCM; without one they are stored as plaintext.
type Store struct {
	dir      string
	now      func() time.Time
	expireIn time.Duration
	keys     KeyProvider
}

// StoreOptions configures a Store. Now and ExpireIn are injectable for tests.
type StoreOptions struct {
	Dir      string
	Now      func() time.Time
	ExpireIn time.Duration
	Keys     KeyProvider
}

// NewStore creates a store rooted at dir (usually <state-dir>/states).
func NewStore(options StoreOptions) (*Store, error) {
	if options.Dir == "" {
		return nil, errors.New("state directory is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ExpireIn <= 0 {
		options.ExpireIn = time.Duration(DefaultExpireDays) * 24 * time.Hour
	}
	if err := os.MkdirAll(options.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &Store{dir: options.Dir, now: options.Now, expireIn: options.ExpireIn, keys: options.Keys}, nil
}

// WithKeyProvider returns a copy of the store that encrypts state files with
// the given key provider.
func (s *Store) WithKeyProvider(keys KeyProvider) *Store {
	clone := *s
	clone.keys = keys
	return &clone
}

// KeySource returns the currently active key source label ("none" when the
// store has no key provider).
func (s *Store) KeySource() KeySource {
	if s.keys == nil {
		return KeySourceNone
	}
	source, err := s.keys.Source()
	if err != nil {
		return KeySourceNone
	}
	return source
}

// Dir returns the store root directory.
func (s *Store) Dir() string { return s.dir }

// ExpireIn returns the configured retention window.
func (s *Store) ExpireIn() time.Duration { return s.expireIn }

// ValidateName rejects names that could escape the state directory or collide
// with the store's internal files.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("state name is required")
	}
	if len(name) > 128 {
		return errors.New("state name is too long")
	}
	if name != filepath.Base(name) {
		return errors.New("state name must not contain path separators")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("state name contains control characters")
		}
	}
	return nil
}

// path returns the on-disk path for a validated name.
func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

// Save writes a state atomically with 0600 permissions. It always refreshes
// SavedAt/ExpiresAt so a re-save extends the retention window.
func (s *Store) Save(st *State) error {
	if err := ValidateName(st.Name); err != nil {
		return err
	}
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	now := s.now()
	st.SavedAt = now.UTC().Format(time.RFC3339Nano)
	st.ExpiresAt = now.Add(s.expireIn).UTC().Format(time.RFC3339Nano)
	if st.Origins == nil {
		st.Origins = map[string]OriginState{}
	}
	raw, err := s.encode(st)
	if err != nil {
		return err
	}
	if err := fsutil.AtomicWriteFile(s.path(st.Name), raw, 0o600); err != nil {
		return fmt.Errorf("write state %q: %w", st.Name, err)
	}
	return nil
}

// Load reads and decodes one named state.
func (s *Store) Load(name string) (*State, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("state %q not found", name)
		}
		return nil, fmt.Errorf("read state %q: %w", name, err)
	}
	st, err := s.decode(raw)
	if err != nil {
		return nil, fmt.Errorf("decode state %q: %w", name, err)
	}
	st.Name = name
	return st, nil
}

// List returns the names of all stored states, sorted for deterministic
// output. Expired states are included so callers can report them.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("list states: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(names)
	return names, nil
}

// Remove deletes one named state.
func (s *Store) Remove(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := fsutil.SafeRemove(s.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove state %q: %w", name, err)
	}
	return nil
}

// Clean removes states whose ExpiresAt lies before now. It returns the names
// of the removed states.
func (s *Store) Clean() ([]string, error) {
	return s.clean(func(st *State) bool {
		expiresAt, err := time.Parse(time.RFC3339Nano, st.ExpiresAt)
		if err != nil {
			return false
		}
		return expiresAt.Before(s.now())
	})
}

// CleanOlderThan removes states whose SavedAt is older than the given age.
// It is the backing implementation of `state clean --older-than <tage>`.
func (s *Store) CleanOlderThan(age time.Duration) ([]string, error) {
	if age <= 0 {
		return nil, errors.New("age must be positive")
	}
	cutoff := s.now().Add(-age)
	return s.clean(func(st *State) bool {
		savedAt, err := time.Parse(time.RFC3339Nano, st.SavedAt)
		if err != nil {
			return false
		}
		return savedAt.Before(cutoff)
	})
}

func (s *Store) clean(expired func(*State) bool) ([]string, error) {
	names, err := s.List()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, name := range names {
		st, err := s.Load(name)
		if err != nil {
			continue
		}
		if expired(st) {
			if err := s.Remove(name); err == nil {
				removed = append(removed, name)
			}
		}
	}
	return removed, nil
}

// Expired returns the names of stored states that are already expired.
func (s *Store) Expired() ([]string, error) {
	names, err := s.List()
	if err != nil {
		return nil, err
	}
	now := s.now()
	var expired []string
	for _, name := range names {
		st, err := s.Load(name)
		if err != nil {
			continue
		}
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, st.ExpiresAt)
		if parseErr != nil {
			continue
		}
		if expiresAt.Before(now) {
			expired = append(expired, name)
		}
	}
	return expired, nil
}

// Metadata is the value-free view of a state used by state show: origins,
// cookie counts and age, but never cookie values or storage contents.
type Metadata struct {
	Name          string           `json:"name"`
	SchemaVersion int              `json:"schema_version"`
	SavedAt       string           `json:"saved_at"`
	ExpiresAt     string           `json:"expires_at"`
	KeySource     string           `json:"key_source,omitempty"`
	Origins       []OriginMetadata `json:"origins"`
}

// OriginMetadata describes one origin's state without any values.
type OriginMetadata struct {
	Origin      string `json:"origin"`
	CookieCount int    `json:"cookie_count"`
	LocalKeys   int    `json:"local_storage_keys"`
	SessionKeys int    `json:"session_storage_keys"`
}

// Metadata returns the value-free metadata view of a stored state.
func (s *Store) Metadata(name string) (Metadata, error) {
	st, err := s.Load(name)
	if err != nil {
		return Metadata{}, err
	}
	return st.Metadata(), nil
}

// Metadata converts a state into its value-free metadata view.
func (st *State) Metadata() Metadata {
	meta := Metadata{
		Name:          st.Name,
		SchemaVersion: st.SchemaVersion,
		SavedAt:       st.SavedAt,
		ExpiresAt:     st.ExpiresAt,
		KeySource:     st.KeySource,
	}
	origins := make([]string, 0, len(st.Origins))
	for origin := range st.Origins {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	for _, origin := range origins {
		entry := st.Origins[origin]
		meta.Origins = append(meta.Origins, OriginMetadata{
			Origin:      origin,
			CookieCount: len(entry.Cookies),
			LocalKeys:   len(entry.LocalStorage),
			SessionKeys: len(entry.SessionStorage),
		})
	}
	return meta
}

// jsonRoundTrip ensures stable serialization for the metadata view.
func (m Metadata) JSON() ([]byte, error) { return json.Marshal(m) }
