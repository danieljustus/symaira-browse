package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/output"
	"github.com/danieljustus/symaira-browse/internal/trace"
)

func newTraceCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "trace",
		Short:   "Export and replay repeatable action traces",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newTraceExportCommand(&session))
	command.AddCommand(newTraceReplayCommand(&session))
	return command
}

func newTraceExportCommand(session *string) *cobra.Command {
	var out string
	command := &cobra.Command{
		Use:   "export",
		Short: "Convert the session journal into a repeatable trace file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request := map[string]any{"session": *session}
			payload, _ := json.Marshal(request)
			response, err := daemonRequest(cmd.Context(), *session, "journal.show", payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			entries := fullJournalEntriesFromResponse(response.Data)
			file := trace.Export(entries, *session)
			if len(file.Steps) == 0 {
				return fmt.Errorf("no replayable steps in the journal of session %q", *session)
			}
			if err := trace.Write(out, file); err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelope(cmd, output.OK(map[string]any{"steps": len(file.Steps), "file": out}, nil))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "exported %d step(s) to %s\n", len(file.Steps), out)
			return err
		},
	}
	command.Flags().StringVar(&out, "out", "trace.json", "trace file to write")
	return command
}

func newTraceReplayCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "replay <file>",
		Short: "Replay a trace file step by step and report deviations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := trace.Read(args[0])
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(map[string]any{"steps": file.Steps})
			response, err := daemonRequest(cmd.Context(), *session, "trace.replay", raw)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			raw, _ = json.MarshalIndent(response.Data, "", "  ")
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
			return err
		},
	}
	return command
}

func fullJournalEntriesFromResponse(data any) []journal.Entry {
	raw, _ := json.Marshal(data)
	var payload struct {
		Entries []journal.Entry `json:"entries"`
	}
	_ = json.Unmarshal(raw, &payload)
	return payload.Entries
}
