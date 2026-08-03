package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

func newSnapshotCommand() *cobra.Command {
	var session, selector string
	var interactive, compact, urls, jsonOutput bool
	var depth int
	command := &cobra.Command{
		Use:   "snapshot",
		Short: "Render the accessibility tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options := engine.SnapshotOptions{Interactive: interactive, Compact: compact, Depth: depth, Selector: selector, URLs: urls}
			args, err := json.Marshal(options)
			if err != nil {
				return err
			}
			path, err := daemon.SocketPath(session)
			if err != nil {
				return err
			}
			client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
			response, err := client.Request(cmd.Context(), daemon.Frame{Cmd: "snapshot", Args: args, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
			if err != nil {
				return err
			}
			if !response.Success {
				if response.Error == nil {
					return errors.New("snapshot request failed")
				}
				return errors.New(response.Error.Message)
			}
			return writeDaemonResponse(cmd, response, jsonOutput)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	command.Flags().BoolVarP(&interactive, "interactive", "i", false, "include only interactive nodes")
	command.Flags().BoolVarP(&compact, "compact", "c", false, "omit non-interactive structural nodes")
	command.Flags().IntVarP(&depth, "depth", "d", 0, "maximum tree depth; zero means unlimited")
	command.Flags().StringVarP(&selector, "selector", "s", "", "select an accessibility subtree")
	command.Flags().BoolVarP(&urls, "urls", "u", false, "include link URLs")
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable tree and ref map")
	return command
}
