package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/doctor"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// stubPolicyReporter is a fixed engine.NetworkPolicyReporter for tests.
type stubPolicyReporter struct {
	blocked     []engine.BlockedRequest
	limitations []string
}

func (s *stubPolicyReporter) BlockedRequests() []engine.BlockedRequest { return s.blocked }
func (s *stubPolicyReporter) Limitations() []string                    { return s.limitations }

func TestNetworkPolicyWarningsReportsBlockedRequests(t *testing.T) {
	reporter := &stubPolicyReporter{blocked: []engine.BlockedRequest{
		{URL: "https://evil.example.org/pixel.png", ResourceType: "Image", Count: 2},
		{URL: "https://evil.example.org/data.json", ResourceType: "XHR", Count: 1},
	}}
	warnings := networkPolicyWarnings(reporter)
	if len(warnings) != 3 {
		t.Fatalf("warnings = %d, want 3 (summary + 2 URLs)", len(warnings))
	}
	if warnings[0].Kind != "network_policy" || !strings.Contains(warnings[0].Message, "3 request(s)") {
		t.Errorf("summary warning = %+v, want total of 3", warnings[0])
	}
	if warnings[1].Kind != "network_policy.blocked" || !strings.Contains(warnings[1].Message, "https://evil.example.org/pixel.png") || !strings.Contains(warnings[1].Message, "2 requests") {
		t.Errorf("per-URL warning = %+v", warnings[1])
	}
	if warnings[2].Kind != "network_policy.blocked" || !strings.Contains(warnings[2].Message, "data.json") {
		t.Errorf("per-URL warning = %+v", warnings[2])
	}
}

func TestNetworkPolicyWarningsCapsURLList(t *testing.T) {
	blocked := make([]engine.BlockedRequest, 0, 15)
	for i := 0; i < 15; i++ {
		blocked = append(blocked, engine.BlockedRequest{URL: "https://evil.example.org/" + string(rune('a'+i)), Count: 1})
	}
	warnings := networkPolicyWarnings(&stubPolicyReporter{blocked: blocked})
	if len(warnings) != 12 { // summary + 10 URLs + "and N more"
		t.Fatalf("warnings = %d, want 12 (summary + 10 URLs + remainder)", len(warnings))
	}
	last := warnings[len(warnings)-1]
	if !strings.Contains(last.Message, "5 more blocked URL(s)") {
		t.Errorf("remainder warning = %q, want 5 more", last.Message)
	}
}

func TestNetworkPolicyWarningsReportsLimitations(t *testing.T) {
	warnings := networkPolicyWarnings(&stubPolicyReporter{limitations: []string{"domain allowlist is not fully enforceable: reusing an existing Chrome profile"}})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1", len(warnings))
	}
	if warnings[0].Kind != "network_policy.limitation" || warnings[0].Severity != "warning" {
		t.Errorf("limitation warning = %+v", warnings[0])
	}
}

func TestNetworkPolicyWarningsEmptyWithoutPolicy(t *testing.T) {
	if warnings := networkPolicyWarnings(&stubPolicyReporter{}); len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none", warnings)
	}
}

// browserRuntimeForTest builds a NavigationRuntime with the Chrome engine and
// a real session registry so service() reaches the executable-resolution step.
func browserRuntimeForTest(t *testing.T) *NavigationRuntime {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{})
	if _, err := registry.Ensure("s"); err != nil {
		t.Fatalf("Ensure session: %v", err)
	}
	return &NavigationRuntime{
		registry:        registry,
		engineKind:      "chrome",
		engines:         make(map[string]engine.Engine),
		browserContexts: make(map[string]engine.Context),
		tabs:            make(map[string][]*sessionTab),
		activeTab:       make(map[string]int),
		recorders:       make(map[string]*recorderState),
	}
}

func stubResolver(path string, err error) func(func(string) string, func(string) (string, error)) (string, error) {
	return func(func(string) string, func(string) (string, error)) (string, error) {
		return path, err
	}
}

// TestServiceDiscoveryFailureIsActionable verifies that when no browser can
// be discovered the error names the missing configuration, the searched paths
// and the override escape hatch.
func TestServiceDiscoveryFailureIsActionable(t *testing.T) {
	orig := resolveBrowserExecutable
	resolveBrowserExecutable = stubResolver("", &doctor.DiscoveryError{SearchPaths: []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}})
	t.Cleanup(func() { resolveBrowserExecutable = orig })

	runtime := browserRuntimeForTest(t)
	_, err := runtime.service(context.Background(), "s")
	if err == nil {
		t.Fatal("expected an error when discovery finds no browser")
	}
	msg := err.Error()
	for _, want := range []string{"browser executable is not configured", "searched", "SYMBROWSE_EXECUTABLE_PATH"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, missing %q", msg, want)
		}
	}
}

