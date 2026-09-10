package diff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRunAndCompareIdenticalHelper(t *testing.T) {
	if os.Getenv("SYMBROWSE_PORT_HELPER") == "1" {
		helperProcess()
		return
	}
	caseSpec := Case{
		ID:           "helper",
		Args:         []string{"-test.run=TestRunAndCompareIdenticalHelper"},
		Env:          map[string]string{"SYMBROWSE_PORT_HELPER": "1", "PORT_OUTPUT": "${WORKSPACE}/out.txt"},
		CompareFiles: true,
	}
	left, err := Run(os.Args[0], caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Run(os.Args[0], caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := Compare(caseSpec, left, right); err != nil {
		t.Fatal(err)
	}
}

func TestCompareDetectsStreamMismatchWithoutLeakingContent(t *testing.T) {
	testCase := Case{ID: "mismatch"}
	err := Compare(testCase, Result{Stdout: []byte("secret-left")}, Result{Stdout: []byte("secret-right")})
	if err == nil {
		t.Fatal("expected mismatch")
	}
	if got := err.Error(); contains(got, "secret-left") || contains(got, "secret-right") {
		t.Fatalf("mismatch exposed stream content: %s", got)
	}
}

func TestCompareDiagnoseContentOptInShowsBoundedExcerpt(t *testing.T) {
	testCase := Case{ID: "mismatch", DiagnoseContent: true}
	err := Compare(testCase, Result{Stdout: []byte("path-left")}, Result{Stdout: []byte("path-right")})
	if err == nil {
		t.Fatal("expected mismatch")
	}
	got := err.Error()
	if !contains(got, "path-left") || !contains(got, "path-right") {
		t.Fatalf("opt-in diagnosis missing excerpt: %s", got)
	}
	if !contains(got, "first diff at line 1") {
		t.Fatalf("opt-in diagnosis missing locator: %s", got)
	}
}

func TestNegativeControlDetectsDifferentExecutables(t *testing.T) {
	extension := filepath.Ext(os.Args[0])
	leftPath := filepath.Join(t.TempDir(), "left-helper"+extension)
	rightPath := filepath.Join(t.TempDir(), "right-helper"+extension)
	copyExecutable(t, os.Args[0], leftPath)
	copyExecutable(t, os.Args[0], rightPath)
	caseSpec := Case{
		ID:   "negative-control",
		Args: []string{"-test.run=TestRunAndCompareIdenticalHelper"},
		Env: map[string]string{
			"SYMBROWSE_PORT_HELPER": "1",
			"PORT_HELPER_MODE":      "identity",
		},
	}
	left, err := Run(leftPath, caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Run(rightPath, caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := Compare(caseSpec, left, right); err == nil {
		t.Fatal("negative control failed to detect different executable output")
	}
}

func TestRunTimesOutAndTerminatesProcess(t *testing.T) {
	caseSpec := Case{
		ID:        "timeout",
		Args:      []string{"-test.run=TestRunAndCompareIdenticalHelper"},
		Env:       map[string]string{"SYMBROWSE_PORT_HELPER": "1", "PORT_HELPER_MODE": "hang"},
		TimeoutMS: 50,
	}
	result, err := Run(os.Args[0], caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut {
		t.Fatal("expected process timeout")
	}
}

func TestRunCapturesSideEffectsOutsideWorkspace(t *testing.T) {
	caseSpec := Case{
		ID:   "home-side-effect",
		Args: []string{"-test.run=TestRunAndCompareIdenticalHelper"},
		Env:  map[string]string{"SYMBROWSE_PORT_HELPER": "1", "PORT_OUTPUT": "${HOME}/out.txt"},
	}
	result, err := Run(os.Args[0], caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range result.Files {
		if entry.Path == "home/out.txt" && entry.Type == "file" {
			return
		}
	}
	t.Fatalf("HOME side effect missing from sandbox manifest: %#v", result.Files)
}

func TestConsoleComparisonNormalizesRootsAndCRLF(t *testing.T) {
	testCase := Case{StdoutMode: "console_text"}
	left := Result{Stdout: []byte("path=/tmp/left/file\r\n"), SandboxRoot: "/tmp/left"}
	right := Result{Stdout: []byte("path=/tmp/right/file\n"), SandboxRoot: "/tmp/right"}
	if err := Compare(testCase, left, right); err != nil {
		t.Fatal(err)
	}
}

func TestJSONComparisonIgnoresOnlyConfiguredVolatileFields(t *testing.T) {
	testCase := Case{StdoutMode: "json", IgnoreJSONFields: []string{"duration_ms"}}
	left := Result{Stdout: []byte(`{"success":true,"data":{"results":[{"duration_ms":1,"value":"same","path":"/tmp/left/file"}]}}`), SandboxRoot: "/tmp/left"}
	right := Result{Stdout: []byte(`{"data":{"results":[{"path":"/tmp/right/file","value":"same","duration_ms":99}]},"success":true}`), SandboxRoot: "/tmp/right"}
	if err := Compare(testCase, left, right); err != nil {
		t.Fatal(err)
	}
	right.Stdout = []byte(`{"success":true,"data":{"results":[{"duration_ms":99,"value":"changed","path":"/tmp/right/file"}]}}`)
	if err := Compare(testCase, left, right); err == nil {
		t.Fatal("expected non-ignored JSON field mismatch")
	}
}

func TestBuildManifestIsDeterministicAndDetectsContent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "entry"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := buildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("manifest is not deterministic: %#v != %#v", first, second)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "entry"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := buildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(first, changed) {
		t.Fatal("manifest did not detect content change")
	}
}

func TestSafeWorkspacePathRejectsEscape(t *testing.T) {
	if _, err := safeWorkspacePath(t.TempDir(), "../escape"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestIsolatedEnvRejectsReservedOverrides(t *testing.T) {
	replacements := map[string]string{"${HOME}": "/isolated/home"}
	for _, key := range []string{"HOME", "xdg_data_home", "TMPDIR", "LANG", "LC_ALL", "TZ", "TERM", "NO_COLOR",
		"SYMBROWSE_CHECK_UPDATES", "SYMBROWSE_SYMGUARD", "SYMBROWSE_PORT_CLOCK", "SYMBROWSE_PORT_SEED"} {
		_, err := isolatedEnv("/isolated/home", "/isolated/tmp", "/isolated/runtime", "/isolated/state", "2026-01-01T00:00:00Z", "1", map[string]string{key: "host-value"}, replacements)
		if err == nil {
			t.Fatalf("expected override %q to be rejected", key)
		}
	}
}

func TestIsolatedEnvEnforcesCaseAllowlist(t *testing.T) {
	replacements := map[string]string{"${HOME}": "/isolated/home"}
	arguments := []string{"/isolated/home", "/isolated/tmp", "/isolated/runtime", "/isolated/state", "2026-01-01T00:00:00Z", "1"}
	for _, key := range []string{"PATH", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "AWS_SECRET_ACCESS_KEY"} {
		_, err := isolatedEnv(arguments[0], arguments[1], arguments[2], arguments[3], arguments[4], arguments[5], map[string]string{key: "host-value"}, replacements)
		if err == nil {
			t.Fatalf("expected non-allowlisted variable %q to be rejected", key)
		}
	}
	for _, key := range []string{"SYMBROWSE_ENGINE", "PORT_OUTPUT", "HTTP_PROXY", "NO_PROXY"} {
		_, err := isolatedEnv(arguments[0], arguments[1], arguments[2], arguments[3], arguments[4], arguments[5], map[string]string{key: "fixture-value"}, replacements)
		if err != nil {
			t.Fatalf("expected allowlisted variable %q: %v", key, err)
		}
	}
}

func helperProcess() {
	switch os.Getenv("PORT_HELPER_MODE") {
	case "hang":
		time.Sleep(30 * time.Second)
		return
	case "child":
		child := exec.Command(os.Args[0], "-test.run=TestRunAndCompareIdenticalHelper")
		child.Env = append(os.Environ(), "SYMBROWSE_PORT_HELPER=1", "PORT_HELPER_MODE=hang")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%d\n", child.Process.Pid)
		_ = child.Wait()
		return
	case "identity":
		_, _ = fmt.Fprintln(os.Stdout, filepath.Base(os.Args[0]))
		return
	}
	path := os.Getenv("PORT_OUTPUT")
	_ = os.WriteFile(path, []byte("deterministic\n"), 0o600)
	_, _ = os.Stdout.WriteString("ok\n")
}

func copyExecutable(t *testing.T, source, target string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o700); err != nil {
		t.Fatal(err)
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
