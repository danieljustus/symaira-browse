package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/trace"
)

// JournalRuntime wraps the navigation runtime and appends one journal entry
// per action frame (issue B-41). Entry payloads are redacted by the journal
// itself before hitting the disk.
type JournalRuntime struct {
	journal *journal.Journal
	nav     *NavigationRuntime
}

// NewJournalRuntime creates a journaling wrapper. When journal is nil the
// wrapper passes through without logging (tests, disabled config).
func NewJournalRuntime(j *journal.Journal, nav *NavigationRuntime) *JournalRuntime {
	return &JournalRuntime{journal: j, nav: nav}
}

// Handle runs the frame and journals it. The journal entry is written after
// the action completes so the result is accurate; a failed frame still gets
// an entry with result "error:<kind>".
func (r *JournalRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	if r.journal == nil {
		return r.nav.Handle(ctx, frame)
	}
	started := time.Now()
	urlBefore := r.currentURL(ctx, frame.Session)
	data, warnings, err := r.nav.Handle(ctx, frame)
	duration := time.Since(started).Milliseconds()

	entry := journal.Entry{
		Session:    frame.Session,
		Command:    frame.Cmd,
		Args:       frame.Args,
		URLBefore:  urlBefore,
		RiskClass:  riskClassOf(frame.Cmd),
		Decider:    "policy",
		DurationMS: duration,
	}
	if err != nil {
		entry.Result = "error:" + errorKind(err)
	} else {
		entry.Result = "ok"
	}
	urlAfter := r.currentURL(ctx, frame.Session)
	if urlAfter != "" {
		entry.URLAfter = urlAfter
	}
	if _, appendErr := r.journal.Append(entry); appendErr != nil {
		// Journaling must never break the action itself; surface as a warning.
		warnings = append(warnings, Warning{Kind: "journal", Severity: "warning", Message: fmt.Sprintf("journal append failed: %v", appendErr)})
	}
	return data, warnings, err
}

// currentURL reads the page origin without starting a browser session.
func (r *JournalRuntime) currentURL(ctx context.Context, session string) string {
	r.nav.mu.Lock()
	service := r.nav.services[session]
	r.nav.mu.Unlock()
	if service == nil {
		return ""
	}
	origin, err := service.Origin(ctx)
	if err != nil {
		return ""
	}
	return origin
}

func errorKind(err error) string {
	var protocolErr *Error
	if errors.As(err, &protocolErr) && protocolErr.Code != "" {
		return protocolErr.Code
	}
	return "failed"
}

// HandleJournal executes journal inspection frames: tail and show.
func (r *JournalRuntime) HandleJournal(ctx context.Context, frame Frame) (any, []Warning, error) {
	if r.journal == nil {
		return nil, nil, errors.New("journal is not enabled")
	}
	switch frame.Cmd {
	case "journal.tail":
		var request struct {
			Session string `json:"session,omitempty"`
			Lines   int    `json:"lines,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		j := r.journal
		if request.Session != "" && request.Session != j.Session() {
			reopened, err := journal.New(journal.Options{Dir: journalDir(j), Session: request.Session})
			if err != nil {
				return nil, nil, err
			}
			j = reopened
		}
		entries, err := j.Tail(request.Lines)
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"schema_version": journal.SchemaVersion, "session": j.Session(), "entries": entries}, nil, nil
	case "journal.show":
		var request struct {
			Session string `json:"session,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		j := r.journal
		if request.Session != "" && request.Session != j.Session() {
			reopened, err := journal.New(journal.Options{Dir: journalDir(j), Session: request.Session})
			if err != nil {
				return nil, nil, err
			}
			j = reopened
		}
		entries, err := j.Read()
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"schema_version": journal.SchemaVersion, "session": j.Session(), "entries": entries}, nil, nil
	case "trace.replay":
		var request struct {
			Steps []trace.Step `json:"steps"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if len(request.Steps) == 0 {
			return nil, nil, errors.New("trace contains no replayable steps")
		}
		service, err := r.nav.service(ctx, frame.Session)
		if err != nil {
			return nil, nil, err
		}
		file := &trace.File{SchemaVersion: trace.SchemaVersion, Session: frame.Session, Steps: request.Steps}
		result := trace.Replay(ctx, service, file)
		return result, nil, nil
	default:
		return nil, nil, errors.New("unknown journal command")
	}
}

// journalDir extracts the directory of a journal for reopening other sessions.
func journalDir(j *journal.Journal) string {
	path := j.Path()
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
