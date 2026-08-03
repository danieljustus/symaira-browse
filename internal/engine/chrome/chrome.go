// Package chrome implements the Chrome DevTools Protocol engine.
package chrome

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	cdproto "github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/danieljustus/symaira-browse/internal/engine"
)

const (
	defaultStartupTimeout = 10 * time.Second
	defaultRequestTimeout = 10 * time.Second
)

// Options controls Chrome discovery and process isolation.
type Options struct {
	ExecutablePath string
	StartupTimeout time.Duration
	RequestTimeout time.Duration
	UserDataDir    string
	// AllowedDomains activates the domain allowlist network policy. Patterns
	// are bare hostnames, optionally prefixed with "*." (see internal/policy).
	// When non-empty, every request outside the allowlist is denied on the
	// CDP network path and WebRTC is disabled.
	AllowedDomains []string
	// SSRFEnabled activates the SSRF guard (issue #34): RFC1918, loopback,
	// link-local, .local, and IPv6-ULA targets are denied on the CDP network
	// path. It is the MCP-mode default; the regular daemon stays opt-in.
	SSRFEnabled bool
	// AllowPrivate relaxes the SSRF guard (--allow-private). Without it the
	// guard denies every private target.
	AllowPrivate bool
	// Headless launches Chrome in headless mode (no GUI session). Needed
	// for CI and agent contexts where a windowed Chrome cannot start.
	Headless bool
}

// Engine is a private Chrome process plus its CDP browser connection.
type Engine struct {
	options       Options
	mu            sync.Mutex
	cmd           *exec.Cmd
	conn          *rpcConnection
	dataDir       string
	removeDataDir bool
	closed        bool
	policy        *networkPolicy
	profileReused bool
}

// New creates an unlaunched Chrome engine.
func New(options Options) *Engine {
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = defaultStartupTimeout
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = defaultRequestTimeout
	}
	return &Engine{options: options}
}

// Launch starts Chrome with a private profile and an ephemeral DevTools port.
func (e *Engine) Launch(ctx context.Context) error {
	e.mu.Lock()
	if e.cmd != nil && e.conn != nil && !e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = false
	e.mu.Unlock()
	if e.options.ExecutablePath == "" {
		return errors.New("chrome executable path is empty")
	}
	policy, err := newNetworkPolicy(e.options.AllowedDomains, e.options.SSRFEnabled, e.options.AllowPrivate, e.call)
	if err != nil {
		return fmt.Errorf("parse network policy: %w", err)
	}
	dataDir := e.options.UserDataDir
	removeDataDir := false
	if dataDir == "" {
		var err error
		dataDir, err = os.MkdirTemp("", "symbrowse-chrome-")
		if err != nil {
			return fmt.Errorf("create private Chrome profile: %w", err)
		}
		removeDataDir = true
	} else {
		// A running Chrome holds a SingletonLock in the profile directory. In
		// that state the profile is not exclusively ours and the allowlist
		// cannot be guaranteed to cover its requests (see issue #42).
		for _, lock := range []string{"SingletonLock", "SingletonSocket"} {
			if _, statErr := os.Stat(filepath.Join(dataDir, lock)); statErr == nil {
				e.mu.Lock()
				e.profileReused = true
				e.mu.Unlock()
				break
			}
		}
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return fmt.Errorf("create Chrome profile: %w", err)
		}
	}
	args := chromeArgs(dataDir, policy.active(), e.options.Headless)
	cmd := exec.Command(e.options.ExecutablePath, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		if removeDataDir {
			_ = os.RemoveAll(dataDir)
		}
		return fmt.Errorf("start Chrome: %w", err)
	}
	fail := func(err error) error {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if removeDataDir {
			_ = os.RemoveAll(dataDir)
		}
		return err
	}
	startupCtx, cancel := context.WithTimeout(ctx, e.options.StartupTimeout)
	defer cancel()
	endpoint, err := waitForEndpoint(startupCtx, dataDir)
	if err != nil {
		return fail(fmt.Errorf("discover Chrome DevTools endpoint: %w", err))
	}
	conn, err := dial(startupCtx, endpoint, e.options.RequestTimeout)
	if err != nil {
		return fail(fmt.Errorf("connect Chrome DevTools: %w", err))
	}
	conn.addHandler(policy.handleEvent)
	e.mu.Lock()
	e.cmd, e.conn, e.dataDir, e.removeDataDir, e.policy = cmd, conn, dataDir, removeDataDir, policy
	e.mu.Unlock()
	return nil
}

func chromeArgs(dataDir string, disableWebRTC, headless bool) []string {
	args := []string{
		"--user-data-dir=" + dataDir,
		"--remote-debugging-port=0",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-domain-reliability",
		"--disable-features=OptimizationHints,MediaRouter",
		"--disable-sync",
		"about:blank",
	}
	if disableWebRTC {
		// WebRTC is not a regular HTTP request and cannot be intercepted via
		// the Fetch domain, so the allowlist disables it at the process level.
		args = append(args[:len(args)-1], "--disable-webrtc", "about:blank")
	}
	if headless {
		// Headless mode runs Chrome without a GUI session; required for
		// CI and agent contexts where the Mach bootstrap is restricted.
		args = append(args[:len(args)-1], "--headless=new", "about:blank")
	}
	return args
}

