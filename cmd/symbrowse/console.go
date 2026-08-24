package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/output"
)

func newConsoleCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "console",
		Short:   "Show or clear the page console buffer",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newConsoleListCommand(&session))
	command.AddCommand(newConsoleClearCommand(&session))
	return command
}

func newConsoleListCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "list",
		Short: "List captured console messages (issue #60)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := stateRequestBudget(cmd.Context(), *session, "console.list", nil, maxTokensFlag(cmd))
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			return printConsoleEntries(cmd, response.Data, jsonOutputFlag(cmd))
		},
	}
	command.Flags().Int("max-tokens", 0, "token budget for the payload; oversized output is truncated and stored in the cache (0 = no limit)")
	return command
}

func newConsoleClearCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "clear",
		Short: "Clear the console buffer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := stateRequest(cmd.Context(), *session, "console.clear", nil)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "console cleared")
			return err
		},
	}
	return command
}

func printConsoleEntries(cmd *cobra.Command, data any, jsonOutput bool) error {
	if jsonOutput {
		return writeEnvelope(cmd, output.OK(data, nil))
	}
	payload, _ := data.(map[string]any)
	entries, _ := payload["entries"].([]any)
	for _, entry := range entries {
		fields, _ := entry.(map[string]any)
		entryType, _ := fields["type"].(string)
		text, _ := fields["text"].(string)
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", entryType, text); err != nil {
			return err
		}
	}
	return nil
}
