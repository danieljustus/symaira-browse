package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// newA11yCommand builds `symbrowse a11y [url]` running an axe-core audit.
func newA11yCommand() *cobra.Command {
	var tags string
	var selector string
	var session string
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "a11y [url]",
		Short:   "Run an axe-core accessibility audit on the current page",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var tagList []string
			if strings.TrimSpace(tags) != "" {
				for _, tag := range strings.Split(tags, ",") {
					if trimmed := strings.TrimSpace(tag); trimmed != "" {
						tagList = append(tagList, trimmed)
					}
				}
			}
			argsPayload := map[string]any{"tags": tagList, "selector": selector}
			if len(args) == 1 {
				// Navigate first when a URL is given, then audit.
				client := daemon.NewClient(daemon.ClientOptions{Session: session})
				if _, err := client.Request(cmd.Context(), daemon.Frame{
					Cmd: "open", Args: marshalArgs(map[string]any{"url": args[0]}), Session: session,
				}); err != nil {
					return err
				}
			}
			return sendSimpleFrame(cmd, session, "a11y", argsPayload)
		},
	}
	command.Flags().StringVar(&tags, "tags", "", "comma-separated WCAG tags (e.g. wcag2a,wcag2aa)")
	command.Flags().StringVar(&selector, "selector", "", "restrict the audit to a CSS selector")
	command.Flags().StringVar(&session, "session", "default", "daemon session name")
	return command
}
