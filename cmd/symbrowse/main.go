package main

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/config"
	"github.com/danieljustus/symaira-browse/internal/engine/doctor"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/logging"
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
	root.AddCommand(newVersionCommand())
	root.AddCommand(newConfigCommand())
	root.AddCommand(newDoctorCommand())
	root.AddCommand(newDaemonCommand())
	root.AddCommand(newSessionCommand())
	for _, command := range newNavigationCommands() {
		root.AddCommand(command)
	}
	root.AddCommand(newSnapshotCommand())
	return root
}

func newVersionCommand() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "version",
		Short: "Print the symbrowse version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := symversion.Info(version)
			if jsonOutput {
				return info.Write(cmd.OutOrStdout())
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), info.String())
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable version payload")
	return command
}

func newConfigCommand() *cobra.Command {
	var jsonOutput bool
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
			return config.WriteShow(cmd.OutOrStdout(), result, jsonOutput)
		},
	}
	show.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable configuration payload")
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
	var jsonOutput bool
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
			if err := doctor.Write(cmd.OutOrStdout(), report, jsonOutput); err != nil {
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
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable doctor payload")
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
	if err := newRootCommand().Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, exitcodes.FormatCLIError(err))
		os.Exit(int(exitcodes.ExitCodeFromError(err)))
	}
}
