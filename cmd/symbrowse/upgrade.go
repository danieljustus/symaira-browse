package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/updatecheck"
	"github.com/danieljustus/symaira-corekit/updatecheck/installmethod"
	"github.com/danieljustus/symaira-corekit/updatecheck/updateapply"

	"github.com/danieljustus/symaira-browse/internal/output"
)

// upgradeTarget identifies the GitHub release stream for symbrowse.
const (
	upgradeOwner = "danieljustus"
	upgradeRepo  = "symaira-browse"
)

var (
	upgradeExecutable   = os.Executable
	detectInstallMethod = installmethod.Detect
	applyRelease        = applyReleaseWithCorekit
)

func applyReleaseWithCorekit(ctx context.Context, release *updatecheck.Release, executable string) error {
	applier := updateapply.NewApplier()
	applier.CheckInstallMethod = true
	applier.GOOS = runtime.GOOS
	applier.GOARCH = runtime.GOARCH
	return applier.Apply(ctx, release, executable)
}

// newUpgradeCommand builds `symbrowse upgrade [--check]`.
func newUpgradeCommand() *cobra.Command {
	var checkOnly bool
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "upgrade",
		Short:   "Check for and apply symbrowse updates",
		Long: "upgrade checks GitHub for a newer release (cached for 24h), " +
			"verifies the asset checksum (and cosign signature when available), " +
			"and atomically replaces the running binary with backup and rollback. " +
			"Homebrew installations are not replaced — the command prints the " +
			"brew upgrade hint instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			current := versionString()
			checker := updatecheck.NewChecker(upgradeOwner, upgradeRepo)
			release, err := checker.Check(cmd.Context(), current)
			if err != nil {
				return fmt.Errorf("update check failed: %w", err)
			}
			if release == nil {
				return writeEnvelope(cmd, output.OK(map[string]any{
					"up_to_date": true,
					"current":    current,
					"checked_at": time.Now().UTC().Format(time.RFC3339),
				}, nil))
			}
			if checkOnly {
				return writeEnvelope(cmd, output.OK(map[string]any{
					"up_to_date": false,
					"current":    current,
					"latest":     release.TagName,
					"url":        release.HTMLURL,
					"hint":       upgradeHint(current, release.TagName),
				}, nil))
			}
			return applyUpgrade(cmd, release)
		},
	}
	command.Flags().BoolVar(&checkOnly, "check", false, "only check for updates, do not apply")
	return command
}

// versionString returns the running binary's version with a v prefix when
// missing, matching the updatecheck stable-version parser.
func versionString() string {
	current := version
	if current == "" || current == "dev" {
		return "0.0.0"
	}
	if !strings.HasPrefix(current, "v") {
		return "v" + current
	}
	return current
}

// applyUpgrade downloads, verifies and atomically installs the release.
func applyUpgrade(cmd *cobra.Command, release *updatecheck.Release) error {
	executable, err := upgradeExecutable()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err == nil {
		executable = resolved
	}

	method, err := detectInstallMethod(executable)
	if err != nil {
		return fmt.Errorf("detect install method: %w", err)
	}
	if method == installmethod.Homebrew {
		return writeEnvelope(cmd, output.OK(map[string]any{
			"applied":        false,
			"hint":           installmethod.Guidance(method, "symbrowse"),
			"latest":         release.TagName,
			"install_method": string(method),
		}, nil))
	}
	if err := applyRelease(cmd.Context(), release, executable); err != nil {
		return fmt.Errorf("update failed (rollback performed): %w", err)
	}
	return writeEnvelope(cmd, output.OK(map[string]any{
		"applied": true,
		"latest":  release.TagName,
		"hint":    "restart symbrowse to use the new version",
	}, nil))
}

// upgradeHint builds the human-readable next step for a pending update.
func upgradeHint(current, latest string) string {
	return fmt.Sprintf("symbrowse %s is available (current: %s); run `symbrowse upgrade` to apply it", latest, current)
}

// checkUpdatesAsync performs a non-blocking update check when enabled by the
// environment. It never writes to stdout: hints go to stderr so MCP mode
// stays zero-stdout (issue #66 AC).
func checkUpdatesAsync(ctx context.Context) {
	if os.Getenv("SYMBROWSE_CHECK_UPDATES") == "" {
		return
	}
	go func() {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		checker := updatecheck.NewChecker(upgradeOwner, upgradeRepo)
		release, err := checker.Check(checkCtx, versionString())
		if err != nil || release == nil {
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "symbrowse %s is available; run `symbrowse upgrade` to apply it\n", release.TagName)
	}()
}
