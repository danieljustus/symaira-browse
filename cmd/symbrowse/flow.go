package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/flows"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// newFlowCommand builds the `symbrowse flow` command tree. Individual
// subcommands (validate, run, record, list) live in their own files.
func newFlowCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "flow",
		Short: "Validate, run and record declarative browser flows",
		Long: "flow manages declarative, versioned browser automation scripts " +
			"(ARCHITEKTUR.md §5.6). Flows are YAML documents with semantic finders, " +
			"hard domain constraints and op://…-only secret references.",
	}
	command.AddCommand(newFlowValidateCommand())
	command.AddCommand(newFlowRunCommand())
	return command
}

func newFlowValidateCommand() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "validate <datei>",
		Short: "Validate a flow document with line-accurate errors",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read flow %q: %w", args[0], err)
			}
			flow, err := flows.Parse(data, args[0])
			if err != nil {
				var validateErr *flows.ValidateError
				if errors.As(err, &validateErr) {
					envelope := output.Failure(string(output.CodeValidation), "flow validation failed")
					envelope.Error.Details = map[string]any{"errors": validateErr.Errors}
					_ = writeEnvelope(cmd, envelope)
					return exitcodes.Wrapf(nil, output.ExitCodeFromCode(output.CodeValidation), exitcodes.KindValidation, "flow validation failed (%d error(s))", len(validateErr.Errors))
				}
				return err
			}
			result := map[string]any{
				"valid":     true,
				"name":      flow.Name,
				"version":   flow.Version,
				"steps":     len(flow.Steps),
				"domains":   flow.Domains,
				"source":    flow.Source,
				"schema":    "docs/flow-schema.json",
				"schema_id": "https://symaira.dev/schemas/symbrowse-flow.json",
			}
			if jsonOutput {
				return writeEnvelope(cmd, output.OK(result, nil))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "valid: %s (version %d, %d steps, domains %v)\n", flow.Name, flow.Version, len(flow.Steps), flow.Domains)
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable validation payload")
	return command
}
