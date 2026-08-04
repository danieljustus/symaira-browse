package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// fakeNetworkEngine implements NetworkEvents and FileTransfer so the
// network.* and upload/download daemon handlers can be exercised without
// Chrome. It embeds a full fake engine for the required Engine methods.
type fakeNetworkEngine struct {
	fakeCookieEngine
	requests    []engine.NetworkRequest
	har         []byte
	routed      []engine.NetworkRoute
	unrouted    string
	uploaded    []string
	downloads   []engine.DownloadEvent
	uploadErr   error
	downloadErr error
}

func (f *fakeNetworkEngine) EnableNetworkCapture(context.Context, engine.Page) error { return nil }
func (f *fakeNetworkEngine) Requests(engine.Page) []engine.NetworkRequest            { return f.requests }
func (f *fakeNetworkEngine) Request(_ engine.Page, id string) (engine.NetworkRequest, bool) {
	for _, request := range f.requests {
		if request.ID == id {
			return request, true
		}
	}
	return engine.NetworkRequest{}, false
}
func (f *fakeNetworkEngine) RouteRequests(_ context.Context, _ engine.Page, route engine.NetworkRoute) error {
	f.routed = append(f.routed, route)
	return nil
}
func (f *fakeNetworkEngine) UnrouteRequests(_ context.Context, _ engine.Page, pattern string) (bool, error) {
	f.unrouted = pattern
	return true, nil
}
func (f *fakeNetworkEngine) HAR(context.Context, engine.Page, engine.HAROptions) ([]byte, error) {
	return f.har, nil
}
func (f *fakeNetworkEngine) UploadFiles(_ context.Context, _ engine.Page, request engine.UploadRequest) (engine.UploadResult, error) {
	if f.uploadErr != nil {
		return engine.UploadResult{}, f.uploadErr
	}
	f.uploaded = request.Files
	return engine.UploadResult{Uploaded: request.Files}, nil
}
func (f *fakeNetworkEngine) DownloadEvents(engine.Page) []engine.DownloadEvent { return f.downloads }
func (f *fakeNetworkEngine) SetDownloadBehavior(context.Context, engine.Page, engine.DownloadConfig) error {
	return f.downloadErr
}

func newNetworkRuntime(t *testing.T, fake *fakeNetworkEngine) *NavigationRuntime {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{UserDataRoot: t.TempDir()})
	if _, err := registry.Ensure("net"); err != nil {
		t.Fatal(err)
	}
	runtime := &NavigationRuntime{
		registry:        registry,
		executable:      "/fake/chrome",
		engines:         map[string]engine.Engine{"net": fake},
		browserContexts: map[string]engine.Context{"net": {ID: "ctx"}},
		tabs:            make(map[string][]*sessionTab),
		activeTab:       make(map[string]int),
		recorders:       make(map[string]*recorderState),
		uploadDirs:      []string{"/tmp/allowed"},
		engineKind:      "chrome",
	}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	runtime.tabs["net"] = []*sessionTab{{Label: "t1", Service: service, Page: engine.Page{ID: "page"}}}
	runtime.activeTab["net"] = 0
	return runtime
}

func TestNetworkReadFrames(t *testing.T) {
	fake := &fakeNetworkEngine{requests: []engine.NetworkRequest{{ID: "r1", URL: "https://example.com/x", Method: "GET"}}}
	runtime := newNetworkRuntime(t, fake)

	raw, _ := json.Marshal(map[string]any{})
	data, _, err := runtime.Handle(context.Background(), Frame{Cmd: "network.requests", Session: "net", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	payload := data.(map[string]any)
	if payload["count"] != 1 {
		t.Fatalf("payload = %+v", payload)
	}

	oneRaw, _ := json.Marshal(map[string]any{"id": "r1"})
	data, _, err = runtime.Handle(context.Background(), Frame{Cmd: "network.request", Session: "net", Args: oneRaw})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["request"].(engine.NetworkRequest).ID != "r1" {
		t.Fatalf("request = %+v", data)
	}

	missingRaw, _ := json.Marshal(map[string]any{"id": "nope"})
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "network.request", Session: "net", Args: missingRaw}); err == nil {
		t.Fatal("expected not-found error")
	}
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "network.bogus", Session: "net", Args: raw}); err == nil {
		t.Fatal("expected unknown network read command")
	}
}

