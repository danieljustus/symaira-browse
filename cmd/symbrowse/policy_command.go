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
			response, err := daemonRequest(cmd.Context(), *session, "policy.explain", payload)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			data, _ := response.Data.(map[string]any)
			explanation, _ := data["explanation"].(string)
			if explanation == "" {
				raw, marshalErr := json.MarshalIndent(response.Data, "", "  ")
				if marshalErr != nil {
					return marshalErr
				}
				explanation = string(raw)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), explanation)
			return err
		},
	}
	command.Flags().StringVar(&url, "url", "", "URL whose host the rule is evaluated against")
	command.Flags().StringVar(&mode, "mode", "", "policy mode: mcp or tty (default: daemon mode)")
	return command
}
