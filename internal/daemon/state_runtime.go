package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/state"
)

// StateRuntime bridges the state store and the per-session navigation
// services: it captures and restores cookies/storage and reports store
// metadata. It owns no browser state itself.
type StateRuntime struct {
	store *state.Store
	nav   *NavigationRuntime
}

// NewStateRuntime creates a state bridge for one store.
func NewStateRuntime(store *state.Store, nav *NavigationRuntime) *StateRuntime {
	return &StateRuntime{store: store, nav: nav}
}

// Store exposes the underlying store (for metadata commands).
func (r *StateRuntime) Store() *state.Store { return r.store }

// Save captures the session's cookies and storage into a named state.
func (r *StateRuntime) Save(ctx context.Context, session, name string) (state.Metadata, error) {
	if err := state.ValidateName(name); err != nil {
		return state.Metadata{}, err
	}
	service, err := r.nav.service(ctx, session)
	if err != nil {
		return state.Metadata{}, err
	}
	cookies, storage, err := service.CaptureCookiesAndStorage(ctx)
	if err != nil {
		return state.Metadata{}, fmt.Errorf("capture session state: %w", err)
	}
	origins := make(map[string]state.OriginState, len(storage))
	for origin, kinds := range storage {
		origins[origin] = state.OriginState{
			Cookies:        cookiesForOrigin(cookies, origin),
			LocalStorage:   kinds[engine.StorageLocal],
			SessionStorage: kinds[engine.StorageSession],
		}
	}
	// Cookies may not carry an origin-keyed map; attach them to the current
	// origin when no storage entry exists for it.
	if len(origins) == 0 && len(cookies) > 0 {
		origin, originErr := service.Origin(ctx)
		if originErr == nil && origin != "" {
			origins[origin] = state.OriginState{Cookies: cookies}
		}
	}
	st := &state.State{
		SchemaVersion: state.SchemaVersion,
		Name:          name,
		KeySource:     string(r.store.KeySource()),
		Origins:       origins,
	}
	if err := r.store.Save(st); err != nil {
		return state.Metadata{}, err
	}
	return st.Metadata(), nil
}

// Load restores a named state into the session browser.
func (r *StateRuntime) Load(ctx context.Context, session, name string) (state.Metadata, []string, error) {
	if err := state.ValidateName(name); err != nil {
		return state.Metadata{}, nil, err
	}
	st, err := r.store.Load(name)
	if err != nil {
		return state.Metadata{}, nil, err
	}
	service, err := r.nav.service(ctx, session)
	if err != nil {
		return state.Metadata{}, nil, err
	}
	var warnings []string
	for origin, entry := range st.Origins {
		restoreWarnings, err := restoreOrigin(ctx, service, origin, entry)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", origin, err))
			continue
		}
		warnings = append(warnings, restoreWarnings...)
	}
	return st.Metadata(), warnings, nil
}

// restoreOrigin navigates to the origin once, then applies cookies and
// storage scoped to it. Cookies are applied with their own domain scope via
// the CDP network domain; storage is written per origin.
func restoreOrigin(ctx context.Context, service *engine.NavigationService, origin string, entry state.OriginState) ([]string, error) {
	var warnings []string
	for _, cookie := range entry.Cookies {
		if err := service.SetCookie(ctx, cookie, cookieURLForOrigin(origin, cookie)); err != nil {
			warnings = append(warnings, fmt.Sprintf("cookie %q: %v", cookie.Name, err))
		}
	}
	storage := map[engine.StorageKind]map[string]string{
		engine.StorageLocal:   entry.LocalStorage,
		engine.StorageSession: entry.SessionStorage,
	}
	restoreWarnings, err := service.RestoreCookiesAndStorage(ctx, nil, map[string]map[engine.StorageKind]map[string]string{origin: storage})
	if err != nil {
		return warnings, err
	}
	return append(warnings, restoreWarnings...), nil
}

// cookieURLForOrigin builds a URL scope for a cookie on the given origin.
func cookieURLForOrigin(origin string, cookie engine.Cookie) string {
	if cookie.Domain == "" {
		return origin
	}
	domain := cookie.Domain
	if len(domain) > 0 && domain[0] == '.' {
		domain = domain[1:]
	}
	scheme := "http"
	if cookie.Secure {
		scheme = "https"
	}
	path := cookie.Path
	if path == "" {
		path = "/"
	}
	return fmt.Sprintf("%s://%s%s", scheme, domain, path)
}

