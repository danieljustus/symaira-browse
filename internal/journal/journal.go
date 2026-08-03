// Package journal implements the append-only action journal (issue B-41):
// every action is recorded as a JSONL entry under
// <state-dir>/journal/<session>.jsonl with 0600 permissions. The schema is
// versioned and deliberately compatible with the signed journal planned by
// symaira-room.
package journal

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
)

// SchemaVersion is the stable version of the on-disk journal schema.
const SchemaVersion = 1

// Entry is one journaled action. Args and Result are redacted before
// writing; the journal must never contain secrets or plaintext passwords.
type Entry struct {
	SchemaVersion int    `json:"schema_version"`
	Timestamp     string `json:"timestamp"`
	Session       string `json:"session"`
	Command       string `json:"command"`
	Args          any    `json:"args,omitempty"`
	RefKey        string `json:"refkey,omitempty"`
	URLBefore     string `json:"url_before,omitempty"`
	URLAfter      string `json:"url_after,omitempty"`
	RiskClass     string `json:"risk_class,omitempty"`
	Decider       string `json:"decider,omitempty"` // policy | human | guard
	Result        string `json:"result,omitempty"`  // ok | error:<kind>
	DurationMS    int64  `json:"duration_ms,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// Redactor masks secret values in journal payloads. Fields whose names match
// the secret keys (password, secret, token, authorization, ...) are replaced
// with the mask.
type Redactor struct {
	// Keys are lower-cased field names that are always masked.
	Keys []string
	// Values are exact strings that are masked wherever they appear.
	Values []string
}

// DefaultRedactor masks the standard credential field names.
func DefaultRedactor() *Redactor {
	return &Redactor{Keys: []string{
		"password", "pass", "secret", "token", "authorization", "api_key", "apikey", "cookie", "credential", "value",
	}}
}

// Redact returns a deep-copied, redacted version of value.
func (r *Redactor) Redact(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"redacted": true}
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{"redacted": true}
	}
	redacted := r.redactValue(decoded)
	// Second pass: exact-value masking on the serialized form.
	out, _ := json.Marshal(redacted)
	text := string(out)
	for _, secret := range r.Values {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "••••")
		}
	}
	var result any
	_ = json.Unmarshal([]byte(text), &result)
	return result
}

// RedactString masks exact secret values inside a string.
func (r *Redactor) RedactString(text string) string {
	for _, secret := range r.Values {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "••••")
		}
	}
	return text
}

func (r *Redactor) redactValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if r.masksKey(key) {
				out[key] = "••••"
				continue
			}
			out[key] = r.redactValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = r.redactValue(item)
		}
		return out
	default:
		return value
	}
}

func (r *Redactor) masksKey(key string) bool {
	lower := strings.ToLower(key)
	for _, candidate := range r.Keys {
		if lower == candidate || strings.HasSuffix(lower, "_"+candidate) || strings.HasSuffix(lower, "-"+candidate) {
			return true
		}
	}
	return false
}

// Journal is an append-only JSONL store for one session.
type Journal struct {
	dir      string
	session  string
	redactor *Redactor
	now      func() time.Time
}

// Options configures a Journal.
type Options struct {
	Dir      string
	Session  string
	Redactor *Redactor
	Now      func() time.Time
}

// New creates a journal for one session under dir.
func New(options Options) (*Journal, error) {
	if options.Dir == "" {
		return nil, errors.New("journal directory is required")
	}
	if options.Session == "" {
		return nil, errors.New("journal session is required")
	}
	if options.Redactor == nil {
		options.Redactor = DefaultRedactor()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := os.MkdirAll(options.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	return &Journal{dir: options.Dir, session: options.Session, redactor: options.Redactor, now: options.Now}, nil
}

// Session returns the journal's session name.
func (j *Journal) Session() string { return j.session }

// Path returns the journal file path.
func (j *Journal) Path() string { return filepath.Join(j.dir, j.session+".jsonl") }

// Append writes one entry. The entry is redacted (keys and exact values),
// stamped with the current time and appended exclusively. It returns the
// entry as written.
func (j *Journal) Append(entry Entry) (Entry, error) {
	entry.SchemaVersion = SchemaVersion
	if entry.Timestamp == "" {
		entry.Timestamp = j.now().UTC().Format(time.RFC3339Nano)
	}
	entry.Args = j.redactor.Redact(entry.Args)
	entry.Reason = j.redactor.RedactString(entry.Reason)
	if entry.Result != "" && !strings.HasPrefix(entry.Result, "error") {
		entry.Result = "ok"
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return Entry{}, fmt.Errorf("marshal journal entry: %w", err)
	}
	if err := appendLine(j.Path(), line); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// appendLine appends one JSON line exclusively with 0600 permissions,
// creating the file if needed. Append-only is enforced at the OS level with
// O_APPEND so concurrent writers cannot interleave partial lines.
func appendLine(path string, line []byte) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := fsutil.AtomicWriteFile(path, nil, 0o600); err != nil {
			return fmt.Errorf("create journal file: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open journal for append: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append journal entry: %w", err)
	}
	return file.Sync()
}

// Read returns all entries of the journal in order.
func (j *Journal) Read() ([]Entry, error) {
	raw, err := os.ReadFile(j.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read journal: %w", err)
	}
	var entries []Entry
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Never fail the whole journal because of one corrupt line:
			// record it as a tombstone so auditing stays possible.
			entry = Entry{SchemaVersion: SchemaVersion, Command: "<corrupt>", Result: "error:corrupt", Args: line}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Tail returns the last n entries.
func (j *Journal) Tail(n int) ([]Entry, error) {
	entries, err := j.Read()
	if err != nil {
		return nil, err
	}
	if n <= 0 || n >= len(entries) {
		return entries, nil
	}
	return entries[len(entries)-n:], nil
}

// Sessions returns the session names that have journal files in dir.
func Sessions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list journal sessions: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".jsonl"))
	}
	sort.Strings(names)
	return names, nil
}
