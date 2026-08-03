package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/flows"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// newFlowListCommand builds `symbrowse flow list` showing every discovered
// flow with its origin.
func newFlowListCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "list",
		Short: "List discovered flows with their origin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			found, err := flows.Discover(flows.DiscoveryOptions{})
			if err != nil {
				return err
			}
			return writeEnvelope(cmd, output.OK(map[string]any{"flows": found}, nil))
		},
	}
	return command
}
