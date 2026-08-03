package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

func newFindCommand() *cobra.Command {
	var session, name string
	var exact bool
	command := &cobra.Command{
		Use:   "find <role|text|label|placeholder|alt|title|testid> <query> <action> [value]",
		Short: "Find an element semantically and optionally act on it",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) < 3 || len(args) > 4 {
				return errors.New("find requires kind, query, action, and an optional value")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			request := engine.FindRequest{Kind: engine.FinderKind(args[0]), Query: args[1], Action: engine.FinderAction(args[2]), Name: name, Exact: exact}
			if len(args) == 4 {
				if request.Action == engine.FindNth {
					index, err := strconv.Atoi(args[3])
					if err != nil {
						return fmt.Errorf("find nth index: %w", err)
					}
					request.Index = index
				} else {
					request.Value = args[3]
				}
			}
			response, err := findRequest(cmd, session, request)
			if err != nil {
				return err
			}
			return writeFindResponse(cmd, response)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	command.Flags().StringVar(&name, "name", "", "filter by accessible name")
	command.Flags().BoolVar(&exact, "exact", false, "require exact matching")
	return command
}

func findRequest(cmd *cobra.Command, session string, request engine.FindRequest) (daemon.Response, error) {
	args, err := json.Marshal(request)
	if err != nil {
		return daemon.Response{}, err
	}
	path, err := daemon.SocketPath(session)
	if err != nil {
		return daemon.Response{}, err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	response, err := client.Request(cmd.Context(), daemon.Frame{Cmd: "find", Args: args, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
	if err != nil {
		return daemon.Response{}, err
	}
	if !response.Success {
		if response.Error == nil {
			return daemon.Response{}, errors.New("find request failed")
		}
		return response, nil
	}
	return response, nil
}

func writeFindResponse(cmd *cobra.Command, response daemon.Response) error {
	if !response.Success {
		return responseError(response)
	}
	if jsonOutputFlag(cmd) {
		return writeDaemonResponse(cmd, response, false)
	}
	var result engine.FindResult
	raw, err := json.Marshal(response.Data)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode find result: %w", err)
	}
	if result.Action == engine.FindTextAction {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Value)
	} else {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "@"+result.Ref)
	}
	return err
}
