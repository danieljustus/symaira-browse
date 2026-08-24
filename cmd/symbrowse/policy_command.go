package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newPolicyCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "policy",
		Short:   "Inspect the local risk policy",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newPolicyExplainCommand(&session))
	return command
}

func newPolicyExplainCommand(session *string) *cobra.Command {
	var url, mode string
	command := &cobra.Command{
		Use:   "explain <command>",
		Short: "Show the effective decision for a command against a URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"command": args[0], "url": url, "mode": mode}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "policy.explain", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			raw, _ := json.MarshalIndent(response.Data, "", "  ")
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return err
		},
	}
	command.Flags().StringVar(&url, "url", "", "URL whose host the rule is evaluated against")
	command.Flags().StringVar(&mode, "mode", "", "policy mode: mcp or tty (default: daemon mode)")
	return command
}
