// Command diffharness compares two binaries as isolated black boxes.
package main

import (
	"debug/buildinfo"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	portdiff "github.com/danieljustus/symaira-browse/scripts/rust-port/internal/diff"
)

func main() {
	left := flag.String("left", "", "path to the reference binary")
	right := flag.String("right", "", "path to the candidate binary")
	casesPath := flag.String("cases", "testdata/port/bootstrap/cases.json", "path to the case suite")
	stage := flag.String("stage", "", "run only cases assigned to this migration stage")
	expectedCommit := flag.String("expect-oracle-commit", "", "require this exact oracle commit in the suite")
	expectedRelease := flag.String("expect-oracle-release", "", "require this exact oracle release in the suite")
	verifyLeftGoRevision := flag.Bool("verify-left-go-revision", false, "require the left Go binary to embed the expected oracle commit")
	flag.Parse()
	if *left == "" || *right == "" {
		fatal("--left and --right are required")
	}

	content, err := os.ReadFile(*casesPath)
	if err != nil {
		fatal("read cases: %v", err)
	}
	var suite portdiff.Suite
	if err := json.Unmarshal(content, &suite); err != nil {
		fatal("decode cases: %v", err)
	}
	if suite.SchemaVersion != 1 {
		fatal("unsupported case schema_version %d", suite.SchemaVersion)
	}
	if suite.Oracle.Commit == "" || suite.Oracle.Release == "" {
		fatal("case suite oracle commit and release are required")
	}
	if *expectedCommit != "" && suite.Oracle.Commit != *expectedCommit {
		fatal("oracle commit mismatch: suite=%s expected=%s", suite.Oracle.Commit, *expectedCommit)
	}
	if *expectedRelease != "" && suite.Oracle.Release != *expectedRelease {
		fatal("oracle release mismatch: suite=%s expected=%s", suite.Oracle.Release, *expectedRelease)
	}
	if *verifyLeftGoRevision {
		if *expectedCommit == "" {
			fatal("--verify-left-go-revision requires --expect-oracle-commit")
		}
		if err := verifyGoRevision(*left, *expectedCommit); err != nil {
			fatal("left Go oracle provenance: %v", err)
		}
	}
	if len(suite.Cases) == 0 {
		fatal("case suite is empty")
	}

	seen := make(map[string]bool, len(suite.Cases))
	passed := 0
	for _, testCase := range suite.Cases {
		if testCase.ID == "" || seen[testCase.ID] {
			fatal("case IDs must be non-empty and unique: %q", testCase.ID)
		}
		seen[testCase.ID] = true
		if *stage != "" && testCase.Stage != *stage {
			continue
		}
		leftResult, err := portdiff.Run(*left, testCase)
		if err != nil {
			fatal("%s left run: %v", testCase.ID, err)
		}
		rightResult, err := portdiff.Run(*right, testCase)
		if err != nil {
			fatal("%s right run: %v", testCase.ID, err)
		}
		if err := portdiff.Compare(testCase, leftResult, rightResult); err != nil {
			fatal("%s: %v", testCase.ID, err)
		}
		fmt.Printf("PASS %s\n", testCase.ID)
		passed++
	}
	if passed == 0 {
		fatal("no cases selected for stage %q", *stage)
	}
	fmt.Printf("PASS all %d selected differential cases\n", passed)
}

func verifyGoRevision(binary, expected string) error {
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("read build info: %w", err)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["vcs.revision"] != expected {
		return fmt.Errorf("revision=%q expected=%q", settings["vcs.revision"], expected)
	}
	return nil
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
