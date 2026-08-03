package chrome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cdproto "github.com/chromedp/cdproto"
	"github.com/danieljustus/symaira-corekit/fsutil"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// Uploads and downloads (issue #63): file inputs are fed only with paths
// inside the allowed directories (path guard via corekit/fsutil), downloads
// land in a configurable directory and every event carries origin URL, size
// and checksum.

const maxDownloadEvents = 500

// UploadFiles validates every file against the allowed directories and
// hands the resolved paths to the input element selected by selector.
// Deny-by-default: without allowed directories no upload is possible.
func (e *Engine) UploadFiles(ctx context.Context, page engine.Page, request engine.UploadRequest) (engine.UploadResult, error) {
	if strings.TrimSpace(request.Selector) == "" {
		return engine.UploadResult{}, errors.New("upload requires a selector")
	}
	if len(request.Files) == 0 {
		return engine.UploadResult{}, errors.New("upload requires at least one file")
	}
	guarded := make([]string, 0, len(request.Files))
	for _, file := range request.Files {
		resolved, err := guardUploadPath(file, request.AllowedDirs)
		if err != nil {
			return engine.UploadResult{}, err
		}
		guarded = append(guarded, resolved)
	}
	target, err := e.ResolveElement(ctx, page, request.Selector)
	if err != nil {
		return engine.UploadResult{}, fmt.Errorf("resolve upload target: %w", err)
	}
	if target.BackendNodeID == 0 {
		return engine.UploadResult{}, errors.New("upload target has no backend node id")
	}
	var result struct{}
	params := struct {
		Files         []string `json:"files"`
		BackendNodeID int64    `json:"backendNodeId"`
	}{guarded, target.BackendNodeID}
	if err := e.call(ctx, page.SessionID, cdproto.CommandDOMSetFileInputFiles, params, &result); err != nil {
		return engine.UploadResult{}, fmt.Errorf("set file input files: %w", err)
	}
	return engine.UploadResult{Uploaded: guarded}, nil
}

// guardUploadPath validates one upload path (issue #63 AC): traversal
// segments, non-existent files, symlinks escaping the allowed roots and
// paths outside every allowed directory are rejected.
func guardUploadPath(path string, allowedDirs []string) (string, error) {
	if path == "" {
		return "", errors.New("upload path is empty")
	}
	if fsutil.HasTraversal(path) {
		return "", fmt.Errorf("upload path rejected: traversal segment in %q", path)
	}
	if len(allowedDirs) == 0 {
		return "", errors.New("upload path rejected: no allowed upload directory is configured")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve upload path: %w", err)
	}
	resolved := absolute
	if evaluated, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
		resolved = evaluated
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("upload file does not exist: %s", path)
		}
		return "", fmt.Errorf("inspect upload file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("upload path is not a regular file: %s", path)
	}
	for _, dir := range allowedDirs {
		root, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		// Resolve the root itself so symlinked prefixes (e.g. /var ->
		// /private/var on macOS) do not break containment checks.
		if evaluated, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
			root = evaluated
		}
		if pathWithin(root, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("upload path outside allowed directories: %s", path)
}

// pathWithin reports whether path is inside dir (or equals it).
func pathWithin(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// SetDownloadBehavior points browser downloads at dir. Empty dir denies
// downloads. Events are enabled so download.list can report them.
func (e *Engine) SetDownloadBehavior(ctx context.Context, page engine.Page, config engine.DownloadConfig) error {
	behavior := "deny"
	downloadPath := ""
	if strings.TrimSpace(config.Dir) != "" {
		absolute, err := filepath.Abs(config.Dir)
		if err != nil {
			return fmt.Errorf("resolve download directory: %w", err)
		}
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return fmt.Errorf("create download directory: %w", err)
		}
		behavior = "allow"
		downloadPath = absolute
	}
	var result struct{}
	params := struct {
		Behavior      string `json:"behavior"`
		DownloadPath  string `json:"downloadPath,omitempty"`
		EventsEnabled bool   `json:"eventsEnabled"`
	}{behavior, downloadPath, true}
	if err := e.call(ctx, "", cdproto.CommandBrowserSetDownloadBehavior, params, &result); err != nil {
		return fmt.Errorf("set download behavior: %w", err)
	}
	e.downloadMu.Lock()
	if e.downloadDir == nil {
		e.downloadDir = make(map[string]string)
	}
	e.downloadDir[page.SessionID] = downloadPath
	e.downloadMu.Unlock()
	return nil
}

// DownloadEvents returns the session's download events; completed events
// carry the file checksum.
func (e *Engine) DownloadEvents(page engine.Page) []engine.DownloadEvent {
	e.downloadMu.Lock()
	defer e.downloadMu.Unlock()
	events := make([]engine.DownloadEvent, 0, len(e.downloadEvents[page.SessionID]))
	for _, event := range e.downloadEvents[page.SessionID] {
		if event.State == "completed" && event.SHA256 == "" {
			event.SHA256 = checksumOf(filepath.Join(e.downloadDir[page.SessionID], event.GUID))
		}
		events = append(events, event)
	}
	return events
}

func (e *Engine) recordDownloadWillBegin(sessionID string, params json.RawMessage) {
	var event struct {
		GUID              string `json:"guid"`
		URL               string `json:"url"`
		SuggestedFilename string `json:"suggestedFilename"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	entry := engine.DownloadEvent{
		GUID:      event.GUID,
		URL:       event.URL,
		Filename:  event.SuggestedFilename,
		State:     "inProgress",
		Timestamp: time.Now(),
	}
	e.downloadMu.Lock()
	if e.downloadEvents == nil {
		e.downloadEvents = make(map[string][]engine.DownloadEvent)
	}
	e.downloadEvents[sessionID] = appendBoundedDownloads(e.downloadEvents[sessionID], entry)
	e.downloadMu.Unlock()
}

func (e *Engine) recordDownloadProgress(sessionID string, params json.RawMessage) {
	var event struct {
		GUID          string `json:"guid"`
		State         string `json:"state"`
		ReceivedBytes int64  `json:"receivedBytes"`
		TotalBytes    int64  `json:"totalBytes"`
	}
	if err := json.Unmarshal(params, &event); err != nil {
		return
	}
	e.downloadMu.Lock()
	defer e.downloadMu.Unlock()
	events := e.downloadEvents[sessionID]
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].GUID == event.GUID {
			events[i].State = event.State
			events[i].ReceivedBytes = event.ReceivedBytes
			events[i].TotalBytes = event.TotalBytes
			return
		}
	}
}

func checksumOf(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func appendBoundedDownloads(events []engine.DownloadEvent, event engine.DownloadEvent) []engine.DownloadEvent {
	if len(events) >= maxDownloadEvents {
		events = append([]engine.DownloadEvent(nil), events[len(events)-maxDownloadEvents+1:]...)
	}
	return append(events, event)
}
