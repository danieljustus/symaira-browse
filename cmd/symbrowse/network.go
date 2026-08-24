package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newNetworkCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDNetwork,
		Use:     "network",
		Short:   "Inspect, mock and export page network activity (issue #59)",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newNetworkRequestsCommand(&session))
	command.AddCommand(newNetworkRequestCommand(&session))
	command.AddCommand(newNetworkRouteCommand(&session))
	command.AddCommand(newNetworkUnrouteCommand(&session))
	command.AddCommand(newNetworkHarCommand(&session))
	return command
}

func newNetworkRequestsCommand(session *string) *cobra.Command {
	var jsonOutput bool
	var filter, requestType, method string
	var status int
	command := &cobra.Command{
		Use:   "requests",
		Short: "List captured requests (sensitive headers masked)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := stateRequestBudget(cmd.Context(), *session, "network.requests", nil, maxTokensFlag(cmd))
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			payload, _ := response.Data.(map[string]any)
			requests, _ := payload["requests"].([]any)
			var filtered []any
			for _, entry := range requests {
				fields, _ := entry.(map[string]any)
				if filter != "" && !strings.Contains(strings.ToLower(fieldString(fields, "url")), strings.ToLower(filter)) {
					continue
				}
				if requestType != "" && !strings.EqualFold(fieldString(fields, "type"), requestType) {
					continue
				}
				if method != "" && !strings.EqualFold(fieldString(fields, "method"), method) {
					continue
				}
				if status > 0 && int(fieldFloat(fields, "status")) != status {
					continue
				}
				filtered = append(filtered, entry)
			}
			if jsonOutput {
				raw, _ := json.MarshalIndent(map[string]any{"requests": filtered, "count": len(filtered)}, "", "  ")
				_, err := fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return err
			}
			for _, entry := range filtered {
				fields, _ := entry.(map[string]any)
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", fieldString(fields, "method"), fieldString(fields, "url"), fieldString(fields, "type"))
				if err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the raw JSON payload")
	command.Flags().StringVar(&filter, "filter", "", "only URLs containing this substring")
	command.Flags().StringVar(&requestType, "type", "", "only this resource type (document, xhr, script, ...)")
	command.Flags().StringVar(&method, "method", "", "only this HTTP method")
	command.Flags().IntVar(&status, "status", 0, "only this HTTP status code")
	command.Flags().Int("max-tokens", 0, "token budget for the payload; oversized output is truncated and stored in the cache (0 = no limit)")
	return command
}

func newNetworkRequestCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "request <id>",
		Short: "Show one captured request by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, _ := json.Marshal(map[string]any{"id": args[0]})
			response, err := stateRequest(cmd.Context(), *session, "network.request", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			raw, _ := json.MarshalIndent(response.Data, "", "  ")
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the raw JSON payload")
	return command
}

func newNetworkRouteCommand(session *string) *cobra.Command {
	var abort bool
	var body string
	var status int
	command := &cobra.Command{
		Use:   "route <url>",
		Short: "Mock or abort requests matching a URL (policy-gated, MCP default deny)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			route := map[string]any{"pattern": args[0]}
			if abort {
				route["action"] = "abort"
			} else {
				route["action"] = "mock"
				route["status"] = status
				if body != "" {
					route["body"] = json.RawMessage(body)
				}
			}
			payload, _ := json.Marshal(route)
			response, err := stateRequest(cmd.Context(), *session, "network.route", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "routed %s (%s)\n", args[0], route["action"])
			return err
		},
	}
	command.Flags().BoolVar(&abort, "abort", false, "abort matching requests instead of mocking them")
	command.Flags().StringVar(&body, "body", "", "JSON response body for the mock")
	command.Flags().IntVar(&status, "status", 200, "HTTP status for the mock")
	return command
}

func newNetworkUnrouteCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "unroute [pattern]",
		Short: "Remove one route (or all routes without a pattern)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := []byte("{}")
			if len(args) == 1 {
				payload, _ = json.Marshal(map[string]any{"pattern": args[0]})
			}
			response, err := stateRequest(cmd.Context(), *session, "network.unroute", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "unrouted")
			return err
		},
	}
	return command
}

func newNetworkHarCommand(session *string) *cobra.Command {
	var content, output string
	command := &cobra.Command{
		Use:   "har start|stop",
		Short: "Start or stop HAR capture; stop prints the HAR document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, _ := json.Marshal(map[string]any{"action": args[0], "content": content})
			response, err := stateRequest(cmd.Context(), *session, "network.har", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if args[0] == "start" {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "HAR capture started")
				return err
			}
			fields, _ := response.Data.(map[string]any)
			raw, _ := json.MarshalIndent(fields["har"], "", "  ")
			if output != "" {
				return os.WriteFile(output, raw, 0o600)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return err
		},
	}
	command.Flags().StringVar(&content, "content", "none", "response body capture: all or none")
	command.Flags().StringVar(&output, "output", "", "write the HAR document to this file")
	return command
}

func fieldString(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

func fieldFloat(fields map[string]any, key string) float64 {
	value, _ := fields[key].(float64)
	return value
}
