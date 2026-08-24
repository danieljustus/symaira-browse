package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newScreenshotCommand captures the page as an image file (issue #16, B-12):
// viewport by default, --full for the whole page, --selector/@ref for one
// element, png or jpeg with optional quality. Without a path the file is
// written to the cache out directory and its path is returned. The unified
// JSON envelope reports the path, pixel dimensions and byte size.
func newScreenshotCommand() *cobra.Command {
	var session string
	var full bool
	var selector, format, screenshotDir string
	var quality int
	command := &cobra.Command{
		GroupID: groupIDCore,
		Use:     "screenshot [path]",
		Short:   "Capture the page (viewport, --full page, or --selector element)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := map[string]any{
				"full":     full,
				"selector": selector,
				"format":   format,
				"quality":  quality,
				"dir":      screenshotDir,
			}
			if len(args) == 1 {
				payload["path"] = args[0]
			}
			raw, _ := json.Marshal(payload)
			response, err := stateRequest(cmd.Context(), session, "screenshot", raw)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if jsonOutputFlag(cmd) {
				return writeEnvelopeFromResponse(cmd, response, true)
			}
			result, _ := response.Data.(map[string]any)
			path, _ := result["path"].(string)
			width, _ := result["width"].(float64)
			height, _ := result["height"].(float64)
			bytes, _ := result["bytes"].(float64)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "screenshot saved to %s (%dx%d, %d bytes)\n", path, int(width), int(height), int(bytes))
			return err
		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.Flags().BoolVar(&full, "full", false, "capture the whole page, not just the viewport")
	command.Flags().StringVar(&selector, "selector", "", "capture the element matched by a CSS selector or @ref")
	command.Flags().StringVar(&format, "format", "png", "image format: png or jpeg")
	command.Flags().IntVar(&quality, "quality", 0, "jpeg quality 0-100 (jpeg only)")
	command.Flags().StringVar(&screenshotDir, "screenshot-dir", "", "allow writing the screenshot into this directory (default: the cache out directory)")
	return command
}
