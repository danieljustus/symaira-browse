package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/profiles"
)

func newProfilesCommand() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "profiles",
		Short: "List discovered Chrome profiles available for reuse",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			found := profiles.Discover()
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"profiles": found})
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
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s%s\n", profile.Name, profile.Path, defaultMark)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the stable machine-readable schema")
	return command
}
