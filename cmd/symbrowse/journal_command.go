package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newJournalCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "journal",
		Short:   "Inspect the append-only action journal",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newJournalTailCommand(&session))
	command.AddCommand(newJournalShowCommand(&session))
	return command
}

func newJournalTailCommand(session *string) *cobra.Command {
	var lines int
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "tail",
		Short: "Show the last journal entries of a session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request := map[string]any{"session": *session, "lines": lines}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "journal.tail", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Data)
			}
			entries := journalEntriesFromResponse(response.Data)
			for _, entry := range entries {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", entry.Timestamp, entry.Command, entry.RiskClass, entry.Decider, entry.Result)
			}
			return nil
		},
	}
	command.Flags().IntVar(&lines, "lines", 10, "number of entries to show")
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the stable machine-readable schema")
	return command
}

func newJournalShowCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "show",
		Short: "Show the full journal of a session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request := map[string]any{"session": *session}
			payload, _ := json.Marshal(request)
			response, err := stateRequest(cmd.Context(), *session, "journal.show", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Data)
			}
			entries := journalEntriesFromResponse(response.Data)
			for _, entry := range entries {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", entry.Timestamp, entry.Command, entry.RiskClass, entry.Decider, entry.Result)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the stable machine-readable schema")
	return command
}

type journalEntryPayload struct {
	Timestamp string `json:"timestamp"`
	Command   string `json:"command"`
	RiskClass string `json:"risk_class"`
	Decider   string `json:"decider"`
	Result    string `json:"result"`
	Reason    string `json:"reason,omitempty"`
}

func journalEntriesFromResponse(data any) []journalEntryPayload {
	raw, _ := json.Marshal(data)
	var payload struct {
		Entries []journalEntryPayload `json:"entries"`
	}
	_ = json.Unmarshal(raw, &payload)
	return payload.Entries
}
