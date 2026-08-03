package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultNavigationTimeout = 25 * time.Second
	defaultNavigationPoll    = 25 * time.Millisecond
	defaultNetworkIdle       = 500 * time.Millisecond
)

// NavigationState is the protocol-neutral state observed after a navigation or
// while evaluating a wait condition. HTTPStatus is zero when Chrome cannot
// expose a response status (for example, for about:blank).
type NavigationState struct {
	URL         string `json:"url"`
	HTTPStatus  int    `json:"http_status"`
	ReadyState  string `json:"ready_state"`
	NetworkIdle bool   `json:"network_idle"`
}

// NavigationOutcome is the stable result returned by navigation commands.
type NavigationOutcome struct {
	Action     string `json:"action"`
	URL        string `json:"url"`
	HTTPStatus int    `json:"http_status"`
}

// WaitKind identifies the supported wait forms.
type WaitKind string

const (
	WaitSelector     WaitKind = "selector"
	WaitMilliseconds WaitKind = "milliseconds"
	WaitText         WaitKind = "text"
	WaitURL          WaitKind = "url"
	WaitLoad         WaitKind = "load"
)

// SelectorState controls the meaning of a selector wait.
type SelectorState string

const (
	SelectorVisible  SelectorState = "visible"
	SelectorHidden   SelectorState = "hidden"
	SelectorAttached SelectorState = "attached"
	SelectorDetached SelectorState = "detached"
)

// LoadState controls the meaning of a load wait.
type LoadState string

const (
	LoadComplete       LoadState = "load"
	LoadDOMContentLoad LoadState = "domcontentloaded"
	LoadNetworkIdle    LoadState = "networkidle"
)

// WaitCondition describes one wait operation. Value is used by selector, text,
// and URL waits; SelectorState is used only for selector waits; Duration is
// used only for millisecond waits; and LoadState is used only for load waits.
type WaitCondition struct {
	Kind          WaitKind      `json:"kind"`
	Value         string        `json:"value,omitempty"`
	SelectorState SelectorState `json:"state,omitempty"`
	LoadState     LoadState     `json:"load_state,omitempty"`
	Duration      time.Duration `json:"duration,omitempty"`
}

// WaitResult is returned when a wait condition matches.
type WaitResult struct {
	Awaited  string          `json:"awaited"`
	Observed NavigationState `json:"observed"`
}

// WaitTimeoutError reports both the condition that was awaited and the last
// state observed. Its message is deliberately stable for CLI and JSON callers.
type WaitTimeoutError struct {
	Awaited  string
	Observed NavigationState
	Timeout  time.Duration
}

func (e *WaitTimeoutError) Error() string {
	return fmt.Sprintf("wait timed out after %s: awaited %s; observed url=%q ready_state=%q http_status=%d network_idle=%t", e.Timeout, e.Awaited, e.Observed.URL, e.Observed.ReadyState, e.Observed.HTTPStatus, e.Observed.NetworkIdle)
}

// NavigationStateProvider is an optional engine extension. It lets a concrete
// engine obtain response metadata without exposing protocol types at this
// package boundary. The navigation service falls back to Runtime evaluation
// when it is not implemented, keeping injected engines small and testable.
type NavigationStateProvider interface {
	NavigationState(context.Context, Page) (NavigationState, error)
}

// NavigationService implements navigation and waiting over the protocol-neutral
// Engine interface. It intentionally owns no session or daemon state.
type NavigationService struct {
	engine         Engine
	page           Page
	timeout        time.Duration
	pollInterval   time.Duration
	networkIdleFor time.Duration
}

// NavigationOptions controls polling and operation timeouts.
type NavigationOptions struct {
	Timeout        time.Duration
	PollInterval   time.Duration
	NetworkIdleFor time.Duration
}

// NewNavigationService creates a controller for one page.
func NewNavigationService(browser Engine, page Page, options NavigationOptions) *NavigationService {
	if options.Timeout <= 0 {
		options.Timeout = defaultNavigationTimeout
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultNavigationPoll
	}
	if options.NetworkIdleFor <= 0 {
		options.NetworkIdleFor = defaultNetworkIdle
	}
	return &NavigationService{engine: browser, page: page, timeout: options.Timeout, pollInterval: options.PollInterval, networkIdleFor: options.NetworkIdleFor}
}

// Open navigates to url. Goto is an explicit alias for Open.
func (s *NavigationService) Open(ctx context.Context, url string) (NavigationOutcome, error) {
	return s.navigate(ctx, "open", url)
}

// Goto navigates to url.
func (s *NavigationService) Goto(ctx context.Context, url string) (NavigationOutcome, error) {
	return s.navigate(ctx, "goto", url)
}

// Back moves one entry back in the page history.
func (s *NavigationService) Back(ctx context.Context) (NavigationOutcome, error) {
	return s.history(ctx, "back", "history.back()")
}

