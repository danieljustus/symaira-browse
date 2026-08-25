package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/output"
)

func newHandoffCommand() *cobra.Command {
	var session, timeout string
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "handoff --reason <text>",
		Short:   "Hand the session over to the human without losing it (2FA, CAPTCHA, approval)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reason, _ := cmd.Flags().GetString("reason")
			if reason == "" {
				return fmt.Errorf("handoff requires --reason")
			}
			request := map[string]any{"reason": reason}
			if timeout != "" {
				request["timeout"] = timeout
			}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), session, "handoff", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if structuredOutput(cmd) {
				return writeEnvelope(cmd, output.OK(response.Data, nil))
			}
			raw, _ := json.MarshalIndent(response.Data, "", "  ")
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return err
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	command.Flags().StringVar(&timeout, "timeout", "5m", "maximum wait before the handoff times out")
	return command
}
