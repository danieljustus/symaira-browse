package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/output"
)

// Issue #60: JavaScript evaluation command.
func newEvalCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "eval <expression>",
		Short:   "Execute JavaScript in the active page",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			expression, err := evalExpression(cmd, args)
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"expression": expression})
			response, err := daemonRequest(cmd.Context(), session, "eval", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			return printEvalResult(cmd, response.Data)
		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.Flags().BoolP("base64", "b", false, "expression is base64-encoded")
	command.Flags().Bool("stdin", false, "read the expression from stdin")
	return command
}

// evalExpression resolves the expression from args, stdin or base64.
func evalExpression(cmd *cobra.Command, args []string) (string, error) {
	fromStdin, _ := cmd.Flags().GetBool("stdin")
	encoded, _ := cmd.Flags().GetBool("base64")
	expression := ""
	if fromStdin {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read expression from stdin: %w", err)
		}
		expression = string(raw)
	} else if len(args) > 0 {
		expression = args[0]
	} else {
		return "", invalidArgs("eval requires an expression argument (or --stdin)")
	}
	if encoded {
		raw, err := base64.StdEncoding.DecodeString(expression)
		if err != nil {
			return "", invalidArgs("decode base64 expression: %v", err)
		}
		expression = string(raw)
	}
	return expression, nil
}

// printEvalResult prints the evaluation result; an uncaught exception is
// reported as an error with its text.
func printEvalResult(cmd *cobra.Command, data any) error {
	if structuredOutput(cmd) {
		return writeEnvelope(cmd, output.OK(data, nil))
	}
	payload, _ := data.(map[string]any)
	if exception, _ := payload["exception_text"].(string); exception != "" {
		return fmt.Errorf("eval threw: %s", exception)
	}
	value, ok := payload["value"]
	if !ok || value == nil {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "undefined")
		return err
	}
	raw, _ := json.Marshal(value)
	if string(raw) == "null" {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "null")
		return err
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), string(raw))
	return err
}
