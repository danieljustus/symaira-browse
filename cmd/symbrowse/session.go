package main

import (
	"github.com/spf13/cobra"
)

func newSessionCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "session",
		Short:   "Inspect browser sessions",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newSessionListCommand(&session))
	command.AddCommand(newSessionInfoCommand(&session))
	command.AddCommand(newSessionIDCommand())
	return command
}

func newSessionListCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "list",
		Short: "List sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := requestNoAutostart(cmd.Context(), *session, "session.list")
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response)
		},
	}
	return command
}

func newSessionInfoCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "info",
		Short: "Show session information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := requestNoAutostart(cmd.Context(), *session, "session.info")
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response)
		},
	}
	return command
}
