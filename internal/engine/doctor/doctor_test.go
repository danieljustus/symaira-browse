package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunJSONHasStableSchemaAndStatuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake unix executable under test; POSIX exec semantics are covered on linux/darwin CI")
	}

	dir := t.TempDir()
	executable := writeExecutable(t, filepath.Join(dir, "chrome"), "#!/bin/sh\necho 'Google Chrome 123.0'\n")
	paths := Paths{
		ConfigDir: filepath.Join(dir, "config"),
		CacheDir:  filepath.Join(dir, "cache"),
		StateDir:  filepath.Join(dir, "state"),
	}
	for _, path := range []string{paths.ConfigDir, paths.CacheDir, paths.StateDir} {
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	report := run(Options{
		ExecutablePath: executable,
		CDPEndpoint:    "://invalid",
		Paths:          paths,
	}, "linux", os.Getenv, exec.LookPath)
	if report.Status != StatusWarn {
		t.Fatalf("status = %q, want %q", report.Status, StatusWarn)
	}
	if got, want := len(report.Checks), len(StableCheckNames()); got != want {
		t.Fatalf("check count = %d, want %d", got, want)
	}
	for index, name := range StableCheckNames() {
		if report.Checks[index].Name != name {
			t.Fatalf("check[%d].name = %q, want %q", index, report.Checks[index].Name, name)
		}
		switch report.Checks[index].Status {
		case StatusPass, StatusWarn, StatusFail, StatusSkipped:
		default:
			t.Fatalf("check[%d] has unstable status %q", index, report.Checks[index].Status)
		}
	}

	var output struct {
		Status string  `json:"status"`
		Checks []Check `json:"checks"`
	}
	var buffer bytes.Buffer
	if err := Write(&buffer, report, true); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buffer.Bytes(), &output); err != nil {
		t.Fatalf("JSON = %q: %v", buffer.String(), err)
	}
	if output.Status != report.Status || len(output.Checks) != len(report.Checks) {
		t.Fatalf("decoded output = %#v", output)
	}
}

func TestMissingChromeMentionsPATHAndOverrideWithManipulatedPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", filepath.Join(t.TempDir(), "program-files"))
	t.Setenv("ProgramFiles(x86)", filepath.Join(t.TempDir(), "program-files-x86"))
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "local-app-data"))
	t.Setenv("USERPROFILE", t.TempDir())

	report := run(Options{
		CDPEndpoint: "://invalid",
		Paths: Paths{
			ConfigDir: t.TempDir(),
			CacheDir:  t.TempDir(),
			StateDir:  t.TempDir(),
		},
	}, "windows", os.Getenv, exec.LookPath)
	if !report.HasFailure("chrome") {
		t.Fatalf("report = %#v, want chrome failure", report)
	}
	message := report.Checks[0].Message
	if !strings.Contains(message, "PATH:") || !strings.Contains(message, "SYMBROWSE_EXECUTABLE_PATH") {
		t.Fatalf("message = %q", message)
	}
}

func TestExecutableOverrideTakesPrecedenceOverPATH(t *testing.T) {
	pathDir := t.TempDir()
	pathExecutable := writeExecutable(t, filepath.Join(pathDir, "chrome.exe"), "not a real browser")
	override := writeExecutable(t, filepath.Join(t.TempDir(), "preferred-browser"), "not a real browser")
	t.Setenv("PATH", pathDir)
	t.Setenv("ProgramFiles", filepath.Join(t.TempDir(), "program-files"))
	t.Setenv("ProgramFiles(x86)", filepath.Join(t.TempDir(), "program-files-x86"))
	t.Setenv("LOCALAPPDATA", filepath.Join(t.TempDir(), "local-app-data"))

	report := run(Options{
		ExecutablePath: override,
		CDPEndpoint:    "://invalid",
		Paths: Paths{
			ConfigDir: t.TempDir(),
			CacheDir:  t.TempDir(),
			StateDir:  t.TempDir(),
		},
	}, "windows", os.Getenv, exec.LookPath)
	chrome := report.Checks[0]
	if chrome.Status != StatusPass || chrome.Details["path"] != override || chrome.Details["source"] != "SYMBROWSE_EXECUTABLE_PATH" {
		t.Fatalf("chrome check = %#v, PATH executable = %q", chrome, pathExecutable)
	}
}

