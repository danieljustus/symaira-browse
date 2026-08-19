package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/config"
	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/oob"
	"github.com/danieljustus/symaira-browse/internal/policy"
	"github.com/danieljustus/symaira-browse/internal/profiles"
	"github.com/danieljustus/symaira-browse/internal/sessionid"
	"github.com/danieljustus/symaira-browse/internal/state"
)

func newDaemonCommand() *cobra.Command {
	var session, restore, profile, allowedDomains string
	var ssrf, allowPrivate, headless bool
	var engineKind string
	command := &cobra.Command{
		Use:   "daemon",
		Short: "Run or inspect the symbrowse daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd, session)

		},
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "daemon session name")
	command.Flags().StringVar(&allowedDomains, "allowed-domains", "", "comma-separated domain allowlist (e.g. \"example.com,*.example.com\"); denies every other domain on the network layer")
	command.Flags().BoolVar(&ssrf, "ssrf", false, "enable the SSRF guard: RFC1918, loopback, link-local, .local, and IPv6-ULA targets are denied (default on in MCP mode)")
	command.Flags().BoolVar(&allowPrivate, "allow-private", false, "allow private and loopback targets when the SSRF guard is active")
	command.Flags().BoolVar(&headless, "headless", false, "launch Chrome in headless mode (no GUI session; also via SYMBROWSE_HEADLESS=1)")
	command.PersistentFlags().StringVar(&restore, "restore", "", "restore the named state when the session browser starts")
	command.PersistentFlags().StringVar(&profile, "profile", "", "reuse an existing Chrome profile (name or path) instead of a private session profile")
	command.Flags().StringVar(&engineKind, "engine", "chrome", "engine implementation: chrome (default) or static (JS-free HTML reader)")

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
	allowedDomainsFlag, _ := cmd.Flags().GetString("allowed-domains")
	allowedDomains := resolveAllowedDomains(allowedDomainsFlag)
	ssrfFlag, _ := cmd.Flags().GetBool("ssrf")
	ssrfEnabled := resolveBoolPolicy("SYMBROWSE_SSRF", ssrfFlag, func(cfg *config.Config) bool { return cfg.SSRFEnabled })
	allowPrivateFlag, _ := cmd.Flags().GetBool("allow-private")
	allowPrivate := resolveBoolPolicy("SYMBROWSE_ALLOW_PRIVATE", allowPrivateFlag, func(cfg *config.Config) bool { return cfg.AllowPrivate })
	headless := false
	if raw := cmd.Flags().Lookup("headless"); raw != nil {
		headless, _ = cmd.Flags().GetBool("headless")
	}
	headless = headless || os.Getenv("SYMBROWSE_HEADLESS") == "1"
	engineKind := "chrome"
	if raw := cmd.Flags().Lookup("engine"); raw != nil {
		engineKind, _ = cmd.Flags().GetString("engine")
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
	operation := time.Duration(daemon.DefaultOperationTimeout)
	if raw := os.Getenv("SYMBROWSE_OPERATION_TIMEOUT"); raw != "" {
		seconds, parseErr := strconv.Atoi(raw)
		if parseErr != nil || seconds <= 0 {
			return fmt.Errorf("invalid SYMBROWSE_OPERATION_TIMEOUT %q", raw)
		}
		operation = time.Duration(seconds) * time.Second
	}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Sessions record where they were created (worktree/repo/cwd scope) so
	// `session list` shows scope and origin path (issue B-37).
	scopeInfo, _ := sessionid.ID(sessionid.ScopeWorktree, "", "")
	registry := daemon.NewSessionRegistry(daemon.SessionRegistryOptions{
		Scope:      string(scopeInfo.Scope),
		OriginPath: scopeInfo.OriginPath,
	})

	// State store: named cookie/storage snapshots under <state-dir>/states.
	// A KeyResolver is probed at construction; when a key source (symvault,
	// OS keychain, or env var) is available, state files are encrypted.
	// Without a key the store falls back to plaintext so the tool stays
	// fully usable standalone.
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

	// Screenshots (issue #16) are written into the cache out directory by
	// default; --screenshot-dir on the command expands the allowed roots.
	screenshotDirs := []string{}
	if paths, pathErr := config.DefaultPaths(); pathErr == nil {
		screenshotDirs = []string{filepath.Join(paths.CacheDir, "out")}
	}
	navigation := daemon.NewNavigationRuntime(registry, os.Getenv("SYMBROWSE_EXECUTABLE_PATH"), daemon.NavigationRuntimeOptions{
		AllowedDomains: allowedDomains,
		SSRFEnabled:    ssrfEnabled,
		AllowPrivate:   allowPrivate,
		Headless:       headless,
		StateStore:     stateStore,
		RestoreOnStart: restoreOnStart,
		Autosave:       autosave,
		Profile:        profile,
		UploadDirs:     uploadDirsFromEnv(),
		ScreenshotDirs: screenshotDirs,
		Engine:         engineKind,
	})
	defer func() { _ = navigation.Close() }()
	stateRuntime := daemon.NewStateRuntime(stateStore, navigation)
	stateRuntime.ReportExpired()
	authRuntime := daemon.NewAuthRuntime(navigation, nil)
	cfg, configErr := config.Load()
	if configErr != nil {
		return configErr
	}
	journalRuntime := daemon.NewJournalRuntime(journalFor(cfg, session), navigation)
	policyRuntime := daemon.NewPolicyRuntime(cfg.StateDir, policyMode())
	oobRuntime := daemon.NewOOBRuntime(oob.NewManager(), oob.NewNotifier(), navigation, policyRuntime.Policy(), policyMode())
	// symguard delegation (issue #52): the guard decides when it is present
	// and configured; the decider lands in the journal for every action.
	oobRuntime.SetDecider(policyRuntime.Decide)
	if policyRuntime.Guard() != nil && policyRuntime.Guard().Active() {
		slog.Info("symguard delegation active", "executable", policyRuntime.Guard().Executable)
	}
	server := daemon.NewServer(daemon.Options{
		SocketPath:       path,
		Session:          session,
		Registry:         registry,
		IdleTimeout:      idle,
		OperationTimeout: operation,
		CacheDir:         filepath.Join(cfg.CacheDir, "out"),
		CacheTTL:         time.Duration(cfg.CacheTTLHours) * time.Hour,
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			switch frame.Cmd {
			case "daemon.ping":
				return map[string]any{"pong": true}, nil, nil
			case "open", "goto", "back", "forward", "reload", "wait", "snapshot", "read", "find", "a11y", "screenshot", "click", "dblclick", "fill", "type", "press", "hover", "focus", "select", "check", "uncheck", "scroll", "scrollintoview", "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked", "cookies.list", "cookies.set", "cookies.clear", "storage.list", "storage.set", "storage.clear", "set.viewport", "set.device", "set.geo", "set.offline", "set.headers", "set.media", "set.user-agent", "eval", "console.list", "console.clear", "errors.list", "errors.clear", "network.requests", "network.request", "network.route", "network.unroute", "network.har", "upload", "downloads.list", "download.setdir":
				allowed, decision, decider, gateErr := oobRuntime.DecideAndConfirm(ctx, frame.Session, frame.Cmd, frameURL(frame), approvalTimeout())
				if gateErr != nil {
					return nil, nil, gateErr
				}
				if !allowed {
					return nil, nil, daemon.NewError(daemon.ErrorPeerDenied, fmt.Sprintf("policy decision %s denied %s", decision, frame.Cmd))
				}
				return journalRuntime.HandleWithDecider(ctx, frame, decider)
			case "state.save", "state.load", "state.list", "state.show", "state.clear", "state.clean":
				return stateRuntime.Handle(ctx, frame)
			case "auth.login":
				return authRuntime.Handle(ctx, frame)
			case "journal.tail", "journal.show", "trace.replay":
				return journalRuntime.HandleJournal(ctx, frame)
			case "policy.explain":
				return policyRuntime.Handle(ctx, frame)
			case "oob.status", "oob.complete", "oob.cancel", "handoff":
				return journalRuntime.HandleOOB(ctx, frame, oobRuntime.Handle)
			case "flow.record.start", "flow.record.stop", "flow.record.status":
				return navigation.Handle(ctx, frame)
			case "tab.list", "tab.new", "tab.switch", "tab.close", "window.new",
				"frame.tree", "frame.main", "frame.select",
				"dialog.status", "dialog.accept", "dialog.dismiss", "dialog.auto":
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
// domain allowlist: flag value, then SYMBROWSE_ALLOWED_DOMAINS, then the
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

// newStateStore builds the named-state store rooted at <state-dir>/states,
// probing the key resolver at construction time. When a key source (symvault,
// OS keychain, or environment variable) is available, state files are
// encrypted with AES-256-GCM; otherwise they are stored as plaintext.
// The default retention window comes from SYMBROWSE_STATE_EXPIRE_DAYS
// (default 30 days).
func newStateStore(cmd *cobra.Command) (*state.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.StateDir, "states")
	resolver := state.NewKeyResolver()
	var keys state.KeyProvider
	if source, err := resolver.Source(); err == nil && source != state.KeySourceNone {
		keys = resolver
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

// journalFor builds the session journal under <state-dir>/journal.
func journalFor(cfg *config.Config, session string) *journal.Journal {
	dir := filepath.Join(cfg.StateDir, "journal")
	j, err := journal.New(journal.Options{Dir: dir, Session: session})
	if err != nil {
		// A broken journal must never prevent the daemon from starting;
		// NewJournalRuntime(nil, ...) disables journaling.
		return nil
	}
	return j
}

// policyMode selects the default policy table: MCP mode when the daemon runs
// as an MCP backend (SYMBROWSE_MCP=1), TTY otherwise.
func policyMode() policy.Mode {
	if os.Getenv("SYMBROWSE_MCP") == "1" {
		return policy.ModeMCP
	}
	return policy.ModeTTY
}

// frameURL extracts the target URL from a frame's args for policy decisions.
func frameURL(frame daemon.Frame) string {
	var request struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(frame.Args, &request); err != nil {
		return ""
	}
	return request.URL
}

// approvalTimeout bounds an OOB approval wait. SYMBROWSE_APPROVAL_TIMEOUT
// overrides the default of 60 seconds; timeout always resolves to deny.
func approvalTimeout() time.Duration {
	if raw := os.Getenv("SYMBROWSE_APPROVAL_TIMEOUT"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 60 * time.Second
}

func daemonLifecycleRequest(ctx context.Context, session, command string, autostart bool) (daemon.Response, error) {
	if autostart {
		return request(ctx, session, command, nil)
	}
	return requestNoAutostart(ctx, session, command)
}

func writeDaemonResponse(cmd *cobra.Command, response daemon.Response, jsonOutput bool) error {
	return writeEnvelopeFromResponse(cmd, response, jsonOutput)
}
