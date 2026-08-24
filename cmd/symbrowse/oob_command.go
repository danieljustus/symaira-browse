package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newOOBCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "oob",
		Short:   "Inspect the out-of-band human channel",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newOOBStatusCommand(&session))
	return command
}

func newOOBStatusCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show whether an out-of-band prompt is pending",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := stateRequest(cmd.Context(), *session, "oob.status", nil)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Data)
			}
			payload := oobStatusFromResponse(response.Data)
			if !payload.Active {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "no pending oob prompt")
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", payload.Prompt.ID, payload.Prompt.Kind, payload.Prompt.Reason)
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the stable machine-readable schema")
	return command
}

type oobStatusPayload struct {
	Active bool `json:"active"`
	Prompt struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Reason string `json:"reason"`
		Status string `json:"status"`
	} `json:"prompt"`
}

func oobStatusFromResponse(data any) oobStatusPayload {
	raw, _ := json.Marshal(data)
	var payload oobStatusPayload
	_ = json.Unmarshal(raw, &payload)
	return payload
}