func TestVersionTimeoutAndErrorAreBoundedWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake unix executable under test; POSIX exec semantics are covered on linux/darwin CI")
	}

	timeoutExecutable := writeExecutable(t, filepath.Join(t.TempDir(), "slow-browser"), "#!/bin/sh\nsleep 1\n")
	check := checkVersion(timeoutExecutable, 20*time.Millisecond)
	if check.Status != StatusWarn || !strings.Contains(check.Message, "timed out") {
		t.Fatalf("timeout check = %#v", check)
	}

	errorExecutable := writeExecutable(t, filepath.Join(t.TempDir(), "broken-browser"), "#!/bin/sh\necho broken >&2\nexit 7\n")
	check = checkVersion(errorExecutable, time.Second)
	if check.Status != StatusWarn || !strings.Contains(check.Message, "version check failed") {
		t.Fatalf("error check = %#v", check)
	}
}

func TestFixInstructionsAreNonMutatingAndCopyable(t *testing.T) {
	instructions := FixInstructions("linux", Options{ExecutablePath: "/tmp/a browser's chrome"})
	joined := strings.Join(instructions, "\n")
	for _, want := range []string{
		"No changes were made",
		"SYMBROWSE_EXECUTABLE_PATH='",
		"symbrowse doctor --json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("instructions = %q, missing %q", joined, want)
		}
	}
	if !strings.Contains(joined, "'\\\"'\\\"'") {
		t.Fatalf("shell quoting is not safe: %q", joined)
	}
}

func writeExecutable(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// tempExecutable creates a real, executable file on disk so usableExecutable
// accepts it during discovery tests.
func tempExecutable(t *testing.T) string {
	t.Helper()
	return writeExecutable(t, filepath.Join(t.TempDir(), "chrome"), "#!/bin/sh\nexit 0\n")
}

// TestDiscoverOverrideWins verifies the explicit override beats platform and
// PATH discovery (absolute path, so lookPath must not be consulted).
func TestDiscoverOverrideWins(t *testing.T) {
	exe := tempExecutable(t)
	lookPathCalls := 0
	browser, err := discover("linux", exe,
		func(string) string { return "" },
		func(string) (string, error) { lookPathCalls++; return "", errors.New("not on PATH") })
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if browser.Path != exe || browser.Source != "SYMBROWSE_EXECUTABLE_PATH" {
		t.Errorf("browser = %+v, want %s via override", browser, exe)
	}
	if lookPathCalls != 0 {
		t.Errorf("lookPath called %d times for absolute override", lookPathCalls)
	}
}

// TestDiscoverOverrideUnusableError verifies an unusable override fails
// before platform discovery and reports the override in the error.
func TestDiscoverOverrideUnusableError(t *testing.T) {
	_, err := discover("linux", "/nonexistent/chrome-binary",
		func(string) string { return "" },
		func(string) (string, error) { return "", errors.New("not on PATH") })
	if err == nil {
		t.Fatal("expected an error for an unusable override")
	}
	if err.Override != "/nonexistent/chrome-binary" {
		t.Errorf("override = %q, want it reported", err.Override)
	}
}

// TestDiscoverPATHFallback verifies discovery finds an executable on PATH
// when no platform path exists.
func TestDiscoverPATHFallback(t *testing.T) {
	exe := tempExecutable(t)
	browser, err := discover("linux", "",
		func(string) string { return "" },
		func(name string) (string, error) {
			if name == "google-chrome" {
				return exe, nil
			}
			return "", errors.New("not found")
		})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if browser.Path != exe || browser.Source != "PATH" {
		t.Errorf("browser = %+v, want %s via PATH", browser, exe)
	}
}

// TestDiscoverNothingFoundError verifies the actionable error with search
// paths when nothing can be found.
func TestDiscoverNothingFoundError(t *testing.T) {
	_, err := discover("linux", "",
		func(string) string { return "" },
		func(string) (string, error) { return "", errors.New("not found") })
	if err == nil {
		t.Fatal("expected an error when nothing is found")
	}
	if len(err.SearchPaths) == 0 {
		t.Error("DiscoveryError has no search paths")
	}
	if !strings.Contains(err.Error(), "searched") {
		t.Errorf("error = %q, want searched paths", err.Error())
	}
}

// TestResolveExecutableUsesOverride verifies the shared resolver prefers the
// SYMBROWSE_EXECUTABLE_PATH override.
func TestResolveExecutableUsesOverride(t *testing.T) {
	exe := tempExecutable(t)
	environ := func(key string) string {
		if key == "SYMBROWSE_EXECUTABLE_PATH" {
			return exe
		}
		return ""
	}
	path, err := ResolveExecutable(environ, func(string) (string, error) { return "", errors.New("not on PATH") })
	if err != nil {
		t.Fatalf("ResolveExecutable: %v", err)
	}
	if path != exe {
		t.Errorf("path = %q, want %q", path, exe)
	}
}
