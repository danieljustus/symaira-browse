package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// sendSimpleFrame sends one daemon frame and writes the envelope response.
func sendSimpleFrame(cmd *cobra.Command, session, command string, args any) error {
	client := daemon.NewClient(daemon.ClientOptions{Session: session})
	raw := marshalArgs(args)
	response, err := client.Request(cmd.Context(), daemon.Frame{Cmd: command, Args: raw, Session: session})
	if err != nil {
		return err
	}
	if !response.Success {
		return responseError(response)
	}
	return writeEnvelope(cmd, output.OK(response.Data, nil))
}

// marshalArgs encodes args for a daemon frame; nil becomes an empty payload.
func marshalArgs(args any) []byte {
	if args == nil {
		return nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil
	}
	return raw
}

func newTabCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "tab",
		Short: "Manage session tabs (list, new, switch, close)",
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")

	list := &cobra.Command{
		Use:   "list",
		Short: "List tabs of the session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendSimpleFrame(cmd, session, "tab.list", nil)
		},
	}
	newTab := &cobra.Command{
		Use:   "new [url]",
		Short: "Open a new tab, optionally at a URL",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := ""
			if len(args) == 1 {
				url = args[0]
			}
			label, _ := cmd.Flags().GetString("label")
			return sendSimpleFrame(cmd, session, "tab.new", map[string]any{"label": label, "url": url})
		},
	}
	newTab.Flags().String("label", "", "tab label for later switching")
	switchTab := &cobra.Command{
		Use:   "switch <t1|label>",
		Short: "Activate a tab by id or label (refs stay valid per tab)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendSimpleFrame(cmd, session, "tab.switch", map[string]any{"tab": args[0]})
		},
	}
	closeTab := &cobra.Command{
		Use:   "close [t1|label]",
		Short: "Close a tab (default: the active one)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			return sendSimpleFrame(cmd, session, "tab.close", map[string]any{"tab": target})
		},
	}
	windowNew := &cobra.Command{
		Use:   "window new",
		Short: "Open a new window (new tab in a fresh browser context)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendSimpleFrame(cmd, session, "window.new", nil)
		},
	}
	window := &cobra.Command{Use: "window", Short: "Window operations"}
	window.AddCommand(windowNew)

	command.AddCommand(list, newTab, switchTab, closeTab, window)
	return command
}

func newFrameCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "frame",
		Short: "Address nested frames (tree, select, main)",
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")
	tree := &cobra.Command{
		Use:   "tree",
		Short: "Show the nested frame tree of the active tab",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendSimpleFrame(cmd, session, "frame.tree", nil)
		},
	}
	main := &cobra.Command{
		Use:   "main",
		Short: "Address the main frame again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendSimpleFrame(cmd, session, "frame.main", nil)
		},
	}
	selectFrame := &cobra.Command{
		Use:   "select <frame-id>",
		Short: "Address a nested frame by its frame id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendSimpleFrame(cmd, session, "frame.select", map[string]any{"frame": args[0]})
		},
	}
	command.AddCommand(tree, main, selectFrame)
	return command
}

func newDialogCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "dialog",
		Short: "Handle JavaScript dialogs (accept, dismiss, status, auto)",
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the pending dialog state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendSimpleFrame(cmd, session, "dialog.status", nil)
		},
	}
	accept := &cobra.Command{
		Use:   "accept [text]",
		Short: "Accept the pending dialog (text for prompt dialogs)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) == 1 {
				text = args[0]
			}
			return sendSimpleFrame(cmd, session, "dialog.accept", map[string]any{"text": text})
		},
	}
	dismiss := &cobra.Command{
		Use:   "dismiss",
		Short: "Dismiss the pending dialog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sendSimpleFrame(cmd, session, "dialog.dismiss", nil)
		},
	}
	auto := &cobra.Command{
		Use:   "auto <accept|dismiss|off>",
		Short: "Configure automatic dialog handling (default: dismiss)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sendSimpleFrame(cmd, session, "dialog.auto", map[string]any{"mode": args[0]})
		},
	}
	command.AddCommand(status, accept, dismiss, auto)
	return command
}
