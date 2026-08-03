package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/flows"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// newFlowRunCommand builds `symbrowse flow run <name> --input k=v …`.
func newFlowRunCommand() *cobra.Command {
	var inputs []string
	var dryRun bool
	var session string
	command := &cobra.Command{
		Use:   "run <name>",
		Short: "Execute a flow step by step (assertions are hard abort conditions)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := findFlowFile(args[0])
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read flow %q: %w", path, err)
			}
			flow, err := flows.Parse(data, path)
			if err != nil {
				return err
			}
			inputMap, err := parseFlowInputs(inputs)
			if err != nil {
				return err
			}
			client := daemon.NewClient(daemon.ClientOptions{Session: session})
			executor := func(ctx context.Context, frame daemon.Frame) (daemon.Response, error) {
				return client.Request(ctx, frame)
			}
			report, err := flows.Run(cmd.Context(), executor, flows.RunOptions{
				Flow:    flow,
				Inputs:  inputMap,
				Session: session,
				DryRun:  dryRun,
			})
			if err != nil {
				var runErr *flows.RunError
				if errors.As(err, &runErr) {
					envelope := output.Failure(string(output.CodeFlowFailed), "flow run failed")
					details := map[string]any{
						"step_index": runErr.StepIndex,
						"action":     runErr.Action,
						"message":    runErr.Message,
						"hint":       runErr.Hint,
					}
					if runErr.Diagnosis != nil {
						details["diagnosis"] = runErr.Diagnosis
					}
					envelope.Error.Details = details
					_ = writeEnvelope(cmd, envelope)
					return err
				}
				return err
			}
			return writeEnvelope(cmd, output.OK(report, nil))
		},
	}
	command.Flags().StringArrayVar(&inputs, "input", nil, "flow input as k=v (repeatable)")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "print the execution plan with risk classes without executing")
	command.Flags().StringVar(&session, "session", "default", "daemon session name")
	return command
}

// parseFlowInputs converts --input k=v entries into a map.
func parseFlowInputs(inputs []string) (map[string]string, error) {
	result := make(map[string]string, len(inputs))
	for _, entry := range inputs {
		key, value, found := strings.Cut(entry, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid --input %q (expected k=v)", entry)
		}
		result[strings.TrimSpace(key)] = value
	}
	return result, nil
}

// findFlowFile resolves a flow name to a file using the same precedence as
// discovery: project-local ./.symbrowse/flows, then the global flows
// directory.
func findFlowFile(name string) (string, error) {
	if filepath.Ext(name) == ".yaml" || filepath.Ext(name) == ".yml" {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}
	candidates := []string{
		filepath.Join(".symbrowse", "flows", name+".yaml"),
		filepath.Join(".symbrowse", "flows", name+".yml"),
	}
	home, err := os.UserHomeDir()
	if err == nil {
		global := filepath.Join(home, ".config", "symbrowse", "flows")
		candidates = append(candidates,
			filepath.Join(global, name+".yaml"),
			filepath.Join(global, name+".yml"),
		)
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("flow %q not found (looked in ./.symbrowse/flows and ~/.config/symbrowse/flows)", name)
}
