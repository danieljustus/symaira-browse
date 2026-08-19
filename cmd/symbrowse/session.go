package main

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

func newSessionCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "session",
		Short: "Inspect browser sessions",
		Args:  cobra.NoArgs,
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
			response, err := sessionRequest(cmd.Context(), *session, "session.list")
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response, false)
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
			response, err := sessionRequest(cmd.Context(), *session, "session.info")
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response, false)
		},
	}
	return command
}

func sessionRequest(ctx context.Context, session, command string) (daemon.Response, error) {
	response, err := request(ctx, session, command, nil)
	if err != nil {
		return daemon.Response{}, err
	}
	if !response.Success && response.Error == nil {
		return daemon.Response{}, errors.New("session request failed")
	}
	return response, nil
}