// Forward moves one entry forward in the page history.
func (s *NavigationService) Forward(ctx context.Context) (NavigationOutcome, error) {
	return s.history(ctx, "forward", "history.forward()")
}

// Reload reloads the current page.
func (s *NavigationService) Reload(ctx context.Context) (NavigationOutcome, error) {
	return s.history(ctx, "reload", "location.reload()")
}

func (s *NavigationService) navigate(ctx context.Context, action, url string) (NavigationOutcome, error) {
	if strings.TrimSpace(url) == "" {
		return NavigationOutcome{}, errors.New("navigation URL is required")
	}
	if _, err := s.engine.Navigate(ctx, s.page, url); err != nil {
		return NavigationOutcome{}, fmt.Errorf("%s %q: %w", action, url, err)
	}
	return s.finishNavigation(ctx, action)
}

func (s *NavigationService) history(ctx context.Context, action, expression string) (NavigationOutcome, error) {
	result, err := s.engine.Evaluate(ctx, s.page, expression)
	if err != nil {
		return NavigationOutcome{}, fmt.Errorf("%s: %w", action, err)
	}
	if result.ExceptionText != "" {
		return NavigationOutcome{}, fmt.Errorf("%s: %s", action, result.ExceptionText)
	}
	return s.finishNavigation(ctx, action)
}

func (s *NavigationService) finishNavigation(ctx context.Context, action string) (NavigationOutcome, error) {
	if _, err := s.Wait(ctx, WaitCondition{Kind: WaitLoad, LoadState: LoadComplete}); err != nil {
		return NavigationOutcome{}, err
	}
	state, err := s.state(ctx)
	if err != nil {
		return NavigationOutcome{}, fmt.Errorf("read final navigation state: %w", err)
	}
	return NavigationOutcome{Action: action, URL: state.URL, HTTPStatus: state.HTTPStatus}, nil
}

// Wait waits for one condition until the configured timeout or ctx is done.
func (s *NavigationService) Wait(ctx context.Context, condition WaitCondition) (WaitResult, error) {
	if err := validateWaitCondition(condition); err != nil {
		return WaitResult{}, err
	}
	if condition.Kind == WaitMilliseconds {
		return s.waitDuration(ctx, condition)
	}

	waitCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var observed NavigationState
	var networkIdleSince time.Time
	for {
		var matched bool
		var err error
		matched, observed, err = s.observeCondition(waitCtx, condition)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return WaitResult{}, err
		}
		if condition.Kind == WaitLoad && condition.LoadState == LoadNetworkIdle {
			if matched {
				if networkIdleSince.IsZero() {
					networkIdleSince = time.Now()
				}
				matched = time.Since(networkIdleSince) >= s.networkIdleFor
			} else {
				networkIdleSince = time.Time{}
			}
		}
		if matched {
			return WaitResult{Awaited: describeWait(condition), Observed: observed}, nil
		}
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.Canceled) && !errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
				return WaitResult{}, waitCtx.Err()
			}
			return WaitResult{}, &WaitTimeoutError{Awaited: describeWait(condition), Observed: observed, Timeout: s.timeout}
		case <-time.After(s.pollInterval):
		}
	}
}

func (s *NavigationService) waitDuration(ctx context.Context, condition WaitCondition) (WaitResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	timer := time.NewTimer(condition.Duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		state, err := s.state(ctx)
		if err != nil {
			return WaitResult{}, fmt.Errorf("read state after %s: %w", describeWait(condition), err)
		}
		return WaitResult{Awaited: describeWait(condition), Observed: state}, nil
	case <-waitCtx.Done():
		state, _ := s.state(context.Background())
		return WaitResult{}, &WaitTimeoutError{Awaited: describeWait(condition), Observed: state, Timeout: s.timeout}
	}
}

func (s *NavigationService) observeCondition(ctx context.Context, condition WaitCondition) (bool, NavigationState, error) {
	if condition.Kind == WaitURL || condition.Kind == WaitLoad {
		state, err := s.state(ctx)
		if err != nil {
			return false, NavigationState{}, err
		}
		if condition.Kind == WaitURL {
			return matchGlob(condition.Value, state.URL), state, nil
		}
		return loadStateMatches(condition.LoadState, state), state, nil
	}

	expression := conditionExpression(condition)
	result, err := s.engine.Evaluate(ctx, s.page, expression)
	if err != nil {
		return false, NavigationState{}, err
	}
	if result.ExceptionText != "" {
		return false, NavigationState{}, errors.New(result.ExceptionText)
	}
	var observation struct {
		Matched     bool   `json:"matched"`
		URL         string `json:"url"`
		HTTPStatus  int    `json:"http_status"`
		ReadyState  string `json:"ready_state"`
		NetworkIdle bool   `json:"network_idle"`
	}
	if err := json.Unmarshal(result.Value, &observation); err != nil {
		return false, NavigationState{}, fmt.Errorf("decode wait observation: %w", err)
	}
	return observation.Matched, NavigationState{URL: observation.URL, HTTPStatus: observation.HTTPStatus, ReadyState: observation.ReadyState, NetworkIdle: observation.NetworkIdle}, nil
}

