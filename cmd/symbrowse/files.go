package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Issue #63: path-guarded file upload command.
func newUploadCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDNetwork,
		Use:     "upload <selector> <files...>",
		Short:   "Upload files into a file input (path-guarded)",
		Long: "upload sets the value of a file input element matching <selector>.\n\n" +
			selectorDocumentation + "\n\n" +
			"Positional arguments:\n" +
			"  <selector>  Target file input element\n" +
			"  <files...>  One or more local file paths to upload\n\n" +
			"Optional [value] argument:\n" +
			"  Not used; specify file paths as positional arguments.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, _ := json.Marshal(map[string]any{"selector": args[0], "files": args[1:]})
			response, err := daemonRequest(cmd.Context(), session, "upload", payload)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "uploaded %d file(s)\n", len(args)-1)
			return err
		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	return command
}

func newDownloadsCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDNetwork,
		Use:     "downloads",
		Short:   "Show download events (origin URL, size, checksum) or set the download directory",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir, _ := cmd.Flags().GetString("dir"); dir != "" {
				payload, _ := json.Marshal(map[string]any{"dir": dir})
				response, err := daemonRequest(cmd.Context(), session, "download.setdir", payload)
				if err != nil {
					return err
				}
				if !response.Success {
					return responseError(response)
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "download directory set to %s\n", dir); err != nil {
					return err
				}
			}
			response, err := daemonRequest(cmd.Context(), session, "downloads.list", nil)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			payload, _ := response.Data.(map[string]any)
			downloads, _ := payload["downloads"].([]any)
			for _, entry := range downloads {
				fields, _ := entry.(map[string]any)
				state, _ := fields["state"].(string)
				filename, _ := fields["filename"].(string)
				url, _ := fields["url"].(string)
				sha256, _ := fields["sha256"].(string)
				line := fmt.Sprintf("[%s] %s (%s)", state, filename, url)
				if state == "completed" && sha256 != "" {
					line += fmt.Sprintf(" sha256:%s", sha256)
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.Flags().String("dir", "", "set the download directory first")
	return command
}

// uploadDirsFromEnv resolves the allowed upload roots: SYMBROWSE_UPLOAD_DIRS
// (comma-separated) or the daemon's working directory as a safe default.
func uploadDirsFromEnv() []string {
	if raw := strings.TrimSpace(os.Getenv("SYMBROWSE_UPLOAD_DIRS")); raw != "" {
		var dirs []string
		for _, dir := range strings.Split(raw, ",") {
			if dir = strings.TrimSpace(dir); dir != "" {
				dirs = append(dirs, dir)
			}
		}
		return dirs
	}
	if cwd, err := os.Getwd(); err == nil {
		return []string{cwd}
	}
	return nil
}
