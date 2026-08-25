package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/domkit"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/injection"
	"github.com/danieljustus/symaira-browse/internal/output"
)

func newReadCommand() *cobra.Command {
	var session, selector, filter string
	var outline, raw, contentBoundaries, engineHint bool
	command := &cobra.Command{
		GroupID: groupIDCore,
		Use:     "read [url]",
		Short:   "Render the page as markdown (or JSON) in the symfetch output schema",
		Long: "read renders the current page — or the page at url when given — and prints " +
			"markdown with YAML frontmatter (title, url, fetched_at, lang, tokens_est, schema_type) " +
			"in the symfetch output schema. --json prints the same document as the unified envelope. " +
			"--engine-hint additionally reports whether JavaScript was actually needed for the " +
			"content (js_required), so an agent can choose Tier 0 directly next time.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := ""
			if len(args) == 1 {
				url = args[0]
			}
			payload, err := json.Marshal(map[string]any{"url": url, "engine_hint": engineHint})
			if err != nil {
				return err
			}
			response, err := readRequest(cmd, cmd.Context(), session, payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			var material struct {
				HTML             string `json:"html"`
				Title            string `json:"title"`
				URL              string `json:"url"`
				JSRequired       *bool  `json:"js_required"`
				JSRequiredReason string `json:"js_required_reason"`
			}
			materialJSON, err := json.Marshal(response.Data)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(materialJSON, &material); err != nil {
				return fmt.Errorf("decode read material: %w", err)
			}
			document, err := domkit.Render(material.HTML, material.Title, material.URL, domkit.Options{
				Selector: selector,
				Filter:   filter,
				Outline:  outline,
				Raw:      raw,
			})
			if err != nil {
				return exitcodes.Wrapf(err, exitcodes.ExitNoInput, exitcodes.KindValidation, "%s", err)
			}
			if contentBoundaries && material.URL != "" {
				boundary, err := injection.New(material.URL)
				if err != nil {
					return err
				}
				document.ContentBoundaries = (*domkit.Boundary)(&boundary)
			}
			if structuredOutput(cmd) {
				data := any(document)
				if material.JSRequired != nil {
					// The engine hint is a sibling of the document in the
					// envelope data (never inside the fetch-schema
					// frontmatter, which is contract-fixed).
					data = withEngineHint(document, *material.JSRequired, material.JSRequiredReason)
				}
				return writeEnvelope(cmd, output.OK(data, nil))
			}
			if material.JSRequired != nil {
				return writeReadHumanWithHint(cmd, document, *material.JSRequired, material.JSRequiredReason)
			}
			return writeReadHuman(cmd, document)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	command.Flags().StringVar(&selector, "selector", "", "render only the first subtree matching this CSS selector")
	command.Flags().StringVar(&filter, "filter", "", "remove every subtree matching this CSS selector before rendering")
	command.Flags().BoolVar(&outline, "outline", false, "return only the heading structure")
	command.Flags().BoolVar(&raw, "raw", false, "return the page HTML instead of markdown")
	command.Flags().BoolVar(&contentBoundaries, "content-boundaries", false, "wrap page content in unforgeable boundary markers (default on in MCP mode)")
	command.Flags().BoolVar(&engineHint, "engine-hint", false, "report whether JavaScript was needed for the content (js_required with reason)")
	command.Flags().Int("max-tokens", 0, "token budget for the payload; oversized output is truncated and stored in the cache (0 = no limit)")
	return command
}

// withEngineHint merges the engine-hint fields into the envelope data while
// keeping the document fields intact.
func withEngineHint(document domkit.Document, required bool, reason string) any {
	raw, err := json.Marshal(document)
	if err != nil {
		return document
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return document
	}
	data["js_required"] = required
	if reason != "" {
		data["js_required_reason"] = reason
	}
	return data
}

// writeReadHumanWithHint prints the document followed by the engine-hint
// line, so a Tier-0 agent sees the recommendation immediately.
func writeReadHumanWithHint(cmd *cobra.Command, document domkit.Document, required bool, reason string) error {
	if err := writeReadHuman(cmd, document); err != nil {
		return err
	}
	line := fmt.Sprintf("js_required: %t", required)
	if reason != "" {
		line += " — " + reason
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), line)
	return err
}

func writeReadHuman(cmd *cobra.Command, document domkit.Document) error {
	var builder strings.Builder
	builder.WriteString(domkit.Frontmatter(document))
	switch {
	case len(document.Outline) > 0:
		for _, heading := range document.Outline {
			builder.WriteString(strings.Repeat("#", heading.Level))
			builder.WriteString(" ")
			builder.WriteString(heading.Text)
			builder.WriteString("\n")
		}
	case document.Raw != "":
		builder.WriteString(document.Raw)
	default:
		builder.WriteString(document.Markdown)
	}
	output := strings.TrimRight(builder.String(), "\n")
	if document.ContentBoundaries != nil {
		// The boundary encloses only the page-derived body; the frontmatter
		// metadata stays outside it.
		bodyStart := strings.Index(output, "\n---\n\n")
		if bodyStart >= 0 {
			head := output[:bodyStart+5]
			body := output[bodyStart+5:]
			output = head + document.ContentBoundaries.WrapText(body)
		} else {
			output = document.ContentBoundaries.WrapText(output)
		}
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), output)
	return err
}

func readRequest(cmd *cobra.Command, ctx context.Context, session string, args json.RawMessage) (daemon.Response, error) {
	response, err := requestBudget(ctx, session, "read", args, maxTokensFlag(cmd))
	if err != nil {
		return daemon.Response{}, err
	}
	if !response.Success && response.Error == nil {
		return daemon.Response{}, errors.New("read request failed")
	}
	return response, nil
}
