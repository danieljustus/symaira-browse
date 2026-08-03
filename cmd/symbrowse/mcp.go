package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/mcp"
)

func newMCPCommand() *cobra.Command {
	var session string
	var allowPrivate bool
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP stdio server (JSON-RPC 2.0 over stdin/stdout)",
		Long: "mcp runs the Model Context Protocol stdio server. Tools proxy to the local " +
			"symbrowse daemon; every tool accepts an optional session argument. " +
			"No byte is written to stdout except JSON-RPC frames (zero stdout pollution); " +
			"all logging goes to stderr.\n\n" +
			"Security defaults in MCP mode: the daemon is started with the SSRF guard " +
			"enabled, so private and loopback targets are denied. Pass --allow-private " +
			"to permit them explicitly. The domain allowlist stays configurable through " +
			"the daemon flags and config.toml.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			server := mcp.New(mcp.Options{
				Version:      version,
				Session:      session,
				AllowPrivate: allowPrivate,
			})
			server.VerifyRunningDaemon()
			if err := server.Core().ServeStdio(cmd.Context()); err != nil {
				return fmt.Errorf("mcp server: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&session, "session", "default", "default session for tool calls without a session argument")
	command.Flags().BoolVar(&allowPrivate, "allow-private", false, "allow private and loopback targets (SSRF opt-out; MCP mode denies them by default)")
	return command
}
