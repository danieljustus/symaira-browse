package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/output"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// batchRunner executes one batch item (argv without the program name) and
// writes its output to stdout/stderr. It is injectable for tests.
type batchRunner func(ctx context.Context, argv []string, stdout, stderr io.Writer) error

// batchOptions controls one batch run.
type batchOptions struct {
	Commands []string
	Bail     bool
	DryRun   bool
}

// batchPlanItem is one entry of the dry-run execution plan.
type batchPlanItem struct {
	Command   string           `json:"command"`
	RiskClass policy.RiskClass `json:"risk_class"`
}

// batchResult is the per-item result array.
type batchResult struct {
	Command    string `json:"command"`
	Success    bool   `json:"success"`
	Data       any    `json:"data,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// batchReport is the overall batch output.
type batchReport struct {
	Results []batchResult   `json:"results,omitempty"`
	Plan    []batchPlanItem `json:"plan,omitempty"`
	Bailed  bool            `json:"bailed,omitempty"`
}

func newBatchCommand() *cobra.Command {
	var bail, dryRun bool
	command := &cobra.Command{
		Use:   "batch <cmd> [cmd...]",
		Short: "Run multiple commands in one process and report per-item status",
		Long: "batch runs each quoted command string as a symbrowse invocation in the same " +
			"process, which avoids one daemon autostart and process startup per command. " +
			"Without positional arguments a JSON array of command strings is read from stdin. " +
			"--bail stops at the first failure; --dry-run returns the execution plan with " +
			"risk classes without executing anything.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			commands, err := resolveBatchCommands(cmd.InOrStdin(), args)
			if err != nil {
				return err
			}
			if len(commands) == 0 {
				return errors.New("batch requires at least one command")
			}
			options := batchOptions{Commands: commands, Bail: bail, DryRun: dryRun}
			report := runBatch(cmd.Context(), newRootCommand(), options, runBatchItem)
			return writeEnvelope(cmd, output.OK(report, nil))
		},
	}
	command.Flags().BoolVar(&bail, "bail", false, "stop at the first failed command")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "return the execution plan without executing")
	return command
}

// resolveBatchCommands takes positional command strings or, when none are
// given, a JSON array of command strings from stdin.
func resolveBatchCommands(stdin io.Reader, args []string) ([]string, error) {
	if len(args) > 0 {
		return args, nil
	}
	content, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read batch commands from stdin: %w", err)
	}
	var commands []string
	if err := json.Unmarshal(content, &commands); err != nil {
		return nil, fmt.Errorf("stdin must contain a JSON array of command strings: %w", err)
	}
	return commands, nil
}

// runBatch executes the commands sequentially and applies the bail/dry-run
// semantics. The runner is injectable for tests.
func runBatch(ctx context.Context, root *cobra.Command, options batchOptions, runner batchRunner) batchReport {
	if options.DryRun {
		plan := make([]batchPlanItem, 0, len(options.Commands))
		for _, command := range options.Commands {
			name := firstToken(command)
			plan = append(plan, batchPlanItem{Command: command, RiskClass: policy.ClassForCommand(name)})
		}
		return batchReport{Plan: plan}
	}

	results := make([]batchResult, 0, len(options.Commands))
	for _, command := range options.Commands {
		argv, err := tokenize(command)
		if err != nil {
			results = append(results, batchResult{Command: command, Success: false, Error: err.Error()})
			if options.Bail {
				return batchReport{Results: results, Bailed: true}
			}
			continue
		}
		if len(argv) == 0 {
			results = append(results, batchResult{Command: command, Success: false, Error: "empty command"})
			if options.Bail {
				return batchReport{Results: results, Bailed: true}
			}
			continue
		}
		result := runOne(ctx, root, command, argv, runner)
		results = append(results, result)
		if !result.Success && options.Bail {
			return batchReport{Results: results, Bailed: true}
		}
	}
	return batchReport{Results: results}
}

// runOne executes a single tokenized command against the root command and
// captures its output.
func runOne(ctx context.Context, root *cobra.Command, command string, argv []string, runner batchRunner) batchResult {
	var stdout, stderr strings.Builder
	start := time.Now()
	err := runner(ctx, argv, &stdout, &stderr)
	duration := time.Since(start).Milliseconds()

	result := batchResult{Command: command, Success: err == nil, DurationMS: duration}
	if err != nil {
		result.Error = err.Error()
	} else {
		result.Data = captureOutput(stdout.String(), argv)
	}
	return result
}

// runBatchItem is the default runner: it executes argv against root with
// isolated writers. When the batch item itself carries --json, the captured
// envelope is decoded into structured data.
func runBatchItem(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	root := newRootCommand()
	root.SetArgs(argv)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.ExecuteContext(ctx)
}

func captureOutput(text string, argv []string) any {
	if !hasFlag(argv, "json") {
		return strings.TrimRight(text, "\n")
	}
	var payload any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return strings.TrimRight(text, "\n")
	}
	return payload
}

func hasFlag(argv []string, name string) bool {
	for _, arg := range argv {
		if arg == "--"+name {
			return true
		}
	}
	return false
}

// tokenize splits a command string on whitespace while honouring single and
// double quotes.
func tokenize(command string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inSingle, inDouble := false, false
	started := false
	for _, r := range command {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				current.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else {
				current.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
			started = true
		case r == '"':
			inDouble = true
			started = true
		case r == ' ' || r == '\t':
			if started {
				tokens = append(tokens, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if inSingle || inDouble {
		return nil, errors.New("unbalanced quotes in command")
	}
	if started {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}

func firstToken(command string) string {
	tokens, err := tokenize(command)
	if err != nil || len(tokens) == 0 {
		return ""
	}
	return tokens[0]
}
