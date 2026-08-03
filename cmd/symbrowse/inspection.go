package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
)

func newInspectionCommands() []*cobra.Command {
	return []*cobra.Command{newGetCommand(), newIsCommand()}
}

func newGetCommand() *cobra.Command {
	var session string
	command := &cobra.Command{Use: "get", Short: "Inspect page and element values"}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	for _, kind := range []engine.InspectionKind{engine.InspectText, engine.InspectHTML, engine.InspectValue, engine.InspectAttr, engine.InspectTitle, engine.InspectURL, engine.InspectCount, engine.InspectBox, engine.InspectStyles} {
		command.AddCommand(newInspectionLeafCommand("get", kind, &session, false))
	}
	return command
}

func newIsCommand() *cobra.Command {
	var session string
	command := &cobra.Command{Use: "is", Short: "Check page and element state"}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	for _, kind := range []engine.InspectionKind{engine.InspectVisible, engine.InspectEnabled, engine.InspectChecked} {
		command.AddCommand(newInspectionLeafCommand("is", kind, &session, true))
	}
	return command
}

func newInspectionLeafCommand(group string, kind engine.InspectionKind, session *string, stateCheck bool) *cobra.Command {
	var jsonOutput bool
	use := string(kind) + " <selector>"
	if kind == engine.InspectTitle || kind == engine.InspectURL {
		use = string(kind) + " [selector]"
	}
	if kind == engine.InspectAttr {
		use = "attr <selector> <attribute>"
	}
	if kind == engine.InspectStyles {
		use = "styles <selector> [property...]"
	}
	command := &cobra.Command{
		Use:   use,
		Short: "Inspect " + string(kind),
		Args: func(_ *cobra.Command, args []string) error {
			if kind == engine.InspectTitle || kind == engine.InspectURL {
				if len(args) > 1 {
					return fmt.Errorf("get %s accepts at most one selector", kind)
				}
				return nil
			}
			if kind == engine.InspectAttr {
				if len(args) != 2 {
					return errors.New("get attr requires a selector and attribute name")
				}
				return nil
			}
			if kind == engine.InspectStyles {
				if len(args) < 1 {
					return errors.New("get styles requires a selector")
				}
				return nil
			}
			if len(args) != 1 {
				return fmt.Errorf("%s %s requires a selector", group, kind)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			request := engine.InspectionRequest{Kind: kind}
			if len(args) > 0 {
				request.Selector = args[0]
			}
			if kind == engine.InspectAttr {
				request.Attribute = args[1]
			}
			if kind == engine.InspectStyles && len(args) > 1 {
				request.Properties = args[1:]
			}
			response, err := inspectionRequest(cmd, *session, group, request)
			if err != nil {
				return err
			}
			return writeInspectionResponse(cmd, response, jsonOutput, stateCheck)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable inspection result")
	return command
}

func inspectionRequest(cmd *cobra.Command, session, group string, request engine.InspectionRequest) (daemon.Response, error) {
	args, err := json.Marshal(request)
	if err != nil {
		return daemon.Response{}, err
	}
	path, err := daemon.SocketPath(session)
	if err != nil {
		return daemon.Response{}, err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	response, err := client.Request(cmd.Context(), daemon.Frame{Cmd: group + "." + string(request.Kind), Args: args, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
	if err != nil {
		return daemon.Response{}, err
	}
	if !response.Success && response.Error == nil {
		return daemon.Response{}, errors.New("inspection request failed")
	}
	return response, nil
}

func writeInspectionResponse(cmd *cobra.Command, response daemon.Response, jsonOutput, stateCheck bool) error {
	if !response.Success {
		if response.Error == nil {
			return errors.New("inspection request failed")
		}
		return errors.New(response.Error.Message)
	}
	if jsonOutput {
		return writeDaemonResponse(cmd, response, true)
	}
	var result struct {
		Kind  engine.InspectionKind `json:"kind"`
		Value json.RawMessage       `json:"value"`
	}
	raw, err := json.Marshal(response.Data)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode inspection result: %w", err)
	}
	var value any
	if err := json.Unmarshal(result.Value, &value); err != nil {
		return fmt.Errorf("decode inspection value: %w", err)
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), value); err != nil {
		return err
	}
	if stateCheck {
		matched, ok := value.(bool)
		if !ok {
			return errors.New("state inspection returned a non-boolean value")
		}
		if !matched {
			return exitcodes.Wrap(nil, exitcodes.ExitGeneric, exitcodes.KindValidation, fmt.Sprintf("state %s is false", result.Kind))
		}
	}
	return nil
}
