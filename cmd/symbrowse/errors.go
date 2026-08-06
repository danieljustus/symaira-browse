package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newErrorsCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "errors",
		Short: "Show or clear uncaught page errors",
		Args:  cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newErrorsListCommand(&session))
	command.AddCommand(newErrorsClearCommand(&session))
	return command
}

func newErrorsListCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List uncaught exceptions with stack traces (issue #60)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := stateRequestBudget(cmd.Context(), *session, "errors.list", nil, maxTokensFlag(cmd))
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			return printErrorEntries(cmd, response.Data, jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the raw JSON payload")
	command.Flags().Int("max-tokens", 0, "token budget for the payload; oversized output is truncated and stored in the cache (0 = no limit)")
	return command
}

func newErrorsClearCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "clear",
		Short: "Clear the error buffer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := stateRequest(cmd.Context(), *session, "errors.clear", nil)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "errors cleared")
			return err
		},
	}
	return command
}

func printErrorEntries(cmd *cobra.Command, data any, jsonOutput bool) error {
	if jsonOutput {
		raw, _ := json.MarshalIndent(data, "", "  ")
		_, err := fmt.Fprintln(cmd.OutOrStdout(), string(raw))
		return err
	}
	payload, _ := data.(map[string]any)
	entries, _ := payload["entries"].([]any)
	for _, entry := range entries {
		fields, _ := entry.(map[string]any)
		text, _ := fields["text"].(string)
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), text); err != nil {
			return err
		}
		if frames, ok := fields["stacktrace"].([]any); ok {
			for _, frame := range frames {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "    at %v\n", frame); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
