package mcp

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// TestCanonicalNameResolvesAliases is the alias-resolution contract (issue
// #2): an alias maps to its canonical tool id, canonical ids are stable,
// and unknown names pass through unchanged.
func TestCanonicalNameResolvesAliases(t *testing.T) {
	if got := CanonicalName("open"); got != "open" {
		t.Fatalf("CanonicalName(open) = %q", got)
	}
	if got := CanonicalName("goto"); got != "open" {
		t.Fatalf("CanonicalName(goto) = %q, want open", got)
	}
	if got := CanonicalName("definitely-not-a-tool"); got != "definitely-not-a-tool" {
		t.Fatalf("unknown name must pass through, got %q", got)
	}
}

// TestToolRegistryValidation enforces uniqueness: canonical ids are unique,
// aliases do not collide with canonical ids or other aliases, and no tool
// aliases itself. The production table must pass; the validator must reject
// broken tables.
func TestToolRegistryValidation(t *testing.T) {
	if err := validateToolRegistry(); err != nil {
		t.Fatalf("production tool table must validate: %v", err)
	}
	// Every canonical name is unique.
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool.Name] {
			t.Fatalf("duplicate canonical tool id %q in the table", tool.Name)
		}
		seen[tool.Name] = true
	}
	// Aliases do not collide with canonical names or each other.
	aliasOwners := map[string]string{}
	for _, tool := range tools {
		for _, alias := range tool.Aliases {
			if alias == tool.Name {
				t.Fatalf("%q aliases itself", tool.Name)
			}
			if seen[alias] {
				t.Fatalf("alias %q collides with a canonical tool", alias)
			}
			if owner, exists := aliasOwners[alias]; exists {
				t.Fatalf("alias %q declared by both %q and %q", alias, owner, tool.Name)
			}
			aliasOwners[alias] = tool.Name
		}
	}
}

// TestRiskClassOfEveryTool verifies the registry carries a policy risk
// class for every registered tool (issue #2): each tool's daemon command
// classifies to a valid risk class.
func TestRiskClassOfEveryTool(t *testing.T) {
	for _, tool := range tools {
		class := RiskClassOf(tool.Name)
		if !policy.ValidClass(policy.RiskClass(class)) {
			t.Fatalf("tool %q has invalid risk class %q", tool.Name, class)
		}
	}
	// The alias resolves to the canonical tool's class.
	if RiskClassOf("goto") != RiskClassOf("open") {
		t.Fatalf("alias goto risk class %q != canonical open %q", RiskClassOf("goto"), RiskClassOf("open"))
	}
	// Navigation tools classify as navigate, inspection tools as read.
	if RiskClassOf("open") != string(policy.ClassNavigate) {
		t.Fatalf("open risk class = %q, want navigate", RiskClassOf("open"))
	}
	if RiskClassOf("snapshot") != string(policy.ClassRead) {
		t.Fatalf("snapshot risk class = %q, want read", RiskClassOf("snapshot"))
	}
}

// TestProfileCountsStableWithAliases verifies profile membership counts the
// canonical tools only: adding aliases must not change --list-profiles
// output (issue #2 AC).
func TestProfileCountsStableWithAliases(t *testing.T) {
	for _, info := range AllProfiles() {
		for _, name := range info.Tools {
			if CanonicalName(name) != name {
				t.Fatalf("profile %q lists non-canonical name %q", info.Name, name)
			}
		}
	}
	core, err := ProfileInfoFor(ProfileCore)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range core.Tools {
		if name == "goto" {
			t.Fatal("profile listing must not contain the goto alias")
		}
	}
}

// TestAliasToolCallResolvesToCanonical verifies a tools/call on the alias
// executes the canonical daemon command (issue #2 AC: an alias resolves to
// its canonical id without registering the same tool twice).
func TestAliasToolCallResolvesToCanonical(t *testing.T) {
	base := socketBase(t)
	path, err := daemon.SocketPathIn(base, "test")
	if err != nil {
		t.Fatal(err)
	}
	var gotCommand string
	daemonServer := daemon.NewServer(daemon.Options{
		SocketPath:    path,
		Session:       "test",
		PeerValidator: func(net.Conn) error { return nil },
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			gotCommand = frame.Cmd
			return map[string]any{"action": "open", "url": "https://example.com/", "http_status": 200}, nil, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemonServer.ListenAndServe(ctx) }()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("capture daemon did not shut down")
		}
	})
	t.Cleanup(cancel)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.Dial("unix", path)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		select {
		case serveErr := <-done:
			t.Fatalf("capture daemon exited before ready: %v", serveErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}

	server := newTestServer(t, base)
	var openTool ProxyTool
	for _, tool := range tools {
		if tool.Name == "open" {
			openTool = tool
		}
	}
	alias := aliasTool(openTool, "goto")
	if !strings.Contains(alias.Description, `"open"`) || !strings.Contains(alias.Description, "use") {
		t.Fatalf("alias description lacks replacement guidance: %q", alias.Description)
	}
	if _, err := server.proxyTool(alias).Handler(context.Background(), json.RawMessage(`{"url": "https://example.com/"}`)); err != nil {
		t.Fatal(err)
	}
	if gotCommand != "open" {
		t.Fatalf("alias call dispatched daemon command %q, want open", gotCommand)
	}
}
