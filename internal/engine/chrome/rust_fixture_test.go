package chrome

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/gorilla/websocket"
)

// This test-only generator exercises the unexported production guards and
// event/checksum handlers. It is intentionally not part of the production
// package API; scripts/rust-port/file_guard_fixture_gen.py invokes it to make
// the committed Rust fixture from the pinned Go oracle.
type rustFileFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	Upload        []rustUploadCase    `json:"upload"`
	Download      rustDownloadFixture `json:"download"`
}

type rustUploadCase struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Accepted  bool   `json:"accepted"`
	Resolved  string `json:"resolved,omitempty"`
	Error     string `json:"error,omitempty"`
	Supported bool   `json:"supported"`
}

type rustDownloadFixture struct {
	Deny      rustDownloadBehavior `json:"deny"`
	Allow     rustDownloadBehavior `json:"allow"`
	Traversal rustDownloadBehavior `json:"traversal"`
	Event     engineDownloadEvent  `json:"event"`
	Collision rustCollisionCase    `json:"collision"`
}

type rustDownloadBehavior struct {
	Behavior      string `json:"behavior"`
	DownloadPath  string `json:"download_path,omitempty"`
	EventsEnabled bool   `json:"events_enabled"`
}

type engineDownloadEvent struct {
	GUID          string `json:"guid"`
	URL           string `json:"url"`
	Filename      string `json:"filename"`
	State         string `json:"state"`
	ReceivedBytes int64  `json:"received_bytes"`
	TotalBytes    int64  `json:"total_bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	Timestamp     string `json:"timestamp"`
}

type rustCollisionCase struct {
	SuggestedFilename string `json:"suggested_filename"`
	ExistingPayload   string `json:"existing_payload"`
	GUIDPayload       string `json:"guid_payload"`
	ChecksumSource    string `json:"checksum_source"`
	CollisionHandling string `json:"collision_handling"`
}

type rustCDPBehaviorParams struct {
	Behavior      string `json:"behavior"`
	DownloadPath  string `json:"downloadPath"`
	EventsEnabled bool   `json:"eventsEnabled"`
}

func TestGenerateRustPortFileFixture(t *testing.T) {
	output := os.Getenv("RUST_PORT_FILE_FIXTURE_OUT")
	if output == "" {
		t.Skip("set RUST_PORT_FILE_FIXTURE_OUT to generate the Rust file-guard fixture")
	}
	fixture := buildRustPortFileFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(output, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildRustPortFileFixture(t *testing.T) rustFileFixture {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	allowed := filepath.Join(root, "allowed")
	if err := os.MkdirAll(allowed, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(allowed, "report.pdf")
	if err := os.WriteFile(inside, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := make([]rustUploadCase, 0, 8)
	cases = append(cases,
		makeUploadCase(t, "valid_relative", "allowed/report.pdf", []string{"allowed"}, root, outside),
		makeUploadCase(t, "traversal", "allowed/sub/../../etc/passwd", []string{"allowed"}, root, outside),
		makeUploadCase(t, "outside", outsideFile, []string{"allowed"}, root, outside),
		makeUploadCase(t, "missing", "allowed/missing.txt", []string{"allowed"}, root, outside),
		makeUploadCase(t, "no_allowed_directories", "allowed/report.pdf", nil, root, outside),
		makeUploadCase(t, "directory", "allowed", []string{"allowed"}, root, outside),
		makeUploadCase(t, "empty", "", []string{"allowed"}, root, outside),
	)
	link := filepath.Join(allowed, "escape-link")
	linkSupported := os.Symlink(outsideFile, link) == nil
	if linkSupported {
		cases = append(cases, makeUploadCase(t, "symlink_escape", "allowed/escape-link", []string{"allowed"}, root, outside))
	} else {
		cases = append(cases, rustUploadCase{Name: "symlink_escape", Path: "allowed/escape-link", Supported: false})
	}

	downloadRoot := filepath.Join(root, "downloads")
	deny, allow, traversal := captureRustDownloadBehaviors(t, downloadRoot)
	if err := os.MkdirAll(downloadRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	downloadFile := filepath.Join(downloadRoot, "g1")
	if err := os.WriteFile(downloadFile, []byte("download-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadRoot, "file.pdf"), []byte("old-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := New(Options{})
	e.downloadDir["fixture"] = downloadRoot
	e.recordDownloadWillBegin("fixture", json.RawMessage(`{"guid":"g1","url":"https://example.test/file.pdf","suggestedFilename":"file.pdf"}`))
	e.recordDownloadProgress("fixture", json.RawMessage(`{"guid":"g1","state":"completed","receivedBytes":13,"totalBytes":13}`))
	event := e.DownloadEvents(engine.Page{SessionID: "fixture"})
	if len(event) != 1 {
		t.Fatalf("generated %d download events, want 1", len(event))
	}
	fixtureEvent := engineDownloadEvent{
		GUID: event[0].GUID, URL: event[0].URL, Filename: event[0].Filename,
		State: event[0].State, ReceivedBytes: event[0].ReceivedBytes,
		TotalBytes: event[0].TotalBytes, SHA256: event[0].SHA256, Timestamp: "<TIMESTAMP>",
	}
	return rustFileFixture{
		SchemaVersion: 1,
		Upload:        cases,
		Download: rustDownloadFixture{
			Deny:      deny,
			Allow:     allow,
			Traversal: traversal,
			Event:     fixtureEvent,
			Collision: rustCollisionCase{
				SuggestedFilename: "file.pdf",
				ExistingPayload:   "old-file",
				GUIDPayload:       "download-data",
				ChecksumSource:    "<ROOT>/downloads/g1",
				CollisionHandling: "none",
			},
		},
	}
}

func makeUploadCase(t *testing.T, name, path string, allowed []string, root, outside string) rustUploadCase {
	t.Helper()
	resolved, err := guardUploadPath(path, allowed)
	result := rustUploadCase{Name: name, Path: normalizeFileFixturePath(path, root, outside), Supported: true}
	if err == nil {
		result.Accepted = true
		result.Resolved = normalizeFileFixturePath(resolved, root, outside)
	} else {
		result.Error = normalizeFileFixtureText(err.Error(), root, outside)
	}
	return result
}

func normalizeFileFixturePath(path string, root, outside string) string {
	value := filepath.ToSlash(path)
	roots := []string{}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		roots = append(roots, filepath.ToSlash(resolved))
	}
	roots = append(roots, filepath.ToSlash(root))
	for _, candidate := range roots {
		value = strings.ReplaceAll(value, candidate, "<ROOT>")
	}
	if outside != "" {
		value = strings.ReplaceAll(value, filepath.ToSlash(outside), "<OUTSIDE>")
		if resolved, err := filepath.EvalSymlinks(outside); err == nil {
			value = strings.ReplaceAll(value, filepath.ToSlash(resolved), "<OUTSIDE>")
		}
	}
	return value
}

func normalizeFileFixtureText(value, root, outside string) string {
	value = normalizeFileFixturePath(value, root, outside)
	return value
}

func captureRustDownloadBehaviors(t *testing.T, downloadRoot string) (rustDownloadBehavior, rustDownloadBehavior, rustDownloadBehavior) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	requests := make(chan rustCDPBehaviorParams, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		for {
			var message struct {
				ID     int64                 `json:"id"`
				Params rustCDPBehaviorParams `json:"params"`
			}
			if err := connection.ReadJSON(&message); err != nil {
				return
			}
			requests <- message.Params
			_ = connection.WriteJSON(map[string]any{"id": message.ID, "result": map[string]any{}})
		}
	}))
	t.Cleanup(server.Close)
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	e := New(Options{CDPEndpoint: endpoint, StartupTimeout: time.Second, RequestTimeout: time.Second})
	if err := e.Launch(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	page := engine.Page{SessionID: "fixture"}
	if err := e.SetDownloadBehavior(context.Background(), page, engine.DownloadConfig{}); err != nil {
		t.Fatal(err)
	}
	denyParams := <-requests
	if err := e.SetDownloadBehavior(context.Background(), page, engine.DownloadConfig{Dir: downloadRoot}); err != nil {
		t.Fatal(err)
	}
	allowParams := <-requests
	if err := e.SetDownloadBehavior(context.Background(), page, engine.DownloadConfig{Dir: "downloads/../normalized"}); err != nil {
		t.Fatal(err)
	}
	traversalParams := <-requests
	toFixture := func(params rustCDPBehaviorParams) rustDownloadBehavior {
		return rustDownloadBehavior{
			Behavior: params.Behavior, DownloadPath: normalizeFileFixturePath(params.DownloadPath, filepath.Dir(downloadRoot), ""), EventsEnabled: params.EventsEnabled,
		}
	}
	return toFixture(denyParams), toFixture(allowParams), toFixture(traversalParams)
}