// cookiesForOrigin filters cookies whose domain matches the origin host.
func cookiesForOrigin(cookies []engine.Cookie, origin string) []engine.Cookie {
	host := originHost(origin)
	var result []engine.Cookie
	for _, cookie := range cookies {
		domain := cookie.Domain
		if domain == "" {
			result = append(result, cookie)
			continue
		}
		if domain[0] == '.' {
			domain = domain[1:]
		}
		if domain == host || hostHasSuffix(host, "."+domain) {
			result = append(result, cookie)
		}
	}
	return result
}

func originHost(origin string) string {
	rest := origin
	if len(rest) > 7 && rest[:7] == "http://" {
		rest = rest[7:]
	} else if len(rest) > 8 && rest[:8] == "https://" {
		rest = rest[8:]
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == ':' || rest[i] == '/' {
			return rest[:i]
		}
	}
	return rest
}

func hostHasSuffix(host, suffix string) bool {
	return len(host) >= len(suffix) && host[len(host)-len(suffix):] == suffix
}

// Handle executes state frames. Metadata-only commands never touch the
// browser; save/load require a session service.
func (r *StateRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	switch frame.Cmd {
	case "state.save":
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		meta, err := r.Save(ctx, frame.Session, request.Name)
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"saved": meta.Name, "metadata": meta}, nil, nil
	case "state.load":
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		meta, warnings, err := r.Load(ctx, frame.Session, request.Name)
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"loaded": meta.Name, "metadata": meta}, protocolWarnings(warnings), nil
	case "state.list":
		names, err := r.store.List()
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"schema_version": state.SchemaVersion, "states": names}, nil, nil
	case "state.show":
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		meta, err := r.store.Metadata(request.Name)
		if err != nil {
			return nil, nil, err
		}
		return meta, nil, nil
	case "state.clear":
		var request struct {
			Name string `json:"name"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if err := r.store.Remove(request.Name); err != nil {
			return nil, nil, err
		}
		return map[string]any{"cleared": request.Name}, nil, nil
	case "state.clean":
		var request struct {
			OlderThanDays int `json:"older_than_days,omitempty"`
		}
		_ = decodeOptionalArgs(frame, &request)
		var removed []string
		var err error
		if request.OlderThanDays > 0 {
			removed, err = r.store.CleanOlderThan(time.Duration(request.OlderThanDays) * 24 * time.Hour)
		} else {
			removed, err = r.store.Clean()
		}
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"removed": removed}, nil, nil
	default:
		return nil, nil, errors.New("unknown state command")
	}
}

func protocolWarnings(messages []string) []Warning {
	var warnings []Warning
	for _, message := range messages {
		warnings = append(warnings, Warning{Kind: "state.restore", Severity: "warning", Message: message})
	}
	return warnings
}

// ReportExpired logs expired states at daemon startup without touching them.
func (r *StateRuntime) ReportExpired() {
	expired, err := r.store.Expired()
	if err != nil {
		slog.Debug("state store not available", "error", err)
		return
	}
	for _, name := range expired {
		slog.Warn("state is expired", "state", name)
	}
}

// AutosavePolicy controls when session state is persisted automatically.
type AutosavePolicy string

const (
	AutosaveAuto   AutosavePolicy = "auto"   // save at most once per interval
	AutosaveAlways AutosavePolicy = "always" // save after every change
	AutosaveNever  AutosavePolicy = "never"  // never save automatically
)

// AutosaveConfig wires the daemon's autosave behaviour.
type AutosaveConfig struct {
	Policy   AutosavePolicy
	Interval time.Duration
	Key      string // named state to write; empty disables autosave
}

// Validate normalizes the policy and interval.
func (c *AutosaveConfig) Validate() error {
	switch c.Policy {
	case "", AutosaveAuto, AutosaveAlways, AutosaveNever:
	default:
		return fmt.Errorf("invalid autosave policy %q", c.Policy)
	}
	if c.Policy == "" {
		c.Policy = AutosaveAuto
	}
	if c.Interval < 0 {
		return errors.New("autosave interval cannot be negative")
	}
	return nil
}
