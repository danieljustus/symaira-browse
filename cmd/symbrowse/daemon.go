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
	var cdpEndpoint string
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "daemon",
		Short:   "Run or inspect the symbrowse daemon",
		Args:    cobra.NoArgs,
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
	command.Flags().StringVar(&engineKind, "engine", "chrome", "engine implementation: chrome (default), static (JS-free HTML reader), safari-attach (live Safari session via Apple Events), or safari-bidi (isolated Safari via safaridriver --bidi)")
	command.Flags().StringVar(&cdpEndpoint, "cdp-endpoint", "", "attach to an existing DevTools endpoint (e.g. http://127.0.0.1:9222) instead of launching Chrome; also via SYMBROWSE_CDP_ENDPOINT or config.toml")

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
			return writeDaemonResponse(cmd, response)
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
			return writeDaemonResponse(cmd, response)
		},
	}
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
	cfg, configErr := config.Load()
	if configErr != nil {
		return configErr
	}
	// The effective network policy is resolved once and then shared by the
	// engine runtime and daemon.status. Resolving it twice let status report
	// defaults while the runtime enforced the real policy (issue #370).
	networkPolicy := resolveDaemonPolicy(cmd, cfg)
	allowedDomains := networkPolicy.AllowedDomains
	ssrfEnabled := networkPolicy.SSRFEnabled
	allowPrivate := networkPolicy.AllowPrivate
	headless := false
	if raw := cmd.Flags().Lookup("headless"); raw != nil {
		headless, _ = cmd.Flags().GetBool("headless")
	}
	headless = headless || cfg.Headless
	engineKind := "chrome"
	if raw := cmd.Flags().Lookup("engine"); raw != nil {
		engineKind, _ = cmd.Flags().GetString("engine")
	}
	// Attach endpoint precedence (issue #296): flag → SYMBROWSE_CDP_ENDPOINT
	// → config.toml. An attached engine does not launch or own Chrome.
	cdpEndpoint := ""
	if raw := cmd.Flags().Lookup("cdp-endpoint"); raw != nil {
		cdpEndpoint, _ = cmd.Flags().GetString("cdp-endpoint")
	}
	if cdpEndpoint == "" {
		cdpEndpoint = cfg.CDPEndpoint
	}

	path, err := daemon.SocketPath(session)
	if err != nil {
		return err
	}
	idle := time.Duration(cfg.IdleTimeoutSeconds) * time.Second
	if cfg.IdleTimeoutSeconds == 0 {
		idle = -1
	}
	operation := time.Duration(cfg.OperationTimeoutSeconds) * time.Second
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
	autosave, err := autosaveFromConfig(cfg, restoreOnStart[session])
	if err != nil {
		return err
	}

	// Screenshots (issue #16) are written into the cache out directory by
	// default; --screenshot-dir on the command expands the allowed roots.
	screenshotDirs := []string{filepath.Join(cfg.CacheDir, "out")}
	navigation := daemon.NewNavigationRuntime(registry, cfg.ExecutablePath, daemon.NavigationRuntimeOptions{
		AllowedDomains: allowedDomains,
		SSRFEnabled:    ssrfEnabled,
		AllowPrivate:   allowPrivate,
		Headless:       headless,
		StateStore:     stateStore,
		RestoreOnStart: restoreOnStart,
		Autosave:       autosave,
		Profile:        profile,
		UploadDirs:     cfg.UploadDirs,
		ScreenshotDirs: screenshotDirs,
		Engine:         engineKind,
		CDPEndpoint:    cdpEndpoint,
		Mode:           policyMode(),
	})
	defer func() { _ = navigation.Close() }()
	stateRuntime := daemon.NewStateRuntime(stateStore, navigation)
	stateRuntime.ReportExpired()
	authRuntime := daemon.NewAuthRuntime(navigation, nil)
	journalRuntime := daemon.NewJournalRuntime(journalFor(cfg, session), navigation)
	policyRuntime := daemon.NewPolicyRuntime(cfg.StateDir, policyMode())
	oobRuntime := daemon.NewOOBRuntime(oob.NewManager(), oob.NewNotifier(), navigation, policyRuntime.Policy(), policyMode())
	// guard delegation (issue #52): the guard decides when it is present
	// and configured; the decider lands in the journal for every action.
	// A missing decider is never silent (issue #299): if no guard binary is
	// found, decisions fall back to the built-in policy and that fallback is
	// logged at startup.
	oobRuntime.SetDecider(policyRuntime.Decide)
	if policyRuntime.Guard() != nil && policyRuntime.Guard().Active() {
		slog.Info("guard delegation active", "command", policyRuntime.Guard().Command())
	} else {
		slog.Warn("no external risk decider available (symbrain not found or disabled); risk decisions fall back to the built-in policy")
	}
	// Fetch runtime (issue #258): serves the SymFetch compatibility frames
	// (fetch.url, fetch.batch, wayback.snapshots) over plain HTTP without a
	// browser session. SSRF policy mirrors the daemon flags.
	fetchRuntime, err := daemon.NewFetchRuntime(daemon.FetchRuntimeOptions{
		AllowPrivate: allowPrivate,
		Robots:       true,
		CacheDir:     filepath.Join(cfg.CacheDir, "fetch"),
		CacheTTL:     time.Duration(cfg.CacheTTLHours) * time.Hour,
	})
	if err != nil {
		return err
	}
	defer func() { _ = fetchRuntime.Close() }()
	server := daemon.NewServer(daemon.Options{
		SocketPath:       path,
		Session:          session,
		Registry:         registry,
		IdleTimeout:      idle,
		OperationTimeout: operation,
		CacheDir:         filepath.Join(cfg.CacheDir, "out"),
		CacheTTL:         time.Duration(cfg.CacheTTLHours) * time.Hour,
		Policy:           networkPolicy,
		Handler: func(ctx context.Context, frame daemon.Frame) (any, []daemon.Warning, error) {
			switch frame.Cmd {
			case "fetch.url", "fetch.batch", "wayback.snapshots":
				return fetchRuntime.Handle(ctx, frame)
			case "daemon.ping":
				return map[string]any{"pong": true}, nil, nil
			case "open", "goto", "back", "forward", "reload", "wait", "snapshot", "read", "find", "a11y", "screenshot", "click", "dblclick", "fill", "type", "press", "hover", "focus", "select", "check", "uncheck", "scroll", "scrollintoview", "get.text", "get.html", "get.value", "get.attr", "get.title", "get.url", "get.count", "get.box", "get.styles", "is.visible", "is.enabled", "is.checked", "cookies.list", "cookies.set", "cookies.clear", "storage.list", "storage.set", "storage.clear", "set.viewport", "set.device", "set.geo", "set.offline", "set.headers", "set.media", "set.user-agent", "eval", "console.list", "console.clear", "errors.list", "errors.clear", "network.requests", "network.request", "network.route", "network.unroute", "network.har", "upload", "downloads.list", "download.setdir":
				allowed, decision, decider, gateErr := oobRuntime.DecideAndConfirm(ctx, frame.Session, frame.Cmd, frameURL(frame), approvalTimeoutFromConfig(cfg))
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
	if errors.Is(err, daemon.ErrDaemonAlreadyRunning) {
		// Another daemon won the startup race for this session and owns the
		// socket. Exiting quietly lets the caller connect to the winner
		// instead of leaving two daemons with split browser state (#371).
		slog.Info("daemon already running for this session; connecting clients will use the existing one", "session", session, "socket", path)
		return nil
	}
	return err
}

// resolveAllowedDomains applies the configuration precedence chain for the
// domain allowlist: flag value, then SYMBROWSE_ALLOWED_DOMAINS, then the
// allowed_domains setting from config.toml.
func resolveAllowedDomains(flagValue string) []string {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	return resolveAllowedDomainsWithConfig(flagValue, cfg)
}

func resolveAllowedDomainsWithConfig(flagValue string, cfg *config.Config) []string {
	if strings.TrimSpace(flagValue) != "" {
		return splitDomains(flagValue)
	}
	if cfg == nil || len(cfg.AllowedDomains) == 0 {
		return nil
	}
	return cfg.AllowedDomains
}

// resolveDaemonPolicy resolves the effective network policy of a daemon run
// from flags, environment, and config. It is the single source of truth for
// both the engine runtime and the daemon.status payload so the reported
// policy can never drift from the enforced one (issue #370).
func resolveDaemonPolicy(cmd *cobra.Command, cfg *config.Config) daemon.PolicyStatus {
	allowedDomainsFlag, _ := cmd.Flags().GetString("allowed-domains")
	ssrfFlag, _ := cmd.Flags().GetBool("ssrf")
	allowPrivateFlag, _ := cmd.Flags().GetBool("allow-private")
	return daemon.PolicyStatus{
		AllowedDomains: resolveAllowedDomainsWithConfig(allowedDomainsFlag, cfg),
		SSRFEnabled:    resolveBoolPolicyWithConfig(ssrfFlag, func(cfg *config.Config) bool { return cfg.SSRFEnabled }, cfg),
		AllowPrivate:   resolveBoolPolicyWithConfig(allowPrivateFlag, func(cfg *config.Config) bool { return cfg.AllowPrivate }, cfg),
	}
}

// resolveBoolPolicy applies the same precedence chain for boolean policy
// options (SSRF guard, allow-private): flag, then environment variable, then
// the config.toml setting. Environment values follow strconv.ParseBool
// semantics ("1", "t", "true", "0", "f", "false").
func resolveBoolPolicy(envName string, flagValue bool, fromConfig func(*config.Config) bool) bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return resolveBoolPolicyWithConfig(flagValue, fromConfig, cfg)
}

func resolveBoolPolicyWithConfig(flagValue bool, fromConfig func(*config.Config) bool, cfg *config.Config) bool {
	if flagValue {
		return true
	}
	if cfg == nil {
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
	return newStateStoreWithResolver(cmd, state.NewKeyResolver())
}

func newStateStoreWithResolver(_ *cobra.Command, resolver state.KeyProvider) (*state.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.StateDir, "states")
	var keys state.KeyProvider
	if resolver != nil {
		source, resolveErr := resolver.Source()
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve state encryption key: %w", resolveErr)
		}
		if source != state.KeySourceNone {
			keys = resolver
		}
	}
	expireIn := time.Duration(cfg.StateExpireDays) * 24 * time.Hour
	if cfg.StateExpireDays == 0 {
		expireIn = time.Duration(state.DefaultExpireDays) * 24 * time.Hour
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
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return autosaveFromConfig(cfg, restoreKey)
}

func autosaveFromConfig(cfg *config.Config, restoreKey string) (*daemon.AutosaveConfig, error) {
	key := restoreKey
	if cfg.AutosaveKey != "" {
		key = cfg.AutosaveKey
	}
	autosave := &daemon.AutosaveConfig{Policy: daemon.AutosavePolicy(cfg.AutosavePolicy), Interval: time.Duration(cfg.AutosaveIntervalSeconds) * time.Second, Key: key}
	if err := autosave.Validate(); err != nil {
		return nil, err
	}
	return autosave, nil
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
	cfg, err := config.Load()
	if err != nil {
		return 60 * time.Second
	}
	return approvalTimeoutFromConfig(cfg)
}

func approvalTimeoutFromConfig(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.ApprovalTimeoutSeconds > 0 {
		return time.Duration(cfg.ApprovalTimeoutSeconds) * time.Second
	}
	return 60 * time.Second
}

func daemonLifecycleRequest(ctx context.Context, session, command string, autostart bool) (daemon.Response, error) {
	if autostart {
		return daemonRequest(ctx, session, command, nil)
	}
	return requestNoAutostart(ctx, session, command)
}

func writeDaemonResponse(cmd *cobra.Command, response daemon.Response) error {
	return writeEnvelopeFromResponse(cmd, response)
}
