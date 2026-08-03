package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/sessionid"
)

func newSessionIDCommand() *cobra.Command {
	var scope, prefix string
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "id",
		Short: "Derive a stable, collision-free session id from the local repository layout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := sessionid.ID(sessionid.Scope(scope), prefix, "")
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(info)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), info.ID)
			return err
		},
	}
	command.Flags().StringVar(&scope, "scope", string(sessionid.ScopeWorktree), "anchor scope: worktree, repo or cwd")
	command.Flags().StringVar(&prefix, "prefix", "", "optional id prefix (e.g. an agent name)")
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the stable machine-readable payload")
	return command
}
