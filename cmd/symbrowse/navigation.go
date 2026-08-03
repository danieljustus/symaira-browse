package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
)

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
	command := &cobra.Command{Use: name + " [url]", Short: "Navigate the browser", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		return writeDaemonResponse(cmd, response, false)
	}}
	command.Flags().StringVar(&session, "session", "default", "session name")
	return command
}

func newWaitCommand() *cobra.Command {
	var session, textValue, urlValue, loadValue, state string
	var milliseconds int64
	command := &cobra.Command{Use: "wait [selector]", Short: "Wait for a browser condition", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		return writeDaemonResponse(cmd, response, false)
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
		return engine.WaitCondition{}, errors.New("choose exactly one wait condition")
	}
	if len(args) == 1 {
		return engine.WaitCondition{Kind: engine.WaitSelector, Value: args[0], SelectorState: engine.SelectorState(state)}, nil
	}
	if milliseconds < 0 {
		return engine.WaitCondition{}, errors.New("--ms cannot be negative")
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
		return engine.WaitCondition{}, fmt.Errorf("invalid --load state %q", loadValue)
	}
}

func navigationRequest(ctx context.Context, session, command string, args json.RawMessage) (daemon.Response, error) {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return daemon.Response{}, err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	response, err := client.Request(ctx, daemon.Frame{Cmd: command, Args: args, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
	if err != nil {
		return daemon.Response{}, err
	}
	if !response.Success && response.Error == nil {
		return daemon.Response{}, errors.New("navigation request failed")
	}
	return response, nil
}
