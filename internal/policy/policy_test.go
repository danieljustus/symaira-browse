package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryRegisteredCommandHasAClass is the B-43 acceptance test: the
// classification table must cover every command the daemon dispatches.
func TestEveryRegisteredCommandHasAClass(t *testing.T) {
	registered := []string{
		"open", "goto", "back", "forward", "reload", "wait", "snapshot",
		"screenshot", "a11y", "read",
		"click", "dblclick", "fill", "type", "press", "hover", "focus",
		"select", "check", "uncheck", "scroll", "scrollintoview",
		"get.text", "get.html", "get.value", "get.attr", "get.title",
		"get.url", "get.count", "get.box", "get.styles",
		"is.visible", "is.enabled", "is.checked", "find",
		"cookies.list", "cookies.set", "cookies.clear",
		"storage.list", "storage.set", "storage.clear",
		"state.save", "state.load", "state.list", "state.show", "state.clear", "state.clean",
		"auth.login",
		"set.viewport", "set.device", "set.geo", "set.offline", "set.headers", "set.media", "set.user-agent",
		"journal.tail", "journal.show", "policy.explain", "oob.status",
		"trace.replay", "watch", "eval", "submit", "download", "upload", "network.route",
		"network.unroute", "network.requests", "network.request", "network.har",
		"downloads.list", "download.setdir",
		"console.list", "console.clear", "errors.list", "errors.clear",
		"session.list", "session.info", "daemon.status",
	}
	for _, command := range registered {
		class, err := Classify(command)
		if err != nil {
			t.Fatalf("command %q missing classification: %v", command, err)
		}
		if !ValidClass(class) {
			t.Fatalf("command %q classified as unknown class %q", command, class)
		}
	}
}

func TestDefaultsMCPvsTTY(t *testing.T) {
	tests := []struct {
		class RiskClass
		mode  Mode
		want  Decision
	}{
		{ClassRead, ModeMCP, Allow},
		{ClassNavigate, ModeMCP, Allow},
		{ClassInteract, ModeMCP, Allow},
		{ClassSubmit, ModeMCP, Confirm},
		{ClassEval, ModeMCP, Confirm},
		{ClassCredential, ModeMCP, Confirm},
		{ClassNetworkMock, ModeMCP, Deny},
		{ClassSubmit, ModeTTY, Allow},
		{ClassCredential, ModeTTY, Confirm},
		{ClassNetworkMock, ModeTTY, Confirm},
	}
	for _, tt := range tests {
		if got := Defaults(tt.class, tt.mode); got != tt.want {
			t.Fatalf("Defaults(%s, %s) = %s, want %s", tt.class, tt.mode, got, tt.want)
		}
	}
}

func TestPolicyFileOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.toml")
	content := `
[[rules]]
class = "submit"
domain = "bank.example.com"
decision = "deny"

[[rules]]
class = "credential"
domain = "login.example.com"
decision = "allow"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	decision, origin := policy.Decide(ClassSubmit, "bank.example.com", ModeMCP)
	if decision != Deny || !strings.HasPrefix(origin, "rule:") {
		t.Fatalf("submit/bank = %s (%s)", decision, origin)
	}
	// Non-matching host keeps the MCP default.
	decision, _ = policy.Decide(ClassSubmit, "other.example.com", ModeMCP)
	if decision != Confirm {
		t.Fatalf("submit/other = %s", decision)
	}
	// Subdomain matches a dotted rule.
	decision, _ = policy.Decide(ClassCredential, "app.login.example.com", ModeMCP)
	if decision != Allow {
		t.Fatalf("credential subdomain = %s", decision)
	}
}

func TestPolicyMissingFileUsesDefaults(t *testing.T) {
	policy, err := LoadPolicy(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if decision, origin := policy.Decide(ClassNetworkMock, "example.com", ModeMCP); decision != Deny || origin != "default" {
		t.Fatalf("network-mock = %s (%s)", decision, origin)
	}
}

func TestPolicyValidateRejectsBadRules(t *testing.T) {
	for _, content := range []string{
		`[[rules]] class="bogus" domain="x.com" decision="allow"`,
		`[[rules]] class="read" domain="" decision="allow"`,
		`[[rules]] class="read" domain="x.com" decision="maybe"`,
	} {
		path := filepath.Join(t.TempDir(), "policy.toml")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPolicy(path); err == nil {
			t.Fatalf("invalid policy accepted: %s", content)
		}
	}
}

func TestExplainShowsRuleOrigin(t *testing.T) {
	policy := &Policy{Source: "/nonexistent/policy.toml"}
	explanation, err := policy.Explain("auth.login", "https://login.example.com/app", ModeMCP)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auth.login", "credential", "login.example.com", "confirm", "default"} {
		if !strings.Contains(explanation, want) {
			t.Fatalf("explain missing %q:\n%s", want, explanation)
		}
	}
	if _, err := policy.Explain("not-a-command", "https://x.com", ModeMCP); err == nil {
		t.Fatal("unclassified command explained")
	}
}

func TestHostOf(t *testing.T) {
	tests := map[string]string{
		"https://example.com/path":                "example.com",
		"http://user:pass@sub.example.com:8080/x": "sub.example.com",
		"example.com": "example.com",
		"":            "",
	}
	for input, want := range tests {
		if got := hostOf(input); got != want {
			t.Fatalf("hostOf(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSortedCommandsDeterministic(t *testing.T) {
	first := SortedCommands()
	second := SortedCommands()
	if len(first) == 0 {
		t.Fatal("no classified commands")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatal("sorted commands not deterministic")
		}
	}
}
