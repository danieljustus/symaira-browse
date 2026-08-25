package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/diff"
	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// newDiffCommand builds the `symbrowse diff` command tree.
func newDiffCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "diff",
		Short:   "Compare snapshots, screenshots and URLs",
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")
	command.AddCommand(newDiffSnapshotCommand(&session))
	command.AddCommand(newDiffScreenshotCommand(&session))
	command.AddCommand(newDiffURLCommand(&session))
	return command
}

// newDiffSnapshotCommand diffs the current snapshot against a baseline file
// or the previous snapshot.
func newDiffSnapshotCommand(session *string) *cobra.Command {
	var baseline string
	command := &cobra.Command{
		Use:   "snapshot",
		Short: "Diff the current snapshot against a baseline file or the previous snapshot",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := daemon.NewClient(daemon.ClientOptions{Session: *session})
			options := engine.SnapshotOptions{Diff: true}
			if baseline != "" {
				// Compare against a stored snapshot baseline file.
				data, err := os.ReadFile(baseline)
				if err != nil {
					return fmt.Errorf("read baseline %q: %w", baseline, err)
				}
				var stored struct {
					Tree string                        `json:"tree"`
					Refs map[string]engine.SnapshotRef `json:"refs"`
				}
				if err := json.Unmarshal(data, &stored); err != nil {
					return fmt.Errorf("decode baseline %q: %w", baseline, err)
				}
				// Capture the current snapshot first.
				response, err := client.Request(cmd.Context(), daemon.Frame{
					Cmd: "snapshot", Args: marshalArgs(engine.SnapshotOptions{}), Session: *session,
				})
				if err != nil {
					return err
				}
				if !response.Success {
					return responseError(response)
				}
				raw, _ := json.Marshal(response.Data)
				var current struct {
					Tree string                        `json:"tree"`
					Refs map[string]engine.SnapshotRef `json:"refs"`
				}
				if err := json.Unmarshal(raw, &current); err != nil {
					return err
				}
				result := diffSnapshotTrees(stored.Tree, current.Tree)
				return writeEnvelope(cmd, output.OK(result, nil))
			}
			// Diff against the previous in-session snapshot.
			response, err := client.Request(cmd.Context(), daemon.Frame{
				Cmd: "snapshot", Args: marshalArgs(options), Session: *session,
			})
			if err != nil {
				return err
			}
			return writeEnvelopeFromResponse(cmd, response)
		},
	}
	command.Flags().StringVar(&baseline, "baseline", "", "baseline snapshot JSON file to compare against")
	return command
}

// diffSnapshotTrees produces a simple +/-/changed diff between two snapshot
// tree strings.
func diffSnapshotTrees(before, after string) map[string]any {
	beforeLines := splitLines(before)
	afterLines := splitLines(after)
	beforeSet := make(map[string]bool, len(beforeLines))
	for _, line := range beforeLines {
		beforeSet[line] = true
	}
	afterSet := make(map[string]bool, len(afterLines))
	for _, line := range afterLines {
		afterSet[line] = true
	}
	var removed, added, stable []string
	for _, line := range beforeLines {
		if afterSet[line] {
			stable = append(stable, line)
		} else {
			removed = append(removed, line)
		}
	}
	for _, line := range afterLines {
		if !beforeSet[line] {
			added = append(added, line)
		}
	}
	return map[string]any{
		"added":   added,
		"removed": removed,
		"stable":  len(stable),
		"diff":    renderDiffLines(removed, added),
	}
}

func splitLines(value string) []string {
	var lines []string
	current := ""
	for _, char := range value {
		if char == '\n' {
			if current != "" {
				lines = append(lines, current)
			}
			current = ""
			continue
		}
		current += string(char)
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func renderDiffLines(removed, added []string) []string {
	var lines []string
	for _, line := range removed {
		lines = append(lines, "- "+line)
	}
	for _, line := range added {
		lines = append(lines, "+ "+line)
	}
	return lines
}

// newDiffScreenshotCommand compares two PNG screenshots with a tolerance.
func newDiffScreenshotCommand(session *string) *cobra.Command {
	var baseline string
	var threshold float64
	var out string
	command := &cobra.Command{
		Use:   "screenshot",
		Short: "Compare the current screenshot against a baseline PNG",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if baseline == "" {
				return invalidArgs("--baseline <png> is required")
			}
			baselineData, err := os.ReadFile(baseline)
			if err != nil {
				return fmt.Errorf("read baseline %q: %w", baseline, err)
			}
			client := daemon.NewClient(daemon.ClientOptions{Session: *session})
			response, err := client.Request(cmd.Context(), daemon.Frame{
				Cmd: "screenshot", Args: marshalArgs(map[string]any{}), Session: *session,
			})
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			raw, _ := json.Marshal(response.Data)
			var shot struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &shot); err != nil {
				return err
			}
			if shot.Path == "" {
				return errors.New("screenshot response did not include a file path")
			}
			captured, err := os.ReadFile(shot.Path)
			if err != nil {
				return fmt.Errorf("read captured screenshot %q: %w", shot.Path, err)
			}
			result, err := diff.CompareScreenshots(baselineData, captured, diff.Options{Threshold: threshold})
			if err != nil {
				return err
			}
			if out != "" {
				if err := os.WriteFile(out, result.DiffImagePNG, 0o600); err != nil {
					return fmt.Errorf("write diff image %q: %w", out, err)
				}
			}
			payload, err := result.JSON()
			if err != nil {
				return err
			}
			var envelopePayload map[string]any
			_ = json.Unmarshal(payload, &envelopePayload)
			if out != "" {
				envelopePayload["diff_image"] = out
			}
			if err := writeEnvelope(cmd, output.OK(envelopePayload, nil)); err != nil {
				return err
			}
			// CI semantics: exit non-zero when the deviation exceeds the
			// threshold.
			if !result.Passed {
				return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation,
					"screenshot deviation %.2f%% exceeds threshold %.2f%%", result.DeviationPct, result.Threshold*100)
			}
			return nil
		},
	}
	command.Flags().StringVar(&baseline, "baseline", "", "baseline PNG file to compare against (required)")
	command.Flags().Float64Var(&threshold, "threshold", 0.001, "allowed deviation fraction (0..1)")
	command.Flags().StringVar(&out, "out", "", "write the diff image to this file")
	return command
}

// newDiffURLCommand opens two URLs in two tabs and diffs their read output.
func newDiffURLCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "url <url1> <url2>",
		Short: "Open two URLs and diff their extracted content",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := daemon.NewClient(daemon.ClientOptions{Session: *session})
			read := func(target string) (string, error) {
				response, err := client.Request(cmd.Context(), daemon.Frame{
					Cmd: "read", Args: marshalArgs(map[string]any{"url": target}), Session: *session,
				})
				if err != nil {
					return "", err
				}
				if !response.Success {
					return "", responseError(response)
				}
				raw, _ := json.Marshal(response.Data)
				var payload struct {
					HTML  string `json:"html"`
					Title string `json:"title"`
				}
				if err := json.Unmarshal(raw, &payload); err != nil {
					return "", err
				}
				return payload.Title + "\n" + payload.HTML, nil
			}
			first, err := read(args[0])
			if err != nil {
				return err
			}
			second, err := read(args[1])
			if err != nil {
				return err
			}
			result := diffSnapshotTrees(first, second)
			return writeEnvelope(cmd, output.OK(result, nil))
		},
	}
	return command
}
