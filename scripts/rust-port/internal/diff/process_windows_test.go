//go:build windows

package diff

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunTimeoutKillsWindowsDescendantTree(t *testing.T) {
	caseSpec := Case{
		ID:        "timeout-child",
		Args:      []string{"-test.run=TestRunAndCompareIdenticalHelper"},
		Env:       map[string]string{"SYMBROWSE_PORT_HELPER": "1", "PORT_HELPER_MODE": "child"},
		TimeoutMS: 250,
	}
	result, err := Run(os.Args[0], caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut {
		t.Fatal("expected process timeout")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		t.Fatalf("parse helper child PID: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		output, taskErr := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").CombinedOutput()
		if taskErr == nil && !strings.Contains(string(output), fmt.Sprintf("\"%d\"", pid)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d survived tree termination: %s (%v)", pid, output, taskErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
