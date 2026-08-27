package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// countingRunner records every executed command; used to prove that --dry-run
// executes nothing.
type countingRunner struct {
	executed []string
	failOn   map[string]error
}

func (c *countingRunner) run(_ context.Context, argv []string, stdout, stderr io.Writer) error {
	c.executed = append(c.executed, strings.Join(argv, " "))
	if err := c.failOn[argv[0]]; err != nil {
		_, _ = io.WriteString(stderr, err.Error())
		return err
	}
	_, _ = io.WriteString(stdout, "ok:"+argv[0])
	return nil
}

func TestBatchPartialFailureContinuesWithoutBail(t *testing.T) {
	runner := &countingRunner{failOn: map[string]error{"snapshot": errors.New("boom")}}
	report := runBatch(context.Background(), newRootCommand(), batchOptions{
		Commands: []string{"open https://example.com", "snapshot", "get.title"},
	}, runner.run)

	if len(report.Results) != 3 {
		t.Fatalf("results = %#v, want 3 items", report.Results)
	}
	if report.Results[0].Success != true || report.Results[1].Success != false || report.Results[2].Success != true {
		t.Fatalf("per-item success flags wrong: %#v", report.Results)
	}
	if report.Results[1].Error != "boom" {
		t.Fatalf("error = %q", report.Results[1].Error)
	}
	if report.Bailed {
		t.Fatal("partial failure must not bail without --bail")
	}
	if len(runner.executed) != 3 {
		t.Fatalf("executed = %#v, want all 3 commands", runner.executed)
	}
	if report.Results[0].Data != "ok:open" {
		t.Fatalf("data = %#v", report.Results[0].Data)
	}
}

func TestBatchBailStopsAtFirstFailure(t *testing.T) {
	runner := &countingRunner{failOn: map[string]error{"open": errors.New("boom")}}
	report := runBatch(context.Background(), newRootCommand(), batchOptions{
		Commands: []string{"open https://example.com", "snapshot"},
		Bail:     true,
	}, runner.run)

	if !report.Bailed {
		t.Fatal("expected bailed report")
	}
	if len(report.Results) != 1 {
		t.Fatalf("results = %#v, want only the failed item", report.Results)
	}
	if len(runner.executed) != 1 {
		t.Fatalf("executed = %#v, want 1 command", runner.executed)
	}
}

func TestBatchDryRunDoesNotExecute(t *testing.T) {
	runner := &countingRunner{failOn: map[string]error{}}
	report := runBatch(context.Background(), newRootCommand(), batchOptions{
		Commands: []string{"click @e1", "fill @e2 hello"},
		DryRun:   true,
	}, runner.run)

	if len(runner.executed) != 0 {
		t.Fatalf("dry-run executed %d commands: %#v", len(runner.executed), runner.executed)
	}
	if len(report.Plan) != 2 {
		t.Fatalf("plan = %#v, want 2 items", report.Plan)
	}
	if report.Plan[0].Command != "click @e1" || report.Plan[0].RiskClass != policy.ClassInteract {
		t.Fatalf("plan[0] = %#v", report.Plan[0])
	}
	if report.Plan[1].RiskClass != policy.ClassInteract {
		t.Fatalf("plan[1] = %#v", report.Plan[1])
	}
	if len(report.Results) != 0 {
		t.Fatalf("dry-run must not produce results: %#v", report.Results)
	}
}

func TestBatchDryRunRiskClasses(t *testing.T) {
	report := runBatch(context.Background(), newRootCommand(), batchOptions{
		Commands: []string{"read https://example.com", "open https://example.com", "version"},
		DryRun:   true,
	}, func(context.Context, []string, io.Writer, io.Writer) error { return nil })

	if report.Plan[0].RiskClass != policy.ClassRead {
		t.Fatalf("read risk = %q, want read", report.Plan[0].RiskClass)
	}
	if report.Plan[1].RiskClass != policy.ClassNavigate {
		t.Fatalf("open risk = %q, want navigate", report.Plan[1].RiskClass)
	}
	if report.Plan[2].RiskClass != policy.RiskClass("unknown") {
		t.Fatalf("version risk = %q, want unknown", report.Plan[2].RiskClass)
	}
}

func TestBatchEmptyAndUnknownCommands(t *testing.T) {
	runner := &countingRunner{failOn: map[string]error{}}
	report := runBatch(context.Background(), newRootCommand(), batchOptions{
		Commands: []string{"", "snapshot"},
	}, runner.run)

	if len(report.Results) != 2 {
		t.Fatalf("results = %#v", report.Results)
	}
	if report.Results[0].Success || report.Results[0].Error != "empty command" {
		t.Fatalf("empty command handling wrong: %#v", report.Results[0])
	}
	if report.Results[1].Success != true {
		t.Fatalf("second command should succeed: %#v", report.Results[1])
	}
}

func TestTokenizeQuotedValues(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"open https://example.com", []string{"open", "https://example.com"}},
		{"fill @e1 \"hello world\"", []string{"fill", "@e1", "hello world"}},
		{"type @e2 'hello world'", []string{"type", "@e2", "hello world"}},
		{"snapshot --selector \".main article\"", []string{"snapshot", "--selector", ".main article"}},
		{"", []string{}},
	}
	for _, tc := range cases {
		got, err := tokenize(tc.input)
		if err != nil {
			t.Fatalf("tokenize(%q): %v", tc.input, err)
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Fatalf("tokenize(%q) = %#v, want %#v", tc.input, got, tc.want)
		}
	}
	if _, err := tokenize(`"unbalanced`); err == nil {
		t.Fatal("expected unbalanced quote error")
	}
}

func TestResolveBatchCommandsFromStdin(t *testing.T) {
	commands, err := resolveBatchCommands(strings.NewReader(`["open https://a", "snapshot"]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0] != "open https://a" {
		t.Fatalf("commands = %#v", commands)
	}

	commands, err = resolveBatchCommands(strings.NewReader(""), []string{"version"})
	if err != nil || len(commands) != 1 {
		t.Fatalf("positional commands = %#v, err = %v", commands, err)
	}

	if _, err := resolveBatchCommands(strings.NewReader("not json"), nil); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestBatchJSONItemDecodesEnvelope(t *testing.T) {
	// version --json emits the raw versionkit handshake payload (issue
	// #32: {tool, version, schema_version}, no envelope), and batch
	// reports it as decoded JSON data.
	report := runBatch(context.Background(), newRootCommand(), batchOptions{
		Commands: []string{"version --json"},
	}, runBatchItem)

	if !report.Results[0].Success {
		t.Fatalf("result = %#v", report.Results[0])
	}
	payload, ok := report.Results[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want decoded JSON object", report.Results[0].Data)
	}
	if payload["tool"] != "symbrowse" || payload["schema_version"] != float64(4) {
		t.Fatalf("payload = %#v", payload)
	}
}
