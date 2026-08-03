package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/engine/chrome"
)

// NavigationRuntime lazily owns one protocol-neutral navigation service and
// Chrome engine per session. CDP details remain confined to engine/chrome.
type NavigationRuntime struct {
	mu         sync.Mutex
	registry   *SessionRegistry
	executable string
	services   map[string]*engine.NavigationService
	engines    map[string]engine.Engine
}

// NewNavigationRuntime creates a runtime. Chrome is not started until the
// first navigation or wait operation for a session.
func NewNavigationRuntime(registry *SessionRegistry, executable string) *NavigationRuntime {
	if executable == "" {
		executable = os.Getenv("SYMBROWSE_EXECUTABLE_PATH")
	}
	return &NavigationRuntime{registry: registry, executable: executable, services: make(map[string]*engine.NavigationService), engines: make(map[string]engine.Engine)}
}

// Handle executes one navigation frame and returns JSON-serializable data.
func (r *NavigationRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, nil, err
	}
	switch frame.Cmd {
	case "open", "goto":
		var request struct {
			URL string `json:"url"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if frame.Cmd == "open" {
			outcome, err := service.Open(ctx, request.URL)
			return outcome, nil, err
		}
		outcome, err := service.Goto(ctx, request.URL)
		return outcome, nil, err
	case "back":
		outcome, err := service.Back(ctx)
		return outcome, nil, err
	case "forward":
		outcome, err := service.Forward(ctx)
		return outcome, nil, err
	case "reload":
		outcome, err := service.Reload(ctx)
		return outcome, nil, err
	case "wait":
		var condition engine.WaitCondition
		if err := decodeArgs(frame, &condition); err != nil {
			return nil, nil, err
		}
		result, err := service.Wait(ctx, condition)
		return result, nil, err
	default:
		return nil, nil, fmt.Errorf("unknown navigation command %q", frame.Cmd)
	}
}

func (r *NavigationRuntime) service(ctx context.Context, session string) (*engine.NavigationService, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if service := r.services[session]; service != nil {
		return service, nil
	}
	if r.registry == nil {
		return nil, errors.New("session registry is required")
	}
	info, err := r.registry.Get(session)
	if err != nil {
		return nil, err
	}
	if r.executable == "" {
		return nil, errors.New("browser executable is not configured; set SYMBROWSE_EXECUTABLE_PATH")
	}
	browser := chrome.New(chrome.Options{ExecutablePath: r.executable, UserDataDir: info.UserDataDir})
	if err := browser.Launch(ctx); err != nil {
		return nil, err
	}
	browserContext, err := browser.NewContext(ctx)
	if err != nil {
		_ = browser.Close()
		return nil, err
	}
	page, err := browser.NewPage(ctx, browserContext, "about:blank")
	if err != nil {
		_ = browser.Close()
		return nil, err
	}
	service := engine.NewNavigationService(browser, page, engine.NavigationOptions{})
	r.engines[session] = browser
	r.services[session] = service
	_ = r.registry.SetActiveTabs(session, 1)
	return service, nil
}

// Close releases all per-session browser engines.
func (r *NavigationRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for session, browser := range r.engines {
		if err := browser.Close(); err != nil && first == nil {
			first = fmt.Errorf("close session %q: %w", session, err)
		}
	}
	r.engines = make(map[string]engine.Engine)
	r.services = make(map[string]*engine.NavigationService)
	return first
}

func decodeArgs(frame Frame, target any) error {
	if len(frame.Args) == 0 || string(frame.Args) == "null" {
		return errors.New("command arguments are required")
	}
	if err := json.Unmarshal(frame.Args, target); err != nil {
		return fmt.Errorf("decode %s arguments: %w", frame.Cmd, err)
	}
	return nil
}
