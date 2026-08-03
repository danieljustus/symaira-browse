package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

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
		Use:   "snapshot",
		Short: "Render the accessibility tree",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options := engine.SnapshotOptions{Interactive: interactive, Compact: compact, Depth: depth, Selector: selector, URLs: urls, Diff: diff || since != "", Since: since}
			args, err := json.Marshal(options)
			if err != nil {
				return err
			}
			path, err := daemon.SocketPath(session)
			if err != nil {
				return err
			}
			client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
			response, err := client.Request(cmd.Context(), daemon.Frame{Cmd: "snapshot", Args: args, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if !noInjectionScan {
				scanWarnings, scanErr := runInjectionScan(cmd, session, patternsFile)
				if scanErr != nil {
					// A scan failure must not fail the snapshot; it is
					// reported as a warning so the page output survives.
					response.Warnings = append(response.Warnings, daemon.Warning{Kind: "injection_scan", Severity: "warning", Message: "injection scan failed: " + scanErr.Error()})
				} else {
					response.Warnings = append(response.Warnings, scanWarnings...)
				}
			} else {
				slog.Warn("injection scan disabled by --no-injection-scan; page output is not checked for prompt-injection vectors")
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
	return command
}

// runInjectionScan fetches the page HTML and runs the heuristic scan. The
// results are protocol warnings carrying kind, severity, ref, and excerpt.
func runInjectionScan(cmd *cobra.Command, session, patternsFile string) ([]daemon.Warning, error) {
	html, err := fetchPageHTML(cmd.Context(), session)
	if err != nil {
		return nil, err
	}
	scanWarnings, err := injection.Scan(html, injection.ScanOptions{PatternsFile: patternsFile})
	if err != nil {
		return nil, err
	}
	warnings := make([]daemon.Warning, 0, len(scanWarnings))
	for _, warning := range scanWarnings {
		warnings = append(warnings, daemon.Warning{
			Kind:     warning.Kind,
			Severity: warning.Severity,
			Message:  injectionMessage(warning),
			Ref:      warning.Ref,
			Excerpt:  warning.Excerpt,
		})
	}
	return warnings, nil
}

// injectionMessage renders a human-readable message for one detection.
func injectionMessage(warning injection.ScanWarning) string {
	switch warning.Kind {
	case injection.KindHiddenText:
		return "hidden text detected on " + warning.Ref
	case injection.KindImperative:
		return "agent-directed instruction detected on " + warning.Ref
	case injection.KindAriaMismatch:
		return "accessible-name mismatch on " + warning.Ref
	case injection.KindAttribute:
		return "instruction hidden in an attribute on " + warning.Ref
	case injection.KindComment:
		return "instruction hidden in an HTML comment"
	case injection.KindMeta:
		return "instruction hidden in meta content"
	}
	return "prompt-injection heuristic warning on " + warning.Ref
}

// fetchPageHTML reads the current page HTML from the daemon.
func fetchPageHTML(ctx context.Context, session string) (string, error) {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return "", err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	response, err := client.Request(ctx, daemon.Frame{Cmd: "get.html", Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
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
	var html string
	if err := json.Unmarshal(result.Value, &html); err != nil {
		return "", err
	}
	return html, nil
}

// currentPageOrigin fetches the current page URL from the daemon for use as
// the content-boundary origin.
func currentPageOrigin(ctx context.Context, session string) (string, error) {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return "", err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	response, err := client.Request(ctx, daemon.Frame{Cmd: "get.url", Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
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
