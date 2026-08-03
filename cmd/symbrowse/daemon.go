package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/config"
	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/profiles"
	"github.com/danieljustus/symaira-browse/internal/state"
)

func newDaemonCommand() *cobra.Command {
	var session, restore, profile string
	command := &cobra.Command{
		Use:   "daemon",
		Short: "Run or inspect the symbrowse daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd, session)
		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")
	command.PersistentFlags().StringVar(&restore, "restore", "", "restore the named state when the session browser starts")
	command.PersistentFlags().StringVar(&profile, "profile", "", "reuse an existing Chrome profile (name or path) instead of a private session profile")
	command.AddCommand(newDaemonStatusCommand(&session))
	command.AddCommand(newDaemonStopCommand(&session))
	return command
}

func newDaemonStatusCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := daemonLifecycleRequest(cmd.Context(), *session, "daemon.status", false)
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response, jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable status payload")
	return command
}

func newDaemonStopCommand(session *string) *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := daemonLifecycleRequest(cmd.Context(), *session, "daemon.stop", false)
			if err != nil {
				return err
			}
			return writeDaemonResponse(cmd, response, jsonOutput)
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print the machine-readable stop payload")
	return command
}

func runDaemon(cmd *cobra.Command, session string) error {
	profile, _ := cmd.Flags().GetString("profile")
	if profile != "" {
		resolved, byName, err := profiles.Resolve(profile)
		if err != nil {
			return fmt.Errorf("resolve profile %q: %w", profile, err)
		}
		if byName {
			profile = resolved
		}
	}
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

	// State store: named cookie/storage snapshots under <state-dir>/states.
	// A KeyResolver is attached when SYMBROWSE_ENCRYPTION_KEY is set so state
	// files are encrypted (issue B-35). Without a key the store falls back to
	// plaintext so the tool stays fully usable standalone.
	stateStore, err := newStateStore(cmd)
	if err != nil {
		return err
	}

	// Restore + autosave wiring (issue B-36): --restore <key> restores the
	// named state when the session browser starts; autosave policy and
	// interval come from SYMBROWSE_AUTOSAVE / SYMBROWSE_AUTOSAVE_INTERVAL.
	restoreOnStart := map[string]string{}
	if cmd.Flags().Changed("restore") {
		if restoreKey, err := cmd.Flags().GetString("restore"); err == nil && restoreKey != "" {
			restoreOnStart[session] = restoreKey
		}
	}
	autosave, err := autosaveFromEnv(cmd, restoreOnStart[session])
	if err != nil {
		return err
	}

	navigation := daemon.NewNavigationRuntime(registry, os.Getenv("SYMBROWSE_EXECUTABLE_PATH"), daemon.NavigationRuntimeOptions{
		StateStore:     stateStore,
		RestoreOnStart: restoreOnStart,
		Autosave:       autosave,
		Profile:        profile,
	})
	defer func() { _ = navigation.Close() }()
	stateRuntime := daemon.NewStateRuntime(stateStore, navigation)
	stateRuntime.ReportExpired()
	authRuntime := daemon.NewAuthRuntime(navigation, nil)
	server := daemon.NewServer(daemon.Options{
		SocketPath:  path,
		Session:     session,
		Registry:    registry,
		IdleTimeout: idle,
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			switch frame.Cmd {
			case "daemon.ping":
				return map[string]any{"pong": true}, nil, nil
			case "open", "goto", "back", "forward", "reload", "wait", "snapshot", "click", "dblclick", "fill", "type", "press", "hover", "focus", "select", "check", "uncheck", "scroll", "scrollintoview", "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked", "cookies.list", "cookies.set", "cookies.clear", "storage.list", "storage.set", "storage.clear":
				return navigation.Handle(ctx, frame)
			case "state.save", "state.load", "state.list", "state.show", "state.clear", "state.clean":
				return stateRuntime.Handle(ctx, frame)
			case "auth.login":
				return authRuntime.Handle(ctx, frame)
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

// newStateStore builds the named-state store rooted at <state-dir>/states,
// attaching the encryption key resolver when a key source is configured. The
// default retention window comes from SYMBROWSE_STATE_EXPIRE_DAYS (default
// 30 days).
func newStateStore(cmd *cobra.Command) (*state.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.StateDir, "states")
	var keys state.KeyProvider
	if os.Getenv(state.EnvKeyName) != "" {
		keys = state.NewKeyResolver()
	}
	expireIn := time.Duration(state.DefaultExpireDays) * 24 * time.Hour
	if raw := os.Getenv("SYMBROWSE_STATE_EXPIRE_DAYS"); raw != "" {
		days, parseErr := strconv.Atoi(raw)
		if parseErr != nil || days < 0 {
			return nil, fmt.Errorf("invalid SYMBROWSE_STATE_EXPIRE_DAYS %q", raw)
		}
		if days > 0 {
			expireIn = time.Duration(days) * 24 * time.Hour
		}
	}
	store, err := state.NewStore(state.StoreOptions{Dir: dir, Keys: keys, ExpireIn: expireIn})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// autosaveFromEnv derives the autosave policy from SYMBROWSE_AUTOSAVE
// (auto|always|never) and SYMBROWSE_AUTOSAVE_INTERVAL (seconds, default 30;
// 0 means save only on close). Autosave is disabled unless a restore key was
// given or SYMBROWSE_AUTOSAVE_KEY names a target state.
func autosaveFromEnv(cmd *cobra.Command, restoreKey string) (*daemon.AutosaveConfig, error) {
	key := restoreKey
	if raw := os.Getenv("SYMBROWSE_AUTOSAVE_KEY"); raw != "" {
		key = raw
	}
	config := &daemon.AutosaveConfig{Policy: daemon.AutosaveAuto, Interval: 30 * time.Second, Key: key}
	if raw := os.Getenv("SYMBROWSE_AUTOSAVE"); raw != "" {
		config.Policy = daemon.AutosavePolicy(raw)
	}
	if raw := os.Getenv("SYMBROWSE_AUTOSAVE_INTERVAL"); raw != "" {
		seconds, parseErr := strconv.Atoi(raw)
		if parseErr != nil || seconds < 0 {
			return nil, fmt.Errorf("invalid SYMBROWSE_AUTOSAVE_INTERVAL %q", raw)
		}
		config.Interval = time.Duration(seconds) * time.Second
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
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
	if !response.Success {
		if response.Error == nil {
			return errors.New("daemon request failed")
		}
		return errors.New(response.Error.Message)
	}
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Data)
	}
	if response.Data == nil {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "ok")
		return err
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), response.Data)
	return err
}
