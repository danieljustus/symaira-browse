package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/config"
	"github.com/danieljustus/symaira-browse/internal/engine/doctor"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/logging"
	"github.com/danieljustus/symaira-browse/internal/output"
	symversion "github.com/danieljustus/symaira-browse/internal/version"
)

// version is replaced at build time by the Makefile's VERSION value.
var version = "dev"

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "symbrowse",
		Short:         "Agent-operable browser automation over Chrome DevTools Protocol",
		Long:          "symbrowse is the standalone command-line entrypoint for Symaira Browse.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// The global --json flag switches every command to the unified
	// machine-readable output envelope (docs/errors.md, internal/output).
	root.PersistentFlags().Bool("json", false, "print the unified machine-readable output envelope")
	root.AddCommand(newVersionCommand())
	root.AddCommand(newConfigCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newDaemonCommand())
	root.AddCommand(newSessionCommand())
	for _, command := range newNavigationCommands() {
		root.AddCommand(command)
	}
	root.AddCommand(newSnapshotCommand())
	root.AddCommand(newFindCommand())
	for _, command := range newInspectionCommands() {
		root.AddCommand(command)
	}
	for _, command := range newInteractionCommands() {
		root.AddCommand(command)
	}
	return root
}

func newVersionCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "version",
		Short: "Print the symbrowse version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := symversion.Info(version)
			if jsonOutputFlag(cmd) {
				return writeEnvelope(cmd, output.OK(info, nil))
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), info.String())
			return err
		},
	}
	return command
}

func newConfigCommand() *cobra.Command {
	var logLevel, logFormat, configDir, cacheDir, stateDir, executablePath string

	show := &cobra.Command{
		Use:   "show",
		Short: "Show the effective configuration and its source",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			overrides := config.FlagOverrides{}
			if cmd.Flags().Changed("log-level") {
				overrides.LogLevel = &logLevel
			}
			if cmd.Flags().Changed("log-format") {
				overrides.LogFormat = &logFormat
			}
			if cmd.Flags().Changed("config-dir") {
				overrides.ConfigDir = &configDir
			}
			if cmd.Flags().Changed("cache-dir") {
				overrides.CacheDir = &cacheDir
			}
			if cmd.Flags().Changed("state-dir") {
				overrides.StateDir = &stateDir
			}
			if cmd.Flags().Changed("executable-path") {
				overrides.ExecutablePath = &executablePath
			}

			result, err := config.LoadWithOverrides(overrides)
			if err != nil {
				return err
			}
			slog.Debug("configuration loaded", "command", "config show")
			if jsonOutputFlag(cmd) {
				return writeEnvelope(cmd, output.OK(config.ShowOutputFor(result), nil))
			}
			return config.WriteShow(cmd.OutOrStdout(), result, false)
		},
	}
	show.Flags().StringVar(&logLevel, "log-level", "", "override the configured log level")
	show.Flags().StringVar(&logFormat, "log-format", "", "override the configured log format")
	show.Flags().StringVar(&configDir, "config-dir", "", "override the config directory")
	show.Flags().StringVar(&cacheDir, "cache-dir", "", "override the cache directory")
	show.Flags().StringVar(&stateDir, "state-dir", "", "override the state directory")
	show.Flags().StringVar(&executablePath, "executable-path", "", "override the browser executable path")

	command := &cobra.Command{Use: "config", Short: "Inspect symbrowse configuration"}
	command.AddCommand(show)
	return command
}

func newDoctorCommand() *cobra.Command {
	var fix bool
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Check browser discovery and local runtime prerequisites",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			options := doctor.Options{
				ExecutablePath: cfg.ExecutablePath,
				Paths: doctor.Paths{
					ConfigDir: cfg.ConfigDir,
					CacheDir:  cfg.CacheDir,
					StateDir:  cfg.StateDir,
				},
			}
			report := doctor.Run(options)
			if fix {
				report.Fixes = doctor.FixInstructions(runtime.GOOS, options)
			}
			if jsonOutputFlag(cmd) {
				if err := writeEnvelope(cmd, output.OK(report, nil)); err != nil {
					return err
				}
			} else if err := doctor.Write(cmd.OutOrStdout(), report, false); err != nil {
				return err
			}
			if report.HasFailure("chrome") {
				message := doctorFailureMessage(report, "chrome")
				return exitcodes.Wrap(nil, exitcodes.ExitNotFound, exitcodes.KindNotFound, message)
			}
			if report.HasFailures() {
				return exitcodes.Wrap(nil, exitcodes.ExitGeneric, exitcodes.KindInternal, "one or more doctor checks failed")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&fix, "fix", false, "print non-mutating, copyable remediation guidance")
	return command
}

func doctorFailureMessage(report doctor.Report, name string) string {
	for _, check := range report.Checks {
		if check.Name == name && check.Status == doctor.StatusFail {
			return check.Message
		}
	}
	return "doctor check failed"
}

func main() {
	logging.Init()
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

// runCLI executes the root command and returns the process exit code. In JSON
// mode failures are printed as the unified machine-readable envelope on
// stdout; otherwise the human-readable error goes to stderr.
func runCLI(args []string, stdout, stderr io.Writer) int {
	root := newRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		if jsonOutputFlag(root) {
			_ = output.WriteError(stdout, err, true)
		} else {
			_, _ = fmt.Fprintln(stderr, exitcodes.FormatCLIError(err))
		}
		return int(exitcodes.ExitCodeFromError(err))
	}
	return int(exitcodes.ExitOK)
}
