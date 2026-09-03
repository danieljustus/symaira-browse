package doctor

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine/safaribidi"
)

// checkSafariBidiPrerequisites verifies what the safari-bidi engine needs
// before a session is attempted (issue #355).
//
// This check exists because Safari's own failure is undiagnosable. Session
// creation fails after 30 seconds with:
//
//	Could not create a session: The session timed out while connecting to a
//	Safari instance. … Request creation of a new automation session
//
// and that identical text covers at least two unrelated causes. The
// discriminator appears only in the unified log, never in the WebDriver
// response:
//
//	[com.apple.Safari:Automation] Rejecting session (…): Safari was not
//	launched for automation.
//
// which is what Safari says when safaridriver attached to an already-running
// browsing Safari instead of launching its own. Measured 2026-09-03 on macOS
// 27.0 / Safari 27.0: with Safari running, session creation fails; with Safari
// quit, the identical request succeeds. Relaying Apple's message would leave a
// user with a timeout and no cause, so each cause is detected and named here.
func checkSafariBidiPrerequisites() Check {
	const name = "safari_bidi_prereq"
	if runtime.GOOS != "darwin" {
		return Check{
			Name:    name,
			Status:  StatusFail,
			Message: "safari-bidi engine requires macOS; it drives Safari through safaridriver",
			Details: map[string]string{"platform": runtime.GOOS},
		}
	}
	if _, err := os.Stat(safaribidi.DriverPath); err != nil {
		return Check{
			Name:    name,
			Status:  StatusFail,
			Message: "safaridriver was not found; safari-bidi needs the driver that ships with Safari",
			Details: map[string]string{"path": safaribidi.DriverPath, "error": err.Error()},
		}
	}
	// safaridriver --bidi exists only in newer Safari releases. Probing the
	// usage text is cheap and does not start a session.
	if !driverSupportsBidi() {
		return Check{
			Name:    name,
			Status:  StatusFail,
			Message: "this safaridriver has no --bidi mode; safari-bidi needs a Safari that speaks WebDriver BiDi",
			Details: map[string]string{
				"path":        safaribidi.DriverPath,
				"remediation": "update macOS/Safari, or use the safari-attach engine instead",
			},
		}
	}
	// The cause that Apple's timeout hides: a Safari already running for
	// normal browsing. safaridriver attaches to it instead of launching its
	// own automation instance, and Safari rejects the session.
	if safariIsRunning() {
		return Check{
			Name:    name,
			Status:  StatusFail,
			Message: "Safari is already running for normal browsing; safaridriver attaches to that instance and Safari rejects the automation session",
			Details: map[string]string{
				"remediation": "quit Safari completely (Cmd-Q) before using safari-bidi; safaridriver then launches its own automation instance",
				"symptom":     "without this, session creation fails after 30s with Safari's generic \"Request creation of a new automation session\" timeout, which names no cause",
				"note":        "safari-attach is the engine for the running, logged-in Safari; safari-bidi always uses an isolated session",
			},
		}
	}
	return Check{
		Name:    name,
		Status:  StatusPass,
		Message: "safaridriver supports --bidi and no browsing Safari is holding the automation slot",
		Details: map[string]string{
			"engine":      "safari-bidi",
			"path":        safaribidi.DriverPath,
			"remediation": "if session creation still times out, run \"sudo safaridriver --enable\" once and enable Safari > Settings > Developer > Allow remote automation",
		},
	}
}

// safariIsRunning reports whether a normal Safari is running. pgrep -x matches
// the browser itself, not its many helper processes.
var safariIsRunning = func() bool {
	return exec.Command("pgrep", "-x", "Safari").Run() == nil
}

// driverSupportsBidi reports whether safaridriver advertises --bidi. The driver
// prints its usage to stdout and exits non-zero when given no configuration
// argument, so the exit status is deliberately ignored.
var driverSupportsBidi = func() bool {
	out, _ := exec.Command(safaribidi.DriverPath, "--help").CombinedOutput()
	return containsBidiFlag(string(out))
}

func containsBidiFlag(usage string) bool {
	return strings.Contains(usage, "--bidi")
}
