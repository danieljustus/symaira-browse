package doctor

import (
	"runtime"
	"strings"
	"testing"
)

func withProbes(t *testing.T, running, bidi bool) {
	t.Helper()
	previousRunning, previousBidi := safariIsRunning, driverSupportsBidi
	safariIsRunning = func() bool { return running }
	driverSupportsBidi = func() bool { return bidi }
	t.Cleanup(func() { safariIsRunning, driverSupportsBidi = previousRunning, previousBidi })
}

func TestSafariBidiPrereqRequiresMacOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this branch is only reachable off macOS")
	}
	check := checkSafariBidiPrerequisites()
	if check.Status != StatusFail {
		t.Fatalf("status = %v, want fail off macOS", check.Status)
	}
}

// A running Safari is the cause Apple's error text hides: safaridriver
// attaches to it and Safari rejects the automation session with a generic
// 30-second timeout. doctor must name the cause and the fix.
func TestSafariBidiPrereqDetectsRunningSafari(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	withProbes(t, true, true)
	check := checkSafariBidiPrerequisites()
	if check.Status != StatusFail {
		t.Fatalf("status = %v, want fail while Safari is running", check.Status)
	}
	if !strings.Contains(check.Message, "already running") {
		t.Errorf("message must name the cause: %s", check.Message)
	}
	remediation := check.Details["remediation"]
	if !strings.Contains(remediation, "quit Safari") {
		t.Errorf("remediation must tell the user to quit Safari: %q", remediation)
	}
	// The point of the check is that it replaces Apple's undiagnosable text.
	if !strings.Contains(check.Details["symptom"], "names no cause") {
		t.Errorf("check should explain the symptom it replaces: %q", check.Details["symptom"])
	}
}

func TestSafariBidiPrereqDetectsDriverWithoutBidi(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	withProbes(t, false, false)
	check := checkSafariBidiPrerequisites()
	if check.Status != StatusFail {
		t.Fatalf("status = %v, want fail without --bidi support", check.Status)
	}
	if !strings.Contains(check.Message, "--bidi") {
		t.Errorf("message must name the missing mode: %s", check.Message)
	}
	if !strings.Contains(check.Details["remediation"], "safari-attach") {
		t.Errorf("remediation should point at the engine that still works: %q", check.Details["remediation"])
	}
}

func TestSafariBidiPrereqPassesAndStillNamesTheEnableStep(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	withProbes(t, false, true)
	check := checkSafariBidiPrerequisites()
	if check.Status != StatusPass {
		t.Fatalf("status = %v (%s), want pass", check.Status, check.Message)
	}
	// The remaining prerequisite cannot be probed without creating a session,
	// which costs 30 seconds on failure, so a passing check still carries it.
	if !strings.Contains(check.Details["remediation"], "safaridriver --enable") {
		t.Errorf("a passing check must still name the enable step: %q", check.Details["remediation"])
	}
}

func TestContainsBidiFlagReadsRealUsageText(t *testing.T) {
	usage := `Usage: safaridriver [options]
	-p, --port                Port number the driver should use.
	-b, --bidi                Port number the driver should use to listen for incoming
	--enable                  Applies configuration changes`
	if !containsBidiFlag(usage) {
		t.Error("safaridriver usage advertising --bidi must be recognized")
	}
	if containsBidiFlag("Usage: safaridriver [options]\n\t-p, --port\n\t--enable") {
		t.Error("usage without --bidi must not be recognized")
	}
}

func TestDoctorReportsSafariBidiEngineCapabilities(t *testing.T) {
	check := checkEngineCapabilities("safari-bidi")
	if check.Details["kind"] != "safari-bidi" {
		t.Fatalf("kind = %q", check.Details["kind"])
	}
	if !strings.Contains(check.Details["interfaces"], "NetworkPolicyReporter") {
		t.Errorf("interfaces = %q", check.Details["interfaces"])
	}
	// Capability reporting is the contract agents adapt to, so the absent
	// input module must show up as unsupported here too.
	if !strings.Contains(check.Details["unsupported"], "InteractionEngine") {
		t.Errorf("InteractionEngine must be reported unsupported: %q", check.Details["unsupported"])
	}
}
