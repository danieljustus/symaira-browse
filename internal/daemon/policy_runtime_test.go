package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// fakeGuardScript writes an executable symguard script that prints verdict
// (or fails) and returns its path.
func fakeGuardScript(t *testing.T, verdict, failWith string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "symguard")
	script := "#!/bin/sh\n"
	if failWith != "" {
		script += "echo '" + failWith + "' >&2\nexit 3\n"
	} else {
		script += "printf '%s\\n' '" + verdict + "'\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func guardRuntime(t *testing.T, guard *policy.Guard) *PolicyRuntime {
	t.Helper()
	return NewPolicyRuntimeWithGuard(t.TempDir(), policy.ModeTTY, guard)
}

func TestPolicyDecideFallsBackWithoutGuard(t *testing.T) {
	runtime := guardRuntime(t, nil)
	decision, decider, reason, err := runtime.Decide(context.Background(), "snapshot", "https://example.com", policy.ModeTTY, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision != policy.Allow || decider != "policy" {
		t.Fatalf("decision=%s decider=%s, want allow/policy (fallback)", decision, decider)
	}
	if reason != "default" {
		t.Fatalf("reason = %s, want default", reason)
	}
	// Unknown command still errors.
	if _, _, _, err := runtime.Decide(context.Background(), "no.such.command", "https://example.com", policy.ModeTTY, nil); err == nil {
		t.Fatal("expected an error for an unclassified command")
	}
}

func TestPolicyDecideDelegatesToGuard(t *testing.T) {
	guard := &policy.Guard{Executable: fakeGuardScript(t, `{"decision":"deny","reason":"domain blocked by guard"}`, ""), Timeout: 2 * time.Second}
	runtime := guardRuntime(t, guard)
	decision, decider, reason, err := runtime.Decide(context.Background(), "snapshot", "https://example.com", policy.ModeTTY, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision != policy.Deny || decider != "guard" {
		t.Fatalf("decision=%s decider=%s, want deny/guard", decision, decider)
	}
	if !strings.Contains(reason, "domain blocked by guard") {
		t.Fatalf("reason = %s, want guard reason", reason)
	}
	// A guard allow wins over the local policy default.
	guard = &policy.Guard{Executable: fakeGuardScript(t, `{"decision":"allow"}`, ""), Timeout: 2 * time.Second}
	runtime = guardRuntime(t, guard)
	decision, decider, _, err = runtime.Decide(context.Background(), "network.route", "https://example.com", policy.ModeMCP, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision != policy.Allow || decider != "guard" {
		t.Fatalf("decision=%s decider=%s, want allow/guard (local default is deny)", decision, decider)
	}
}

func TestPolicyDecideGuardFailureDenies(t *testing.T) {
	guard := &policy.Guard{Executable: fakeGuardScript(t, "", "guard crashed"), Timeout: 2 * time.Second}
	runtime := guardRuntime(t, guard)
	decision, decider, reason, err := runtime.Decide(context.Background(), "snapshot", "https://example.com", policy.ModeTTY, nil)
	if err != nil {
		t.Fatal(err)
	}
	if decision != policy.Deny {
		t.Fatalf("decision = %s, want deny on guard failure (no silent allow)", decision)
	}
	if decider != "guard" {
		t.Fatalf("decider = %s, want guard", decider)
	}
	if !strings.Contains(reason, "guard failure") {
		t.Fatalf("reason = %s, want guard failure message", reason)
	}
}

func TestPolicyExplainShowsDecider(t *testing.T) {
	runtime := guardRuntime(t, nil)
	payload, _ := json.Marshal(map[string]any{"command": "eval", "url": "https://example.com"})
	data, _, err := runtime.Handle(context.Background(), Frame{Cmd: "policy.explain", Args: payload, Session: "s"})
	if err != nil {
		t.Fatal(err)
	}
	explanation, _ := data.(map[string]any)
	if explanation["decider"] != "policy" {
		t.Fatalf("decider = %v, want policy", explanation["decider"])
	}
	if explanation["guard_active"] != false {
		t.Fatalf("guard_active = %v, want false", explanation["guard_active"])
	}
	if !strings.Contains(explanation["explanation"].(string), "decider:   policy") {
		t.Fatalf("explanation lacks decider line: %v", explanation["explanation"])
	}

	guard := &policy.Guard{Executable: fakeGuardScript(t, `{"decision":"confirm"}`, ""), Timeout: 2 * time.Second}
	runtime = guardRuntime(t, guard)
	data, _, err = runtime.Handle(context.Background(), Frame{Cmd: "policy.explain", Args: payload, Session: "s"})
	if err != nil {
		t.Fatal(err)
	}
	explanation, _ = data.(map[string]any)
	if explanation["decider"] != "guard" || explanation["guard_active"] != true {
		t.Fatalf("decider=%v guard_active=%v, want guard/true", explanation["decider"], explanation["guard_active"])
	}
	if !strings.Contains(explanation["explanation"].(string), "decider:   guard") {
		t.Fatalf("explanation lacks guard decider line: %v", explanation["explanation"])
	}
}

func TestDeciderFor(t *testing.T) {
	runtime := guardRuntime(t, nil)
	if got := runtime.DeciderFor("open"); got != "policy" {
		t.Fatalf("DeciderFor = %s, want policy", got)
	}
	guard := &policy.Guard{Executable: fakeGuardScript(t, `{"decision":"allow"}`, ""), Timeout: 2 * time.Second}
	runtime = guardRuntime(t, guard)
	if got := runtime.DeciderFor("open"); got != "guard" {
		t.Fatalf("DeciderFor = %s, want guard", got)
	}
}
