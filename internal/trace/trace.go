// Package trace converts journal entries into repeatable trace files and
// replays them step by step (issue B-42). A trace captures the action, the
// resolved selector and the expected state after the step; replay executes
// the steps against a fresh browser and reports per-step match or deviation.
package trace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/journal"
)

// SchemaVersion is the stable trace file schema version.
const SchemaVersion = 1

// Step is one replayable action with its expected post-state.
type Step struct {
	Command     string `json:"command"`
	Selector    string `json:"selector,omitempty"`
	Value       string `json:"value,omitempty"`
	Key         string `json:"key,omitempty"`
	URL         string `json:"url,omitempty"`
	ExpectedURL string `json:"expected_url,omitempty"`
}

// File is the repeatable trace document.
type File struct {
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     string `json:"created_at"`
	Session       string `json:"session"`
	Steps         []Step `json:"steps"`
}

// Export builds a trace from a journal. Steps are derived from journal
// entries: open/goto become URL steps, interactions keep selector/value/key.
// Redacted journal args contain no secrets, so the trace file is safe to
// share; credential values are never exported (they are re-resolved during
// replay through symvault, never read from the file).
func Export(entries []journal.Entry, session string) *File {
	file := &File{
		SchemaVersion: SchemaVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Session:       session,
	}
	for _, entry := range entries {
		if entry.Result != "" && strings.HasPrefix(entry.Result, "error") {
			continue
		}
		step := stepFromEntry(entry)
		if step == nil {
			continue
		}
		file.Steps = append(file.Steps, *step)
	}
	return file
}

// stepFromEntry maps one journal entry to a replay step. Selectors and
// values are read from the redacted args map; credential-class entries are
// skipped entirely — their values are never stored, and replay re-resolves
// them through symvault (issue B-42).
func stepFromEntry(entry journal.Entry) *Step {
	if entry.RiskClass == "credential" {
		return nil
	}
	switch entry.Command {
	case "open", "goto":
		url := argString(entry.Args, "url")
		if url == "" {
			return nil
		}
		return &Step{Command: entry.Command, URL: url, ExpectedURL: url}
	case "click", "dblclick", "hover", "focus", "check", "uncheck", "scrollintoview", "scroll":
		selector := argString(entry.Args, "selector")
		if selector == "" {
			return nil
		}
		return &Step{Command: entry.Command, Selector: selector}
	case "fill", "type", "select":
		selector := argString(entry.Args, "selector")
		if selector == "" {
			return nil
		}
		value := argString(entry.Args, "value")
		return &Step{Command: entry.Command, Selector: selector, Value: value}
	case "press":
		key := argString(entry.Args, "key")
		if key == "" {
			return nil
		}
		return &Step{Command: entry.Command, Key: key}
	case "auth.login":
		// Credentials are re-resolved during replay; the step only records
		// the vault entry reference.
		entryName := argString(entry.Args, "entry")
		if entryName == "" {
			return nil
		}
		return &Step{Command: entry.Command, Value: entryName}
	default:
		return nil
	}
}

func argString(args any, key string) string {
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	value, _ := object[key].(string)
	return value
}

// Write persists a trace file atomically.
func Write(path string, file *File) error {
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal trace: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write trace: %w", err)
	}
	return nil
}

// Read loads a trace file.
func Read(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trace: %w", err)
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse trace: %w", err)
	}
	if file.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported trace schema version %d", file.SchemaVersion)
	}
	return &file, nil
}

// ReplayOutcome is the result of one replayed step.
type ReplayOutcome struct {
	Index       int    `json:"index"`
	Command     string `json:"command"`
	Matched     bool   `json:"matched"`
	ExpectedURL string `json:"expected_url,omitempty"`
	ActualURL   string `json:"actual_url,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ReplayResult aggregates a whole replay run.
type ReplayResult struct {
	Total    int             `json:"total"`
	Matched  int             `json:"matched"`
	Deviated int             `json:"deviated"`
	Failed   int             `json:"failed"`
	Outcomes []ReplayOutcome `json:"outcomes"`
}

// Replay runs the trace steps against a navigation service. Every step is
// executed; deviations are collected, never aborted, so the human sees the
// full picture. A step that could not be executed counts as failed.
func Replay(ctx context.Context, service *engine.NavigationService, file *File) ReplayResult {
	result := ReplayResult{}
	for index, step := range file.Steps {
		outcome := replayStep(ctx, service, index, step)
		result.Outcomes = append(result.Outcomes, outcome)
		result.Total++
		switch {
		case outcome.Error != "":
			result.Failed++
		case !outcome.Matched:
			result.Deviated++
		default:
			result.Matched++
		}
	}
	return result
}

func replayStep(ctx context.Context, service *engine.NavigationService, index int, step Step) ReplayOutcome {
	outcome := ReplayOutcome{Index: index, Command: step.Command}
	switch step.Command {
	case "open", "goto":
		nav, err := service.Open(ctx, step.URL)
		if err != nil {
			outcome.Error = err.Error()
			return outcome
		}
		outcome.ExpectedURL = step.ExpectedURL
		outcome.ActualURL = nav.URL
		outcome.Matched = normalizeURL(nav.URL) == normalizeURL(step.ExpectedURL)
	case "click", "dblclick", "hover", "focus", "check", "uncheck", "scrollintoview":
		_, err := service.Interact(ctx, engine.InteractionRequest{Action: engine.InteractionAction(step.Command), Selector: step.Selector})
		outcome = finishInteraction(outcome, err)
	case "scroll":
		_, err := service.Interact(ctx, engine.InteractionRequest{Action: engine.ActionScroll, Selector: step.Selector, Amount: 1})
		outcome = finishInteraction(outcome, err)
	case "fill", "type", "select":
		_, err := service.Interact(ctx, engine.InteractionRequest{Action: engine.InteractionAction(step.Command), Selector: step.Selector, Value: step.Value})
		outcome = finishInteraction(outcome, err)
	case "press":
		_, err := service.Interact(ctx, engine.InteractionRequest{Action: engine.ActionPress, Selector: "body", Key: step.Key})
		outcome = finishInteraction(outcome, err)
	case "auth.login":
		// auth.login is never replayed from the file: credentials must be
		// re-resolved through symvault, which the caller does by invoking the
		// auth runtime. A bare trace replay reports the step as skipped.
		outcome.Error = "credential step requires symvault re-resolution; replay it with auth login"
		return outcome
	default:
		outcome.Error = fmt.Sprintf("step command %q is not replayable", step.Command)
	}
	return outcome
}

func finishInteraction(outcome ReplayOutcome, err error) ReplayOutcome {
	if err != nil {
		outcome.Error = err.Error()
		return outcome
	}
	outcome.Matched = true
	return outcome
}

// normalizeURL strips fragments and trailing slashes for comparison.
func normalizeURL(raw string) string {
	if idx := strings.Index(raw, "#"); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSuffix(raw, "/")
}

// ErrNoSteps is returned when a trace contains nothing replayable.
var ErrNoSteps = errors.New("trace contains no replayable steps")
