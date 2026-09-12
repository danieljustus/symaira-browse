package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

const portCredentialURL = "https://private-user:private-password@blocked.example/path?token=private-token&password=query-password&view=public"

type portPolicyEngine struct {
	*boundaryEngine
	blocked []engine.BlockedRequest
}

func (e *portPolicyEngine) NavigationState(context.Context, engine.Page) (engine.NavigationState, error) {
	return engine.NavigationState{URL: "https://example.com/path?view=public", ReadyState: "complete", HTTPStatus: 200, NetworkIdle: true}, nil
}
func (e *portPolicyEngine) Inspect(_ context.Context, _ engine.Page, request engine.InspectionRequest, _ *engine.InteractionTarget) (engine.InspectionResult, error) {
	return engine.InspectionResult{Kind: request.Kind, Value: json.RawMessage(`"fixture title"`)}, nil
}
func (e *portPolicyEngine) BlockedRequests() []engine.BlockedRequest { return e.blocked }
func (e *portPolicyEngine) Limitations() []string                    { return []string{"fixture enforcement limitation"} }

// TestNavigationAdmissionSequencePort freezes production Handle observations,
// with an injected engine. Admission denials must not alter engine history.
func TestNavigationAdmissionSequencePort(t *testing.T) {
	rt, fake := newBoundaryRuntime(t, "chrome", NavigationRuntimeOptions{AllowedDomains: []string{"example.com"}})
	defer func() { _ = rt.Close() }()
	reporter := &portPolicyEngine{boundaryEngine: fake}
	rt.engines["boundary"] = reporter
	rt.tabs["boundary"][0].Service = engine.NewNavigationService(reporter, engine.Page{ID: "page"}, engine.NavigationOptions{})
	var steps []map[string]any
	for _, command := range []string{"open", "goto", "get.title"} {
		target := "https://example.com/path?view=public"
		if command == "goto" {
			target = "https://blocked.example/path?view=public"
		}
		_, warnings, err := rt.Handle(context.Background(), Frame{Cmd: command, Session: "boundary", Args: mustArgs(t, map[string]any{"url": target})})
		if (err == nil) != (command != "goto") {
			t.Fatalf("%s unexpected success/failure: %v", command, err)
		}
		step := map[string]any{"command": command, "success": err == nil, "engine_calls": len(fake.navigateURLs), "warnings": warnings}
		if err != nil {
			step["error"] = err.Error()
		}
		steps = append(steps, step)
	}
	if len(fake.navigateURLs) != 1 {
		t.Fatalf("navigation calls: %v", fake.navigateURLs)
	}
	// Named known defect: Go warning rendering emits credential-bearing engine
	// history verbatim. Freeze it; do not alter the executable oracle silently.
	reporter.blocked = []engine.BlockedRequest{{URL: portCredentialURL, ResourceType: "Document", Count: 2}}
	_, warnings, err := rt.Handle(context.Background(), Frame{Cmd: "get.title", Session: "boundary", Args: mustArgs(t, map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(warnings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "private-password") || !strings.Contains(string(raw), "private-token") {
		t.Fatalf("known defect changed: %s", raw)
	}
	sources := map[string]string{}
	for _, name := range []string{"internal/daemon/navigation.go", "internal/daemon/navigation_frames.go", "internal/daemon/inspect_frames.go", "internal/daemon/protocol.go", "internal/engine/navigation.go", "internal/policy/allowlist.go"} {
		source, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = fmt.Sprintf("%x", sha256.Sum256(source))
	}
	fixture := map[string]any{"oracle": map[string]any{"commit": "e86c1db46ad758d89372640473a5311525e3edf1", "source_sha256": sources}, "safe_sequence": steps, "GO_KNOWN_DEFECT_442_CREDENTIAL_POLICY_WARNING": warnings}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "testdata", "port", "daemon", "navigation-security.json")
	if os.Getenv("SYMBROWSE_PORT_FIXTURE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, encoded) {
		t.Fatal("navigation security fixture drift")
	}
}