func (s *NavigationService) state(ctx context.Context) (NavigationState, error) {
	if provider, ok := s.engine.(NavigationStateProvider); ok {
		return provider.NavigationState(ctx, s.page)
	}
	result, err := s.engine.Evaluate(ctx, s.page, navigationStateExpression)
	if err != nil {
		return NavigationState{}, err
	}
	if result.ExceptionText != "" {
		return NavigationState{}, errors.New(result.ExceptionText)
	}
	var state NavigationState
	if err := json.Unmarshal(result.Value, &state); err != nil {
		return NavigationState{}, fmt.Errorf("decode navigation state: %w", err)
	}
	return state, nil
}

func validateWaitCondition(condition WaitCondition) error {
	switch condition.Kind {
	case WaitSelector:
		if strings.TrimSpace(condition.Value) == "" {
			return errors.New("wait selector is required")
		}
		switch condition.SelectorState {
		case SelectorVisible, SelectorHidden, SelectorAttached, SelectorDetached:
		default:
			return fmt.Errorf("invalid selector wait state %q", condition.SelectorState)
		}
	case WaitMilliseconds:
		if condition.Duration < 0 {
			return errors.New("wait duration cannot be negative")
		}
	case WaitText, WaitURL:
		if condition.Value == "" {
			return fmt.Errorf("wait %s value is required", condition.Kind)
		}
	case WaitLoad:
		switch condition.LoadState {
		case LoadComplete, LoadDOMContentLoad, LoadNetworkIdle:
		default:
			return fmt.Errorf("invalid load wait state %q", condition.LoadState)
		}
	default:
		return fmt.Errorf("invalid wait kind %q", condition.Kind)
	}
	return nil
}

func conditionExpression(condition WaitCondition) string {
	value, _ := json.Marshal(condition.Value)
	var predicate string
	switch condition.Kind {
	case WaitSelector:
		state, _ := json.Marshal(string(condition.SelectorState))
		predicate = fmt.Sprintf(`(function(){const e=document.querySelector(%s); if (%s === "attached") return !!e; if (%s === "detached") return !e; if (!e) return %t; const s=getComputedStyle(e), r=e.getBoundingClientRect(); const visible=s.display!=="none" && s.visibility!=="hidden" && s.opacity!=="0" && r.width>0 && r.height>0; return %s === "visible" ? visible : !visible;})()`, value, state, state, condition.SelectorState == SelectorHidden, state)
	case WaitText:
		predicate = fmt.Sprintf(`(document.body ? (document.body.innerText || document.body.textContent || "") : "").includes(%s)`, value)
	}
	return fmt.Sprintf(`(function(){const nav=performance.getEntriesByType("navigation")[0]; return {matched:!!(%s),url:location.href,http_status:nav && nav.responseStatus || 0,ready_state:document.readyState,network_idle:document.readyState === "complete"};})()`, predicate)
}

const navigationStateExpression = `(function(){const nav=performance.getEntriesByType("navigation")[0]; return {url:location.href,http_status:nav && nav.responseStatus || 0,ready_state:document.readyState,network_idle:document.readyState === "complete"};})()`

func loadStateMatches(state LoadState, observed NavigationState) bool {
	switch state {
	case LoadComplete:
		return observed.ReadyState == "complete"
	case LoadDOMContentLoad:
		return observed.ReadyState == "interactive" || observed.ReadyState == "complete"
	case LoadNetworkIdle:
		return observed.NetworkIdle
	default:
		return false
	}
}

func describeWait(condition WaitCondition) string {
	switch condition.Kind {
	case WaitSelector:
		return fmt.Sprintf("selector %q to be %s", condition.Value, condition.SelectorState)
	case WaitMilliseconds:
		return fmt.Sprintf("%d milliseconds", condition.Duration/time.Millisecond)
	case WaitText:
		return fmt.Sprintf("text %q", condition.Value)
	case WaitURL:
		return fmt.Sprintf("URL matching glob %q", condition.Value)
	case WaitLoad:
		return fmt.Sprintf("load state %q", condition.LoadState)
	default:
		return string(condition.Kind)
	}
}

func matchGlob(pattern, value string) bool {
	p, v := []rune(pattern), []rune(value)
	previous := make([]bool, len(v)+1)
	previous[0] = true
	for i := range p {
		pc := p[i]
		current := make([]bool, len(v)+1)
		if pc == '*' {
			current[0] = previous[0]
			for j := 1; j <= len(v); j++ {
				current[j] = current[j-1] || previous[j]
			}
		} else {
			for j, vc := range v {
				if pc == '?' || pc == vc {
					current[j+1] = previous[j]
				}
			}
		}
		previous = current
	}
	return previous[len(v)]
}