func waitForEndpoint(ctx context.Context, dataDir string) (string, error) {
	active := filepath.Join(dataDir, "DevToolsActivePort")
	for {
		contents, err := os.ReadFile(active)
		if err == nil {
			lines := strings.Fields(string(contents))
			if len(lines) >= 2 {
				port, parseErr := strconv.Atoi(lines[0])
				if parseErr == nil && port > 0 && port < 65536 {
					path := lines[1]
					endpoint := path
					dialPort := port
					if !strings.HasPrefix(path, "ws://") && !strings.HasPrefix(path, "wss://") {
						endpoint = fmt.Sprintf("ws://127.0.0.1:%d%s", port, path)
					} else if u, urlErr := url.Parse(path); urlErr == nil && u.Port() != "" {
						if p, atoiErr := strconv.Atoi(u.Port()); atoiErr == nil {
							dialPort = p
						}
					}
					// A reused session profile can carry a stale
					// DevToolsActivePort from a previous Chrome instance.
					// Verify the endpoint actually accepts connections and
					// keep waiting for a fresh file otherwise.
					conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", dialPort), 150*time.Millisecond)
					if dialErr == nil {
						_ = conn.Close()
						return endpoint, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (e *Engine) call(ctx context.Context, sessionID, method string, params, result any) error {
	e.mu.Lock()
	conn := e.conn
	closed := e.closed
	e.mu.Unlock()
	if conn == nil || closed {
		return errors.New("chrome engine is not launched")
	}
	requestCtx, cancel := context.WithTimeout(ctx, e.options.RequestTimeout)
	defer cancel()
	return cdp.Execute(cdp.WithExecutor(requestCtx, &sessionExecutor{conn: conn, sessionID: sessionID}), method, params, result)
}

// NewContext creates an isolated Chrome browser context.
func (e *Engine) NewContext(ctx context.Context) (engine.Context, error) {
	var result struct {
		BrowserContextID string `json:"browserContextId"`
	}
	if err := e.call(ctx, "", cdproto.CommandTargetCreateBrowserContext, struct{}{}, &result); err != nil {
		return engine.Context{}, err
	}
	if result.BrowserContextID == "" {
		return engine.Context{}, errors.New("chrome returned an empty browser context id")
	}
	return engine.Context{ID: result.BrowserContextID}, nil
}

// NewPage creates and attaches a page in an isolated context.
func (e *Engine) NewPage(ctx context.Context, browserContext engine.Context, url string) (engine.Page, error) {
	var target struct {
		TargetID string `json:"targetId"`
	}
	params := struct {
		URL              string `json:"url"`
		BrowserContextID string `json:"browserContextId,omitempty"`
	}{URL: url, BrowserContextID: browserContext.ID}
	if err := e.call(ctx, "", cdproto.CommandTargetCreateTarget, params, &target); err != nil {
		return engine.Page{}, err
	}
	if target.TargetID == "" {
		return engine.Page{}, errors.New("chrome returned an empty target id")
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	attach := struct {
		TargetID string `json:"targetId"`
		Flatten  bool   `json:"flatten"`
	}{TargetID: target.TargetID, Flatten: true}
	if err := e.call(ctx, "", cdproto.CommandTargetAttachToTarget, attach, &attached); err != nil {
		return engine.Page{}, err
	}
	if attached.SessionID == "" {
		return engine.Page{}, errors.New("chrome returned an empty CDP session id")
	}
	page := engine.Page{ID: target.TargetID, SessionID: attached.SessionID}
	for _, method := range []string{cdproto.CommandPageEnable, cdproto.CommandRuntimeEnable, cdproto.CommandDOMEnable, cdproto.CommandAccessibilityEnable, cdproto.CommandNetworkEnable} {
		if err := e.call(ctx, page.SessionID, method, struct{}{}, nil); err != nil {
			return engine.Page{}, fmt.Errorf("enable %s: %w", method, err)
		}
	}
	if e.policy != nil && e.policy.active() {
		if err := e.enableNetworkPolicy(ctx, page.SessionID); err != nil {
			return engine.Page{}, err
		}
	}
	return page, nil
}

// enableNetworkPolicy arms the CDP interception for one session and asks
// Chrome to auto-attach related targets (workers, cross-origin iframes,
// window.open popups) so the same policy covers their requests too.
func (e *Engine) enableNetworkPolicy(ctx context.Context, sessionID string) error {
	if err := e.call(ctx, sessionID, cdpCommandFetchEnable, fetchEnableParams{Patterns: []fetchPattern{{URLPattern: "*", RequestStage: "Request"}}}, nil); err != nil {
		return fmt.Errorf("enable network policy interception: %w", err)
	}
	if err := e.call(ctx, sessionID, cdpCommandTargetAutoAttach, targetAutoAttachParams{
		AutoAttach:             true,
		WaitForDebuggerOnStart: false,
		Flatten:                true,
		AutoAttachRelated:      true,
	}, nil); err != nil {
		return fmt.Errorf("enable network policy auto-attach: %w", err)
	}
	return nil
}

// Navigate navigates a page to url.
func (e *Engine) Navigate(ctx context.Context, page engine.Page, url string) (engine.NavigationResult, error) {
	if e.policy != nil && e.policy.active() {
		if allowed, reason := e.policy.allowsURL(url); !allowed {
			return engine.NavigationResult{}, fmt.Errorf("navigation to %q is blocked by the network policy (%s; active: %s)", url, reason, e.policy.describe())
		}
	}
	params := struct {
		URL string `json:"url"`
	}{URL: url}
	var result struct {
		FrameID   string `json:"frameId"`
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandPageNavigate, params, &result); err != nil {
		return engine.NavigationResult{}, err
	}
	return engine.NavigationResult{FrameID: result.FrameID, LoaderID: result.LoaderID, ErrorText: result.ErrorText}, nil
}

// Evaluate evaluates JavaScript with a value result.
func (e *Engine) Evaluate(ctx context.Context, page engine.Page, expression string) (engine.EvaluationResult, error) {
	params := struct {
		Expression    string `json:"expression"`
		ReturnByValue bool   `json:"returnByValue"`
		AwaitPromise  bool   `json:"awaitPromise"`
	}{expression, true, true}
	var result struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails,omitempty"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandRuntimeEvaluate, params, &result); err != nil {
		return engine.EvaluationResult{}, err
	}
	out := engine.EvaluationResult{Value: result.Result.Value, Type: result.Result.Type, Description: result.Result.Description}
	if result.ExceptionDetails != nil {
		out.ExceptionText = result.ExceptionDetails.Text
	}
	return out, nil
}

// AXTree returns the page accessibility tree as protocol-neutral JSON nodes.
func (e *Engine) AXTree(ctx context.Context, page engine.Page) ([]engine.AXNode, error) {
	var result struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandAccessibilityGetFullAXTree, struct{}{}, &result); err != nil {
		return nil, err
	}
	nodes := make([]engine.AXNode, 0, len(result.Nodes))
	for _, raw := range result.Nodes {
		nodes = append(nodes, engine.AXNode{Raw: raw})
	}
	return nodes, nil
}

// Screenshot captures a PNG screenshot of the page.
func (e *Engine) Screenshot(ctx context.Context, page engine.Page) ([]byte, error) {
	var result struct {
		Data string `json:"data"`
	}
	if err := e.call(ctx, page.SessionID, cdproto.CommandPageCaptureScreenshot, struct{}{}, &result); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return nil, fmt.Errorf("decode screenshot: %w", err)
	}
	return data, nil
}

// Close stops Chrome, closes the protocol connection, reaps the process, and
// removes the private profile. It is safe to call repeatedly.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	conn, cmd, dataDir, remove := e.conn, e.cmd, e.dataDir, e.removeDataDir
	e.conn, e.cmd = nil, nil
	e.mu.Unlock()
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), e.options.RequestTimeout)
		_ = conn.Execute(ctx, cdproto.CommandBrowserClose, struct{}{}, nil)
		cancel()
		_ = conn.Close()
	}
	var closeErr error
	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			closeErr = err
		}
		_, _ = cmd.Process.Wait()
	}
	if remove && dataDir != "" {
		if err := os.RemoveAll(dataDir); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

var _ engine.Engine = (*Engine)(nil)
var _ engine.NetworkPolicyReporter = (*Engine)(nil)

// BlockedRequests implements engine.NetworkPolicyReporter.
func (e *Engine) BlockedRequests() []engine.BlockedRequest {
	e.mu.Lock()
	policy := e.policy
	e.mu.Unlock()
	return policy.blockedRequests()
}

// Limitations implements engine.NetworkPolicyReporter. It reports startup
// configurations in which the domain allowlist cannot be fully enforced.
func (e *Engine) Limitations() []string {
	e.mu.Lock()
	policy := e.policy
	reused := e.profileReused
	e.mu.Unlock()
	if policy == nil || !policy.active() {
		return nil
	}
	var limitations []string
	if reused {
		limitations = append(limitations, "domain allowlist is not fully enforceable: reusing an existing Chrome profile; use a private profile for guaranteed enforcement")
	}
	return limitations
}

// sessionExecutor adapts a CDP session to chromedp's executor interface.
type sessionExecutor struct {
	conn      *rpcConnection
	sessionID string
}

func (s *sessionExecutor) Execute(ctx context.Context, method string, params, result any) error {
	return s.conn.execute(ctx, s.sessionID, method, params, result)
}
