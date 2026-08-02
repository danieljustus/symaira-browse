package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is replaced at build time by the Makefile's VERSION value.
var version = "dev"

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "symbrowse",
		Short:         "Agent-operable browser automation over Chrome DevTools Protocol",
		Long:          "symbrowse is the standalone command-line entrypoint for Symaira Browse.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCommand())
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the symbrowse version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "symbrowse %s\n", version)
			return err
		},
	}
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
