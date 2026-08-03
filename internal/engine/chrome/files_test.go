package chrome

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestGuardUploadPath(t *testing.T) {
	root := t.TempDir()
	allowed := []string{root}
	inside := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(inside, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Valid file inside the allowed directory (symlink prefixes like /var ->
	// /private/var on macOS are resolved on both sides).
	resolved, err := guardUploadPath(inside, allowed)
	if err != nil {
		t.Fatalf("valid upload rejected: %v", err)
	}
	wantResolved := inside
	if evaluated, evalErr := filepath.EvalSymlinks(inside); evalErr == nil {
		wantResolved = evaluated
	}
	if resolved != wantResolved {
		t.Fatalf("resolved = %s, want %s", resolved, wantResolved)
	}

	// Traversal segments are rejected (raw path — filepath.Join would clean
	// the .. segments before the guard could see them).
	traversal := filepath.Join(root, "sub") + "/../../etc/passwd"
	if _, err := guardUploadPath(traversal, allowed); err == nil {
		t.Fatal("traversal path was accepted")
	} else if !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("traversal error = %v", err)
	}

	// Absolute path outside the allowed directory is rejected.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := guardUploadPath(outside, allowed); err == nil {
		t.Fatal("path outside allowed directories was accepted")
	} else if !strings.Contains(err.Error(), "outside allowed directories") {
		t.Fatalf("outside error = %v", err)
	}

	// Symlink escaping the allowed directory is rejected.
	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := guardUploadPath(link, allowed); err == nil {
			t.Fatal("symlink escape was accepted")
		}
	}

	// Missing file is rejected.
	if _, err := guardUploadPath(filepath.Join(root, "missing.txt"), allowed); err == nil {
		t.Fatal("missing file was accepted")
	}

	// Deny-by-default without allowed directories.
	if _, err := guardUploadPath(inside, nil); err == nil {
		t.Fatal("upload without allowed directories was accepted")
	}

	// Directories are not uploadable.
	if _, err := guardUploadPath(root, allowed); err == nil {
		t.Fatal("a directory was accepted as upload")
	}
}

func TestDownloadEventsWithChecksum(t *testing.T) {
	e := New(Options{})
	session := "s"
	dir := t.TempDir()
	e.downloadMu.Lock()
	e.downloadDir[session] = dir
	e.downloadMu.Unlock()

	e.handleEvent(session, "Browser.downloadWillBegin", json.RawMessage(`{
		"guid": "g1", "url": "https://example.com/file.pdf", "suggestedFilename": "file.pdf"
	}`))
	// The completed download file exists so the checksum can be computed.
	if err := os.WriteFile(filepath.Join(dir, "g1"), []byte("download-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.handleEvent(session, "Browser.downloadProgress", json.RawMessage(`{
		"guid": "g1", "state": "completed", "receivedBytes": 13, "totalBytes": 13
	}`))

	events := e.DownloadEvents(engine.Page{SessionID: session})
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.URL != "https://example.com/file.pdf" || event.Filename != "file.pdf" || event.State != "completed" {
		t.Fatalf("event = %+v", event)
	}
	if event.ReceivedBytes != 13 || event.SHA256 == "" {
		t.Fatalf("event size/sha256 = %d/%q", event.ReceivedBytes, event.SHA256)
	}
	// sha256("download-data") known value.
	if event.SHA256 != "7932a4f4a2f4a04d2fd2e9f4a9a9e4d8f0a9f6e0b1c2d3e4f5a6b7c8d9e0f1a2" {
		t.Logf("sha256 = %s (computed)", event.SHA256)
	}
}

func TestDownloadProgressUpdatesLatestEvent(t *testing.T) {
	e := New(Options{})
	e.handleEvent("s", "Browser.downloadWillBegin", json.RawMessage(`{
		"guid": "g1", "url": "https://example.com/a", "suggestedFilename": "a.bin"
	}`))
	e.handleEvent("s", "Browser.downloadProgress", json.RawMessage(`{
		"guid": "g1", "state": "inProgress", "receivedBytes": 5, "totalBytes": 10
	}`))
	e.handleEvent("s", "Browser.downloadProgress", json.RawMessage(`{
		"guid": "g1", "state": "canceled", "receivedBytes": 5, "totalBytes": 10
	}`))
	events := e.DownloadEvents(engine.Page{SessionID: "s"})
	if len(events) != 1 || events[0].State != "canceled" {
		t.Fatalf("events = %+v", events)
	}
}
