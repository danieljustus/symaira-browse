package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/injection"
	"github.com/danieljustus/symaira-browse/internal/output"
	"github.com/danieljustus/symaira-browse/internal/render"
)

func newReadCommand() *cobra.Command {
	var session, selector, filter string
	var outline, raw, contentBoundaries bool
	command := &cobra.Command{
		Use:   "read [url]",
		Short: "Render the page as markdown (or JSON) in the symfetch output schema",
		Long: "read renders the current page — or the page at url when given — and prints " +
			"markdown with YAML frontmatter (title, url, fetched_at, lang, tokens_est, schema_type) " +
			"in the symfetch output schema. --json prints the same document as the unified envelope.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := ""
			if len(args) == 1 {
				url = args[0]
			}
			payload, err := json.Marshal(map[string]string{"url": url})
			if err != nil {
				return err
			}
			response, err := readRequest(cmd.Context(), session, payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			var material struct {
				HTML  string `json:"html"`
				Title string `json:"title"`
				URL   string `json:"url"`
			}
			materialJSON, err := json.Marshal(response.Data)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(materialJSON, &material); err != nil {
				return fmt.Errorf("decode read material: %w", err)
			}
			document, err := render.Render(material.HTML, material.Title, material.URL, render.Options{
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
				document.ContentBoundaries = &boundary
			}
			if jsonOutputFlag(cmd) {
				return writeEnvelope(cmd, output.OK(document, nil))
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
	return command
}

func writeReadHuman(cmd *cobra.Command, document render.Document) error {
	var builder strings.Builder
	builder.WriteString(render.Frontmatter(document))
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

func readRequest(ctx context.Context, session string, args json.RawMessage) (daemon.Response, error) {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return daemon.Response{}, err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	response, err := client.Request(ctx, daemon.Frame{Cmd: "read", Args: args, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())})
	if err != nil {
		return daemon.Response{}, err
	}
	if !response.Success && response.Error == nil {
		return daemon.Response{}, errors.New("read request failed")
	}
	return response, nil
}
