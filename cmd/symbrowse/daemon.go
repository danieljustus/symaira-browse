package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/config"
	"github.com/danieljustus/symaira-browse/internal/daemon"
)

func newDaemonCommand() *cobra.Command {
	var session, allowedDomains string
	var ssrf, allowPrivate bool
	command := &cobra.Command{
		Use:   "daemon",
		Short: "Run or inspect the symbrowse daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd, session, allowedDomains, ssrf, allowPrivate)
		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")
	command.Flags().StringVar(&allowedDomains, "allowed-domains", "", "comma-separated domain allowlist (e.g. \"example.com,*.example.com\"); denies every other domain on the network layer")
	command.Flags().BoolVar(&ssrf, "ssrf", false, "enable the SSRF guard: RFC1918, loopback, link-local, .local, and IPv6-ULA targets are denied (default on in MCP mode)")
	command.Flags().BoolVar(&allowPrivate, "allow-private", false, "allow private and loopback targets when the SSRF guard is active")
	command.AddCommand(newDaemonStatusCommand(&session))
	command.AddCommand(newDaemonStopCommand(&session))
	return command
}

func newDaemonStatusCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := daemonLifecycleRequest(cmd.Context(), *session, "daemon.status", false)
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response, false)
		},
	}
	return command
}

func newDaemonStopCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := daemonLifecycleRequest(cmd.Context(), *session, "daemon.stop", false)
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response, false)
		},
	}
	return command
}

func runDaemon(cmd *cobra.Command, session, allowedDomainsFlag string, ssrfFlag, allowPrivateFlag bool) error {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return err
	}
	allowedDomains := resolveAllowedDomains(allowedDomainsFlag)
	ssrfEnabled := resolveBoolPolicy("SYMBROWSE_SSRF", ssrfFlag, func(cfg *config.Config) bool { return cfg.SSRFEnabled })
	allowPrivate := resolveBoolPolicy("SYMBROWSE_ALLOW_PRIVATE", allowPrivateFlag, func(cfg *config.Config) bool { return cfg.AllowPrivate })
	idle := time.Duration(daemon.DefaultIdleTimeout)
	if raw := os.Getenv("SYMBROWSE_IDLE_TIMEOUT"); raw != "" {
		seconds, parseErr := strconv.Atoi(raw)
		if parseErr != nil || seconds < 0 {
			return fmt.Errorf("invalid SYMBROWSE_IDLE_TIMEOUT %q", raw)
		}
		if seconds == 0 {
			idle = -1
		} else {
			idle = time.Duration(seconds) * time.Second
		}
	}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{})
	navigation := daemon.NewNavigationRuntime(registry, os.Getenv("SYMBROWSE_EXECUTABLE_PATH"), daemon.NavigationRuntimeOptions{AllowedDomains: allowedDomains, SSRFEnabled: ssrfEnabled, AllowPrivate: allowPrivate})
	defer func() { _ = navigation.Close() }()
	server := daemon.NewServer(daemon.Options{
		SocketPath:  path,
		Session:     session,
		Registry:    registry,
		IdleTimeout: idle,
		Policy: daemon.PolicyStatus{
			AllowedDomains: allowedDomains,
			SSRFEnabled:    ssrfEnabled,
			AllowPrivate:   allowPrivate,
		},
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			switch frame.Cmd {
			case "daemon.ping":
				return map[string]any{"pong": true}, nil, nil
			case "open", "goto", "back", "forward", "reload", "wait", "snapshot", "read", "click", "dblclick", "fill", "type", "press", "hover", "focus", "select", "check", "uncheck", "scroll", "scrollintoview", "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked":
				return navigation.Handle(ctx, frame)
			default:
				return nil, nil, daemon.NewError(daemon.ErrorUnknownCommand, "command is not implemented by the daemon")
			}
		},
	})
	err = server.ListenAndServe(ctx)
	if errors.Is(err, daemon.ErrIdleTimeout) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// resolveAllowedDomains applies the configuration precedence chain for the
// domain allowlist: daemon flag, then SYMBROWSE_ALLOWED_DOMAINS, then the
// allowed_domains setting from config.toml.
func resolveAllowedDomains(flagValue string) []string {
	if strings.TrimSpace(flagValue) != "" {
		return splitDomains(flagValue)
	}
	if envValue := os.Getenv("SYMBROWSE_ALLOWED_DOMAINS"); strings.TrimSpace(envValue) != "" {
		return splitDomains(envValue)
	}
	cfg, err := config.Load()
	if err != nil || len(cfg.AllowedDomains) == 0 {
		return nil
	}
	return cfg.AllowedDomains
}

// resolveBoolPolicy applies the same precedence chain for boolean policy
// options (SSRF guard, allow-private): flag, then environment variable, then
// the config.toml setting. Environment values follow strconv.ParseBool
// semantics ("1", "t", "true", "0", "f", "false").
func resolveBoolPolicy(envName string, flagValue bool, fromConfig func(*config.Config) bool) bool {
	if flagValue {
		return true
	}
	if envValue := os.Getenv(envName); envValue != "" {
		parsed, err := strconv.ParseBool(envValue)
		if err != nil {
			return false
		}
		return parsed
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return fromConfig(cfg)
}

// splitDomains splits a comma-separated allowlist value, preserving each
// pattern as supplied (validation happens in the engine policy layer).
func splitDomains(value string) []string {
	var domains []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			domains = append(domains, trimmed)
		}
	}
	return domains
}

func daemonLifecycleRequest(ctx context.Context, session, command string, autostart bool) (daemon.Response, error) {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return daemon.Response{}, err
	}
	client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: session})
	frame := daemon.Frame{Cmd: command, Session: session, RequestID: fmt.Sprintf("%d", time.Now().UnixNano())}
	if autostart {
		return client.Request(ctx, frame)
	}
	return client.RequestWithoutAutostart(ctx, frame)
}

func writeDaemonResponse(cmd *cobra.Command, response daemon.Response, jsonOutput bool) error {
	return writeEnvelopeFromResponse(cmd, response, jsonOutput)
}