// TestServiceFallbackDiscoveryUsed verifies that a discovered executable is
// actually used: the runtime proceeds to launch (and fails with a launch
// error, not the not-configured error).
func TestServiceFallbackDiscoveryUsed(t *testing.T) {
	orig := resolveBrowserExecutable
	resolveBrowserExecutable = stubResolver("/nonexistent/stub-chrome", nil)
	t.Cleanup(func() { resolveBrowserExecutable = orig })

	runtime := browserRuntimeForTest(t)
	_, err := runtime.service(context.Background(), "s")
	if err == nil {
		t.Fatal("expected a launch error for the stub executable")
	}
	if strings.Contains(err.Error(), "browser executable is not configured") {
		t.Errorf("discovery fallback did not engage: %v", err)
	}
}

// TestServiceExplicitExecutableSkipsDiscovery verifies SYMBROWSE_EXECUTABLE_PATH
// (or the constructed executable) takes precedence and discovery is never
// consulted.
func TestServiceExplicitExecutableSkipsDiscovery(t *testing.T) {
	called := false
	orig := resolveBrowserExecutable
	resolveBrowserExecutable = func(func(string) string, func(string) (string, error)) (string, error) {
		called = true
		return "", errors.New("must not be called")
	}
	t.Cleanup(func() { resolveBrowserExecutable = orig })

	runtime := browserRuntimeForTest(t)
	runtime.executable = "/nonexistent/stub-chrome"
	_, err := runtime.service(context.Background(), "s")
	if err == nil {
		t.Fatal("expected a launch error for the stub executable")
	}
	if called {
		t.Error("discovery was called although an executable was configured")
	}
}

func TestEngineInfoReportsPlannedAndActiveCapabilities(t *testing.T) {
	rt := NewNavigationRuntime(nil, "/usr/bin/chrome", NavigationRuntimeOptions{})
	data, err := rt.handleEngineInfoFrame(Frame{Session: "test"})
	if err != nil {
		t.Fatalf("handleEngineInfoFrame = %v", err)
	}
	caps, ok := data.(engine.Capabilities)
	if !ok {
		t.Fatalf("data = %#v, want engine.Capabilities", data)
	}
	if caps.Kind != "chrome" || caps.LaunchMode != "launch" || len(caps.Interfaces) != len(engine.OptionalInterfaceNames) {
		t.Fatalf("planned chrome caps = %+v, want kind=chrome launch=launch all interfaces", caps)
	}

	staticRT := NewNavigationRuntime(nil, "", NavigationRuntimeOptions{Engine: "static"})
	staticData, err := staticRT.handleEngineInfoFrame(Frame{Session: "test"})
	if err != nil {
		t.Fatalf("static handleEngineInfoFrame = %v", err)
	}
	staticCaps := staticData.(engine.Capabilities)
	if staticCaps.Kind != "static" || len(staticCaps.Interfaces) != 2 {
		t.Fatalf("static caps = %+v, want kind=static with 2 interfaces", staticCaps)
	}
}

// TestSafariAttachHonorsModeGuard verifies the #297 safety boundary: the
// safari-attach engine is read-only in MCP mode (no InteractionEngine) and only
// enables interactions in TTY mode, because it has no network layer for the
// SSRF guard to enforce.
func TestSafariAttachHonorsModeGuard(t *testing.T) {
	mcpRT := NewNavigationRuntime(nil, "", NavigationRuntimeOptions{Engine: "safari-attach", Mode: policy.ModeMCP})
	mcpData, err := mcpRT.handleEngineInfoFrame(Frame{Session: "test"})
	if err != nil {
		t.Fatalf("mcp handleEngineInfoFrame = %v", err)
	}
	mcpCaps := mcpData.(engine.Capabilities)
	if includes(mcpCaps.Interfaces, "InteractionEngine") {
		t.Fatalf("MCP mode must keep safari-attach read-only: %+v", mcpCaps.Interfaces)
	}

	ttyRT := NewNavigationRuntime(nil, "", NavigationRuntimeOptions{Engine: "safari-attach", Mode: policy.ModeTTY, SSRFEnabled: true})
	ttyData, err := ttyRT.handleEngineInfoFrame(Frame{Session: "test"})
	if err != nil {
		t.Fatalf("tty handleEngineInfoFrame = %v", err)
	}
	ttyCaps := ttyData.(engine.Capabilities)
	if !includes(ttyCaps.Interfaces, "InteractionEngine") {
		t.Fatalf("TTY mode must enable safari-attach InteractionEngine: %+v", ttyCaps.Interfaces)
	}
	if ttyCaps.LaunchMode != "attach" {
		t.Fatalf("safari-attach launch mode = %q, want attach", ttyCaps.LaunchMode)
	}
}

func includes(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
