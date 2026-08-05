package policy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeGuard writes an executable symguard script into a temp dir and returns
// its path. The script prints verdict (or fails when failWith is set).
func fakeGuard(t *testing.T, verdict string, failWith string) string {
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

func TestDetectGuardFromPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake unix executable under test; POSIX exec semantics are covered on linux/darwin CI")
	}

	path := fakeGuard(t, `{"decision":"allow"}`, "")
	t.Setenv(GuardEnvName, "")
	t.Setenv("PATH", filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	guard := DetectGuard()
	if guard == nil || !guard.Active() {
		t.Fatal("guard not detected on PATH")
	}
	if guard.Executable != path {
		t.Fatalf("executable = %s, want %s", guard.Executable, path)
	}
}

func TestDetectGuardEnvOverrideAndDisable(t *testing.T) {
	path := fakeGuard(t, `{"decision":"allow"}`, "")
	t.Setenv(GuardEnvName, path)
	if guard := DetectGuard(); guard == nil || guard.Executable != path {
		t.Fatalf("env override not honored: %+v", guard)
	}
	for _, disable := range []string{"0", "none", "off", "false"} {
		t.Setenv(GuardEnvName, disable)
		if guard := DetectGuard(); guard != nil {
			t.Fatalf("disable value %q still detected a guard", disable)
		}
	}
	t.Setenv(GuardEnvName, "")
	t.Setenv("PATH", "/nonexistent")
	if guard := DetectGuard(); guard != nil {
		t.Fatalf("guard detected without binary: %+v", guard)
	}
}

func TestGuardDecide(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake unix executable under test; POSIX exec semantics are covered on linux/darwin CI")
	}

	path := fakeGuard(t, `{"decision":"deny","reason":"test guard says no"}`, "")
	guard := &Guard{Executable: path, Timeout: 2 * time.Second}
	outcome, err := guard.Decide(context.Background(), GuardInput{Command: "eval", Class: ClassEval, Domain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Decision != Deny || outcome.Reason != "test guard says no" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestGuardDecideFailuresDeny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake unix executable under test; POSIX exec semantics are covered on linux/darwin CI")
	}

	// Non-zero exit with a stderr message.
	guard := &Guard{Executable: fakeGuard(t, "", "guard exploded"), Timeout: 2 * time.Second}
	if _, err := guard.Decide(context.Background(), GuardInput{Command: "eval", Class: ClassEval}); err == nil {
		t.Fatal("expected an error for a failing guard")
	} else if !strings.Contains(err.Error(), "guard exploded") {
		t.Fatalf("error = %v, want stderr message", err)
	}
	// Unparseable verdict.
	guard = &Guard{Executable: fakeGuard(t, "not json", ""), Timeout: 2 * time.Second}
	if _, err := guard.Decide(context.Background(), GuardInput{Command: "eval", Class: ClassEval}); err == nil {
		t.Fatal("expected an error for an unparseable verdict")
	}
	// Invalid decision value.
	guard = &Guard{Executable: fakeGuard(t, `{"decision":"maybe"}`, ""), Timeout: 2 * time.Second}
	if _, err := guard.Decide(context.Background(), GuardInput{Command: "eval", Class: ClassEval}); err == nil {
		t.Fatal("expected an error for an invalid decision value")
	}
	// Missing executable.
	guard = &Guard{Executable: filepath.Join(t.TempDir(), "missing"), Timeout: 2 * time.Second}
	if _, err := guard.Decide(context.Background(), GuardInput{Command: "eval", Class: ClassEval}); err == nil {
		t.Fatal("expected an error for a missing executable")
	}
	// Inactive guard.
	guard = &Guard{}
	if _, err := guard.Decide(context.Background(), GuardInput{Command: "eval", Class: ClassEval}); err == nil {
		t.Fatal("expected an error for an inactive guard")
	}
}
