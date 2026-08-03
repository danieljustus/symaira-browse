package daemon

import (
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
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
