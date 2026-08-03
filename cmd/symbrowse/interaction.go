package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

func newInteractionCommands() []*cobra.Command {
	actions := []engine.InteractionAction{engine.ActionClick, engine.ActionDoubleClick, engine.ActionFill, engine.ActionType, engine.ActionPress, engine.ActionHover, engine.ActionFocus, engine.ActionSelect, engine.ActionCheck, engine.ActionUncheck, engine.ActionScroll, engine.ActionScrollIntoView}
	commands := make([]*cobra.Command, 0, len(actions))
	for _, action := range actions {
		commands = append(commands, newInteractionCommand(action))
	}
	return commands
}

func newInteractionCommand(action engine.InteractionAction) *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   string(action) + " <selector> [value]",
		Short: "Perform a trusted interaction",
		Args: func(cmd *cobra.Command, args []string) error {
			max := 1
			if action == engine.ActionFill || action == engine.ActionType || action == engine.ActionPress || action == engine.ActionSelect || action == engine.ActionScroll {
				max = 2
			}
			if len(args) < 1 || len(args) > max {
				return fmt.Errorf("%s requires a selector and optional value", action)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			request := engine.InteractionRequest{Action: action, Selector: args[0]}
			if len(args) == 2 {
				switch action {
				case engine.ActionScroll:
					amount, err := strconv.ParseInt(args[1], 10, 64)
					if err != nil {
						return fmt.Errorf("scroll amount: %w", err)
					}
					request.Amount = amount
				case engine.ActionPress:
					request.Key = args[1]
				default:
					request.Value = args[1]
				}
			}
			payload, err := json.Marshal(request)
			if err != nil {
				return err
			}
			path, err := daemon.SocketPath(session)
			if err != nil {
				return err
			}
			client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
			response, err := client.Request(cmd.Context(), daemon.Frame{Cmd: string(action), Args: payload, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			return writeDaemonResponse(cmd, response, false)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	return command
}
