package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/output"
	"github.com/danieljustus/symaira-browse/internal/profiles"
)

func newProfilesCommand() *cobra.Command {
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "profiles",
		Short:   "List discovered Chrome profiles available for reuse",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			found := profiles.Discover()
			if jsonOutputFlag(cmd) {
				return writeEnvelope(cmd, output.OK(map[string]any{"profiles": found}, nil))
			}
			if len(found) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "no Chrome profiles found")
				return err
			}
			for _, profile := range found {
				defaultMark := ""
				if profile.IsDefault {
					defaultMark = " (default)"
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s	%s%s\n", profile.Name, profile.Path, defaultMark)
			}
			return nil
		},
	}
	return command
}
