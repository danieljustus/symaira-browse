package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newWatchCommand() *cobra.Command {
	var session, takeOverReason string
	var takeOver bool
	command := &cobra.Command{
		Use:   "watch",
		Short: "Watch an agent session: stream the action journal live (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if takeOver {
				if takeOverReason == "" {
					return fmt.Errorf("watch --take-over requires --reason")
				}
				request := map[string]any{"reason": takeOverReason, "timeout": "5m"}
				payload, _ := json.Marshal(request)
				response, err := stateRequest(cmd.Context(), session, "handoff", payload)
				if err != nil {
					return err
				}
				if !response.Success {
					return responseError(response)
				}
				raw, _ := json.MarshalIndent(response.Data, "", "  ")
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return err
			}
			return watchSession(cmd, session)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	command.Flags().BoolVar(&takeOver, "take-over", false, "switch into a regular handoff instead of watching")
	command.Flags().StringVar(&takeOverReason, "reason", "", "handoff reason (required with --take-over)")
	return command
}

// watchSession tails the session journal and streams entries to the terminal
// until interrupted. Watching is strictly read-only: no interaction frame is
// sent, so the agent session's refs stay stable (issue B-47 acceptance).
func watchSession(cmd *cobra.Command, session string) error {
	// Start at the current end of the journal so the human sees live
	// entries, not the whole history.
	var seen int
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "watching session %q (read-only; Ctrl-C to stop)\n", session)
	for {
		if ctx.Err() != nil {
			return nil
		}
		response, err := stateRequest(ctx, session, "journal.show", nil)
		if err != nil {
			// Daemon not up yet: poll quietly.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
				continue
			}
		}
		if !response.Success {
			return responseError(response)
		}
		entries := journalEntriesFromResponse(response.Data)
		for i := seen; i < len(entries); i++ {
			entry := entries[i]
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tclass=%s\tdecider=%s\t%s\n",
				entry.Timestamp, entry.Command, entry.RiskClass, entry.Decider, entry.Result)
		}
		seen = len(entries)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}
