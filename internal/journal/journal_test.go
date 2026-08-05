package journal

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

func newTestJournal(t *testing.T, session string, redactor *Redactor) *Journal {
	t.Helper()
	dir := t.TempDir()
	journal, err := New(Options{Dir: dir, Session: session, Redactor: redactor})
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func TestAppendAndReadRoundTrip(t *testing.T) {
	journal := newTestJournal(t, "default", nil)
	if _, err := journal.Append(Entry{Command: "open", Args: map[string]any{"url": "https://example.com"}, RiskClass: "navigate", Decider: "policy", Result: "ok", DurationMS: 12}); err != nil {
		t.Fatal(err)
	}
	entries, err := journal.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	entry := entries[0]
	if entry.Command != "open" || entry.RiskClass != "navigate" || entry.Decider != "policy" || entry.DurationMS != 12 || entry.SchemaVersion != SchemaVersion {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Timestamp == "" {
		t.Fatal("timestamp missing")
	}
	if entry.Result != "ok" {
		t.Fatalf("result = %q", entry.Result)
	}
}

func TestAppendOnlyIsAppend(t *testing.T) {
	journal := newTestJournal(t, "default", nil)
	path := journal.Path()
	for i := 0; i < 3; i++ {
		if _, err := journal.Append(Entry{Command: "click"}); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(path)
	if lines := strings.Count(strings.TrimRight(string(raw), "\n"), "\n"); lines != 2 {
		t.Fatalf("expected 3 lines (2 newlines), got %d", lines)
	}
	entries, _ := journal.Read()
	if len(entries) != 3 {
		t.Fatalf("entries = %d", len(entries))
	}
}

func TestFilePermissions0600(t *testing.T) {
	journal := newTestJournal(t, "default", nil)
	if _, err := journal.Append(Entry{Command: "open"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX mode bits (chmod only toggles read-only).
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("permissions = %o, want 600", perm)
		}
	}
}

// TestNoSecretInJournal is the redaction acceptance test: a password typed
// into a form must never appear in the journal, neither as a field value nor
// inside a free-text reason.
func TestNoSecretInJournal(t *testing.T) {
	redactor := DefaultRedactor()
	redactor.Values = append(redactor.Values, "hunter2-secret", "ada")
	journal := newTestJournal(t, "default", redactor)
	if _, err := journal.Append(Entry{
		Command: "fill",
		Args: map[string]any{
			"selector": "#pass",
			"value":    "hunter2-secret",
		},
		RiskClass: "credential",
		Decider:   "human",
		Reason:    "login failed for ada with hunter2-secret",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "hunter2-secret") || strings.Contains(text, "ada") {
		t.Fatalf("secret leaked into journal: %s", text)
	}
	if !strings.Contains(text, "••••") {
		t.Fatalf("mask marker missing: %s", text)
	}
}

func TestRedactorMasksCredentialKeys(t *testing.T) {
	redactor := DefaultRedactor()
	out := redactor.Redact(map[string]any{
		"username": "ada",
		"password": "s3cret",
		"nested":   map[string]any{"api_key": "k-123", "keep": "visible"},
	})
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "k-123") {
		t.Fatalf("credential key leaked: %s", raw)
	}
	if !strings.Contains(string(raw), "visible") || !strings.Contains(string(raw), "ada") {
		t.Fatalf("non-secret data wrongly masked: %s", raw)
	}
}

func TestTailAndSessions(t *testing.T) {
	dir := t.TempDir()
	journal, err := New(Options{Dir: dir, Session: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := journal.Append(Entry{Command: "click"}); err != nil {
			t.Fatal(err)
		}
	}
	tail, err := journal.Tail(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 {
		t.Fatalf("tail = %d", len(tail))
	}
	other, err := New(Options{Dir: dir, Session: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Append(Entry{Command: "open"}); err != nil {
		t.Fatal(err)
	}
	sessions, err := Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0] != "alpha" || sessions[1] != "beta" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestCorruptLineBecomesTombstone(t *testing.T) {
	journal := newTestJournal(t, "default", nil)
	if err := os.WriteFile(journal.Path(), []byte("{\"command\":\"open\"}\nnot-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := journal.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Command != "<corrupt>" {
		t.Fatalf("entries = %#v", entries)
	}
}
