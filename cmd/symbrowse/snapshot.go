package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/injection"
)

func newSnapshotCommand() *cobra.Command {
	var session, selector, since, patternsFile string
	var interactive, compact, urls, diff, contentBoundaries, noInjectionScan bool
	var depth int
	command := &cobra.Command{
		GroupID: groupIDCore,
		Use:     "snapshot",
		Short:   "Render the accessibility tree",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request := struct {
				engine.SnapshotOptions
				NoInjectionScan   bool   `json:"no_injection_scan,omitempty"`
				InjectionPatterns string `json:"injection_patterns,omitempty"`
			}{
				SnapshotOptions:   engine.SnapshotOptions{Interactive: interactive, Compact: compact, Depth: depth, Selector: selector, URLs: urls, Diff: diff || since != "", Since: since},
				NoInjectionScan:   noInjectionScan,
				InjectionPatterns: patternsFile,
			}
			args, err := json.Marshal(request)
			if err != nil {
				return err
			}
			if noInjectionScan {
				slog.Warn("injection scan disabled by --no-injection-scan; page output is not checked for prompt-injection vectors")
			}
			response, err := requestBudget(cmd.Context(), session, "snapshot", args, maxTokensFlag(cmd))
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if contentBoundaries && !jsonOutputFlag(cmd) {
				origin, originErr := currentPageOrigin(cmd.Context(), session)
				if originErr != nil {
					return originErr
				}
				boundary, boundaryErr := injection.New(origin)
				if boundaryErr != nil {
					return boundaryErr
				}
				text := fmt.Sprint(response.Data)
				response.Data = boundary.WrapText(text)
			}
			return writeSnapshotResponse(cmd, response, diff || since != "")
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	command.Flags().BoolVarP(&interactive, "interactive", "i", false, "include only interactive nodes")
	command.Flags().BoolVarP(&compact, "compact", "c", false, "omit non-interactive structural nodes")
	command.Flags().IntVarP(&depth, "depth", "d", 0, "maximum tree depth; zero means unlimited")
	command.Flags().StringVarP(&selector, "selector", "s", "", "select an accessibility subtree")
	command.Flags().BoolVarP(&urls, "urls", "u", false, "include link URLs")
	command.Flags().BoolVar(&diff, "diff", false, "show changes since the previous snapshot")
	command.Flags().StringVar(&since, "since", "", "show changes since a specific snapshot ID")
	command.Flags().BoolVar(&contentBoundaries, "content-boundaries", false, "wrap page content in unforgeable boundary markers (default on in MCP mode)")
	command.Flags().BoolVar(&noInjectionScan, "no-injection-scan", false, "disable the prompt-injection heuristic scan (hidden text, agent-directed imperatives, aria-label mismatch, alt/title/meta/comment instructions)")
	command.Flags().StringVar(&patternsFile, "injection-patterns", "", "custom prompt-injection pattern file (one phrase per line; replaces the embedded multilingual list)")
	command.Flags().Int("max-tokens", 0, "token budget for the payload; oversized output is truncated and stored in the cache (0 = no limit)")
	return command
}

// currentPageOrigin fetches the current page URL from the daemon for use as
// the content-boundary origin.
func currentPageOrigin(ctx context.Context, session string) (string, error) {
	response, err := request(ctx, session, "get.url", nil)
	if err != nil {
		return "", err
	}
	if !response.Success {
		return "", responseError(response)
	}
	raw, err := json.Marshal(response.Data)
	if err != nil {
		return "", err
	}
	var result struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	var origin string
	if err := json.Unmarshal(result.Value, &origin); err != nil {
		return "", err
	}
	return origin, nil
}

func writeSnapshotResponse(cmd *cobra.Command, response daemon.Response, _ bool) error {
	return writeDaemonResponse(cmd, response, false)
}
