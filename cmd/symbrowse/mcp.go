package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/mcp"
)

func newMCPCommand() *cobra.Command {
	var session, tools string
	var allowPrivate, listProfiles bool
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "mcp",
		Short:   "Start the MCP stdio server (JSON-RPC 2.0 over stdin/stdout)",
		Long: "mcp runs the Model Context Protocol stdio server. Tools proxy to the local " +
			"symbrowse daemon; every tool accepts an optional session argument. " +
			"No byte is written to stdout except JSON-RPC frames (zero stdout pollution); " +
			"all logging goes to stderr.\n\n" +
			"Tool profiles select the registered tools (--tools core|nav|state|network|" +
			"debug|flows|all, comma-separated combinations allowed; default core).\n\n" +
			"Security defaults in MCP mode: the daemon is started with the SSRF guard " +
			"enabled, so private and loopback targets are denied. Pass --allow-private " +
			"to permit them explicitly. The domain allowlist stays configurable through " +
			"the daemon flags and config.toml.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listProfiles {
				return writeProfileList(cmd)
			}
			server, err := mcp.New(mcp.Options{
				Version:      version,
				Session:      session,
				AllowPrivate: allowPrivate,
				Profiles:     tools,
			})
			if err != nil {
				return err
			}
			server.VerifyRunningDaemon()
			if err := server.Core().ServeStdio(cmd.Context()); err != nil {
				return fmt.Errorf("mcp server: %w", err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&session, "session", "default", "default session for tool calls without a session argument")
	command.Flags().StringVar(&tools, "tools", "core", "tool profiles to register: core|nav|state|network|debug|flows|all (comma-separated combinations allowed)")
	command.Flags().BoolVar(&allowPrivate, "allow-private", false, "allow private and loopback targets (SSRF opt-out; MCP mode denies them by default)")
	command.Flags().BoolVar(&listProfiles, "list-profiles", false, "describe every tool profile and its tool count, then exit")
	return command
}

// writeProfileList prints the profile registry. The output is a stable data
// table: one line per profile with the tool count, plus the tools per line.
func writeProfileList(cmd *cobra.Command) error {
	var builder strings.Builder
	for _, profile := range mcp.AllProfiles() {
		count := len(profile.Tools)
		fmt.Fprintf(&builder, "%-8s %2d tools  %s\n", profile.Name, count, profile.Description)
		if count > 0 {
			builder.WriteString("           tools: " + strings.Join(profile.Tools, ", ") + "\n")
		}
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), builder.String())
	return err
}
