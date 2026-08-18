package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/flows"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// newFlowRecordCommand builds `symbrowse flow record start|stop|status`.
func newFlowRecordCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "record <start|stop|status>",
		Short: "Record a session into a flow draft",
		Long: "flow record start|stop captures the actions of a session and " +
			"generates a flow draft: concrete values become {{input_N}} references, " +
			"secret-looking values become op://… placeholders, session refs are " +
			"converted to semantic selectors, and observed end states become " +
			"assert steps. The draft is printed for human review.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := daemon.NewClient(daemon.ClientOptions{Session: session})
			response, err := client.Request(cmd.Context(), daemon.Frame{
				Cmd:     "flow.record." + args[0],
				Session: session,
			})
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if args[0] != "stop" {
				return writeEnvelope(cmd, output.OK(response.Data, nil))
			}
			return writeFlowDraft(cmd, response)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "daemon session name")
	return command
}

// writeFlowDraft renders the recorded actions into a flow draft document.
func writeFlowDraft(cmd *cobra.Command, response daemon.Response) error {
	raw, err := json.Marshal(response.Data)
	if err != nil {
		return err
	}
	var payload struct {
		Actions []daemon.RecordedAction `json:"actions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	actions := make([]flows.RecordedAction, 0, len(payload.Actions))
	for _, action := range payload.Actions {
		actions = append(actions, flows.RecordedAction{
			Index:      action.Index,
			Command:    action.Command,
			Selector:   action.Selector,
			Value:      action.Value,
			Role:       action.Role,
			Name:       action.Name,
			InputType:  action.InputType,
			Autocomplete: action.Autocomplete,
		})
	}
	draft, err := flows.GenerateDraft(actions, nil)
	if err != nil {
		return err
	}
	yaml, err := draft.RenderYAML()
	if err != nil {
		return err
	}
	if jsonOutputFlag(cmd) {
		envelope := output.OK(map[string]any{
			"recording":   false,
			"name":        draft.Name,
			"inputs":      draft.Inputs,
			"domains":     draft.Domains,
			"secret_refs": draft.SecretRefs,
			"steps":       len(draft.Steps),
			"draft":       string(yaml),
		}, nil)
		return writeEnvelope(cmd, envelope)
	}
	_, err = fmt.Fprint(cmd.OutOrStdout(), string(yaml))
	return err
}