func TestNetworkControlFrames(t *testing.T) {
	fake := &fakeNetworkEngine{har: []byte(`{"log":{}}`), requests: []engine.NetworkRequest{{ID: "r1", URL: "https://example.com/x", Method: "GET"}}}
	runtime := newNetworkRuntime(t, fake)

	routeRaw, _ := json.Marshal(map[string]any{"pattern": "https://example.com/*", "action": "mock"})
	data, _, err := runtime.Handle(context.Background(), Frame{Cmd: "network.route", Session: "net", Args: routeRaw})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["routed"] != "https://example.com/*" {
		t.Fatalf("route payload = %+v", data)
	}
	if len(fake.routed) != 1 {
		t.Fatalf("routed = %+v", fake.routed)
	}

	unrouteRaw, _ := json.Marshal(map[string]any{"pattern": "https://example.com/*"})
	data, _, err = runtime.Handle(context.Background(), Frame{Cmd: "network.unroute", Session: "net", Args: unrouteRaw})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["removed"] != true {
		t.Fatalf("unroute payload = %+v", data)
	}

	harStart, _ := json.Marshal(map[string]any{"action": "start"})
	data, _, err = runtime.Handle(context.Background(), Frame{Cmd: "network.har", Session: "net", Args: harStart})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["started"] != true {
		t.Fatalf("har start payload = %+v", data)
	}

	harStop, _ := json.Marshal(map[string]any{"action": "stop", "content": "all"})
	data, _, err = runtime.Handle(context.Background(), Frame{Cmd: "network.har", Session: "net", Args: harStop})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["entries"] != 1 {
		t.Fatalf("har stop payload = %+v", data)
	}

	harBad, _ := json.Marshal(map[string]any{"action": "sideways"})
	if _, _, err = runtime.Handle(context.Background(), Frame{Cmd: "network.har", Session: "net", Args: harBad}); err == nil {
		t.Fatal("expected invalid-har-action error")
	}
}

func TestUploadDownloadFrames(t *testing.T) {
	fake := &fakeNetworkEngine{downloads: []engine.DownloadEvent{{URL: "https://example.com/f", Filename: "f.bin", State: "completed"}}}
	runtime := newNetworkRuntime(t, fake)

	uploadRaw, _ := json.Marshal(map[string]any{"files": []string{"/tmp/allowed/a.txt"}})
	data, _, err := runtime.Handle(context.Background(), Frame{Cmd: "upload", Session: "net", Args: uploadRaw})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.(map[string]any)["uploaded"].([]string)) != 1 {
		t.Fatalf("upload payload = %+v", data)
	}

	dlRaw, _ := json.Marshal(map[string]any{})
	data, _, err = runtime.Handle(context.Background(), Frame{Cmd: "downloads.list", Session: "net", Args: dlRaw})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.(map[string]any)["downloads"].([]engine.DownloadEvent)) != 1 {
		t.Fatalf("downloads payload = %+v", data)
	}

	setDirRaw, _ := json.Marshal(map[string]any{"dir": "/tmp/dl"})
	data, _, err = runtime.Handle(context.Background(), Frame{Cmd: "download.setdir", Session: "net", Args: setDirRaw})
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["download_dir"] != "/tmp/dl" {
		t.Fatalf("setdir payload = %+v", data)
	}
}

func TestNetworkHandlersWithoutCapability(t *testing.T) {
	// A plain cookie engine has no NetworkEvents / FileTransfer.
	runtime, _ := newCookieRuntime(t)
	raw, _ := json.Marshal(map[string]any{})
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "network.requests", Session: "default", Args: raw}); err == nil {
		t.Fatal("expected network-capability error")
	}
	uploadRaw, _ := json.Marshal(map[string]any{"files": []string{"/tmp/a"}})
	if _, _, err := runtime.Handle(context.Background(), Frame{Cmd: "upload", Session: "default", Args: uploadRaw}); err == nil {
		t.Fatal("expected upload-capability error")
	}
}
