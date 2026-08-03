package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newAuthCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "auth",
		Short: "Credential management through symvault (no plaintext)",
		Args:  cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newAuthLoginCommand(&session))
	return command
}

func newAuthLoginCommand(session *string) *cobra.Command {
	var url string
	command := &cobra.Command{
		Use:   "login <vault-entry>",
		Short: "Resolve a vault entry and type the credentials into the detected login form",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"entry": args[0]}
			if url != "" {
				request["url"] = url
			}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "auth.login", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "credentials entered; press enter or submit the form to log in")
			return err
		},
	}
	command.Flags().StringVar(&url, "url", "", "navigate to this URL before detecting the login form")
	return command
}
