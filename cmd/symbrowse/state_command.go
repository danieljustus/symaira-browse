package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func newStateCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "state",
		Short: "Save, restore and manage named browser session states",
		Args:  cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newStateSaveCommand(&session))
	command.AddCommand(newStateLoadCommand(&session))
	command.AddCommand(newStateListCommand(&session))
	command.AddCommand(newStateShowCommand(&session))
	command.AddCommand(newStateClearCommand(&session))
	command.AddCommand(newStateCleanCommand(&session))
	return command
}

func newStateSaveCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "save <name>",
		Short: "Capture cookies and web storage into a named state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"name": args[0]}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "state.save", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "saved state %q\n", args[0])
			return err
		},
	}
	return command
}

func newStateLoadCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "load <name>",
		Short: "Restore cookies and web storage from a named state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"name": args[0]}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "state.load", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "loaded state %q\n", args[0])
			return err
		},
	}
	return command
}

func newStateListCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List named states",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := stateRequest(cmd.Context(), *session, "state.list", nil)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Data)
			}
			payload := stateListFromResponse(response.Data)
			for _, name := range payload.States {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), name)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the stable machine-readable schema")
	return command
}

func newStateShowCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "show <name>",
		Short: "Show state metadata (origins, counts, age) without values",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"name": args[0]}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "state.show", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Data)
			}
			raw, _ := json.MarshalIndent(response.Data, "", "  ")
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the stable machine-readable schema")
	return command
}

func newStateClearCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "clear <name>",
		Short: "Delete one named state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"name": args[0]}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "state.clear", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "cleared state %q\n", args[0])
			return err
		},
	}
	return command
}

func newStateCleanCommand(session *string) *cobra.Command {
	var olderThan string
	command := &cobra.Command{
		Use:   "clean",
		Short: "Remove expired states (or states older than --older-than days)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request := map[string]any{}
			if olderThan != "" {
				days, err := parseDays(olderThan)
				if err != nil {
					return err
				}
				request["older_than_days"] = days
			}
			var payload []byte
			if len(request) > 0 {
				payload, _ = json.Marshal(request)
			}
			response, err := stateRequest(cmd.Context(), *session, "state.clean", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			cleanPayload := stateCleanFromResponse(response.Data)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %d expired state(s)\n", len(cleanPayload.Removed))
			return err
		},
	}
	command.Flags().StringVar(&olderThan, "older-than", "", "remove states saved more than this many days ago")
	return command
}

type stateListPayload struct {
	SchemaVersion int      `json:"schema_version"`
	States        []string `json:"states"`
}

func stateListFromResponse(data any) stateListPayload {
	raw, _ := json.Marshal(data)
	var payload stateListPayload
	_ = json.Unmarshal(raw, &payload)
	return payload
}

type stateCleanPayload struct {
	Removed []string `json:"removed"`
}

func stateCleanFromResponse(data any) stateCleanPayload {
	raw, _ := json.Marshal(data)
	var payload stateCleanPayload
	_ = json.Unmarshal(raw, &payload)
	return payload
}

// parseDays converts a --older-than argument into a duration. The flag is
// accepted for interface compatibility with the plan; the store's own expiry
// window (SYMBROWSE_STATE_EXPIRE_DAYS, default 30) governs retention.
func parseDays(value string) (int, error) {
	days, err := strconv.Atoi(value)
	if err != nil || days < 0 {
		return 0, fmt.Errorf("invalid day count %q", value)
	}
	return days, nil
}
