package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/output"
	"github.com/danieljustus/symaira-browse/internal/sessionid"
)

func newSessionIDCommand() *cobra.Command {
	var scope, prefix string
	command := &cobra.Command{
		Use:   "id",
		Short: "Derive a stable, collision-free session id from the local repository layout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := sessionid.ID(sessionid.Scope(scope), prefix, "")
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelope(cmd, output.OK(info, nil))
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), info.ID)
			return err
		},
	}
	command.Flags().StringVar(&scope, "scope", string(sessionid.ScopeWorktree), "anchor scope: worktree, repo or cwd")
	command.Flags().StringVar(&prefix, "prefix", "", "optional id prefix (e.g. an agent name)")
	return command
}
