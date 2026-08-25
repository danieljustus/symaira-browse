package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
)

func navigationShort(name string) string {
	switch name {
	case "open":
		return "Open a URL in the browser and wait for load"
	case "goto":
		return "Navigate to a URL (alias for open)"
	case "back":
		return "Navigate back in page history"
	case "forward":
		return "Navigate forward in page history"
	case "reload":
		return "Reload the current page"
	default:
		return "Navigate the browser"
	}
}

func newNavigationCommands() []*cobra.Command {
	return []*cobra.Command{
		newNavigateCommand("open"),
		newNavigateCommand("goto"),
		newNavigateCommand("back"),
		newNavigateCommand("forward"),
		newNavigateCommand("reload"),
		newWaitCommand(),
	}
}

func newNavigateCommand(name string) *cobra.Command {
	var session string
	groupID := groupIDNav
	if name == "open" || name == "goto" {
		groupID = groupIDCore
	}
	command := &cobra.Command{
		GroupID: groupID,
		Use:     name + " [url]",
		Short:   navigationShort(name),
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (name == "open" || name == "goto") && len(args) != 1 {
				return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "navigation URL is required")
			}
			if name != "open" && name != "goto" && len(args) != 0 {
				return exitcodes.Wrapf(nil, exitcodes.ExitNoInput, exitcodes.KindValidation, "this navigation command does not accept arguments")
			}
			var raw json.RawMessage
			if len(args) == 1 {
				raw, _ = json.Marshal(map[string]string{"url": args[0]})
			}
			response, err := navigationRequest(cmd.Context(), session, name, raw)
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	return command
}

func newWaitCommand() *cobra.Command {
	var session, textValue, urlValue, loadValue, state string
	var milliseconds int64
	command := &cobra.Command{
		GroupID: groupIDCore,
		Use:     "wait [selector]",
		Short:   "Wait for a browser condition", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			condition, err := waitConditionFromFlags(args, milliseconds, textValue, urlValue, loadValue, state)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(condition)
			if err != nil {
				return err
			}
			response, err := navigationRequest(cmd.Context(), session, "wait", raw)
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response)
		}}
	command.Flags().StringVar(&session, "session", "default", "session name")
	command.Flags().Int64Var(&milliseconds, "ms", 0, "wait for milliseconds")
	command.Flags().StringVar(&textValue, "text", "", "wait for text")
	command.Flags().StringVar(&urlValue, "url", "", "wait for a URL glob")
	command.Flags().StringVar(&loadValue, "load", "", "wait for load, domcontentloaded, or networkidle")
	command.Flags().StringVar(&state, "state", "visible", "selector state: visible, hidden, attached, or detached")
	return command
}

func waitConditionFromFlags(args []string, milliseconds int64, textValue, urlValue, loadValue, state string) (engine.WaitCondition, error) {
	selected := 0
	if len(args) == 1 {
		selected++
	}
	if milliseconds != 0 {
		selected++
	}
	if textValue != "" {
		selected++
	}
	if urlValue != "" {
		selected++
	}
	if loadValue != "" {
		selected++
	}
	if selected != 1 {
		return engine.WaitCondition{}, invalidArgs("wait requires exactly one condition")
	}
	if len(args) == 1 {
		return engine.WaitCondition{Kind: engine.WaitSelector, Value: args[0], SelectorState: engine.SelectorState(state)}, nil
	}
	if milliseconds < 0 {
		return engine.WaitCondition{}, invalidArgs("--ms cannot be negative")
	}
	if milliseconds != 0 {
		return engine.WaitCondition{Kind: engine.WaitMilliseconds, Duration: time.Duration(milliseconds) * time.Millisecond}, nil
	}
	if textValue != "" {
		return engine.WaitCondition{Kind: engine.WaitText, Value: textValue}, nil
	}
	if urlValue != "" {
		return engine.WaitCondition{Kind: engine.WaitURL, Value: urlValue}, nil
	}
	switch engine.LoadState(loadValue) {
	case engine.LoadComplete, engine.LoadDOMContentLoad, engine.LoadNetworkIdle:
		return engine.WaitCondition{Kind: engine.WaitLoad, LoadState: engine.LoadState(loadValue)}, nil
	default:
		return engine.WaitCondition{}, invalidArgs("invalid --load state %q", loadValue)
	}
}

func navigationRequest(ctx context.Context, session, command string, args json.RawMessage) (daemon.Response, error) {
	response, err := daemonRequest(ctx, session, command, args)
	if err != nil {
		return daemon.Response{}, err
	}
	if !response.Success && response.Error == nil {
		return daemon.Response{}, errors.New("navigation request failed")
	}
	return response, nil
}
