package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

func newDaemonCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		Use:   "daemon",
		Short: "Run or inspect the symbrowse daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd, session)
		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")
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

func runDaemon(cmd *cobra.Command, session string) error {
	path, err := daemon.SocketPath(session)
	if err != nil {
		return err
	}
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
	navigation := daemon.NewNavigationRuntime(registry, os.Getenv("SYMBROWSE_EXECUTABLE_PATH"))
	defer func() { _ = navigation.Close() }()
	server := daemon.NewServer(daemon.Options{
		SocketPath:  path,
		Session:     session,
		Registry:    registry,
		IdleTimeout: idle,
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			switch frame.Cmd {
			case "daemon.ping":
				return map[string]any{"pong": true}, nil, nil
			case "open", "goto", "back", "forward", "reload", "wait", "snapshot", "click", "dblclick", "fill", "type", "press", "hover", "focus", "select", "check", "uncheck", "scroll", "scrollintoview", "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked":
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
