package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeNavigationEngine struct {
	state       NavigationState
	navigateURL string
	evaluations []string
	selectorOK  bool
}

func (f *fakeNavigationEngine) Launch(context.Context) error { return nil }
func (f *fakeNavigationEngine) NewContext(context.Context) (Context, error) {
	return Context{ID: "ctx"}, nil
}
func (f *fakeNavigationEngine) NewPage(context.Context, Context, string) (Page, error) {
	return Page{ID: "page"}, nil
}
func (f *fakeNavigationEngine) Navigate(_ context.Context, _ Page, url string) (NavigationResult, error) {
	f.navigateURL = url
	f.state.URL = url
	f.state.ReadyState = "complete"
	f.state.NetworkIdle = true
	return NavigationResult{}, nil
}
func (f *fakeNavigationEngine) Evaluate(_ context.Context, _ Page, expression string) (EvaluationResult, error) {
	f.evaluations = append(f.evaluations, expression)
	if strings.Contains(expression, "history.back") {
		f.state.URL = "http://fixture.test/previous"
	}
	if strings.Contains(expression, "history.forward") {
		f.state.URL = "http://fixture.test/next"
	}
	if strings.Contains(expression, "location.reload") {
		f.state.ReadyState = "complete"
	}
	if strings.Contains(expression, "matched:") {
		payload, _ := json.Marshal(map[string]any{
			"matched":      f.selectorOK,
			"url":          f.state.URL,
			"http_status":  f.state.HTTPStatus,
			"ready_state":  f.state.ReadyState,
			"network_idle": f.state.NetworkIdle,
		})
		return EvaluationResult{Value: payload}, nil
	}
	payload, _ := json.Marshal(f.state)
	return EvaluationResult{Value: payload}, nil
}
func (f *fakeNavigationEngine) AXTree(context.Context, Page) ([]AXNode, error)   { return nil, nil }
func (f *fakeNavigationEngine) Screenshot(context.Context, Page) ([]byte, error) { return nil, nil }
func (f *fakeNavigationEngine) Close() error                                     { return nil }
func (f *fakeNavigationEngine) NavigationState(context.Context, Page) (NavigationState, error) {
	return f.state, nil
}

func TestNavigationServiceOpenReturnsFinalURLAndStatus(t *testing.T) {
	fake := &fakeNavigationEngine{state: NavigationState{HTTPStatus: 200}}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{Timeout: time.Second, PollInterval: time.Millisecond})
	got, err := service.Open(context.Background(), "http://fixture.test/redirected")
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://fixture.test/redirected" || got.HTTPStatus != 200 {
		t.Fatalf("outcome = %#v", got)
	}
	if fake.navigateURL != "http://fixture.test/redirected" {
		t.Fatalf("navigate URL = %q", fake.navigateURL)
	}
}

func TestNavigationServiceHistoryActionsUseBrowserHistory(t *testing.T) {
	fake := &fakeNavigationEngine{state: NavigationState{URL: "http://fixture.test/start", ReadyState: "complete", NetworkIdle: true}}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{Timeout: time.Second, PollInterval: time.Millisecond})
	if _, err := service.Back(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Forward(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.evaluations, "\n")
	for _, want := range []string{"history.back()", "history.forward()", "location.reload()"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evaluations missing %q: %s", want, joined)
		}
	}
}

func TestNavigationServiceWaitSelectorStates(t *testing.T) {
	fake := &fakeNavigationEngine{state: NavigationState{URL: "http://fixture.test/spa", ReadyState: "complete", NetworkIdle: true}, selectorOK: true}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{Timeout: time.Second, PollInterval: time.Millisecond})
	result, err := service.Wait(context.Background(), WaitCondition{Kind: WaitSelector, Value: "#hydrated-button", SelectorState: SelectorVisible})
	if err != nil {
		t.Fatal(err)
	}
	if result.Awaited != `selector "#hydrated-button" to be visible` {
		t.Fatalf("awaited = %q", result.Awaited)
	}
}

func TestNavigationServiceWaitTimeoutNamesAwaitedAndObserved(t *testing.T) {
	fake := &fakeNavigationEngine{state: NavigationState{URL: "http://fixture.test/spa", ReadyState: "loading", HTTPStatus: 200}, selectorOK: false}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{Timeout: 15 * time.Millisecond, PollInterval: time.Millisecond})
	_, err := service.Wait(context.Background(), WaitCondition{Kind: WaitSelector, Value: "#never", SelectorState: SelectorAttached})
	if err == nil {
		t.Fatal("wait unexpectedly succeeded")
	}
	var timeoutErr *WaitTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error type = %T: %v", err, err)
	}
	message := err.Error()
	for _, want := range []string{`selector "#never" to be attached`, "observed", "fixture.test/spa", "loading"} {
		if !strings.Contains(message, want) {
			t.Fatalf("timeout message %q missing %q", message, want)
		}
	}
}

func TestNavigationServiceMillisecondsWait(t *testing.T) {
	fake := &fakeNavigationEngine{state: NavigationState{URL: "http://fixture.test/static", ReadyState: "complete", NetworkIdle: true}}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{Timeout: time.Second})
	start := time.Now()
	result, err := service.Wait(context.Background(), WaitCondition{Kind: WaitMilliseconds, Duration: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 5*time.Millisecond || result.Observed.URL == "" {
		t.Fatalf("result = %#v, elapsed = %s", result, time.Since(start))
	}
}

func TestNavigationServiceWaitRejectsOversizedValue(t *testing.T) {
	fake := &fakeNavigationEngine{}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{Timeout: time.Second})
	value := strings.Repeat("x", maxWaitValueBytes+1)
	if _, err := service.Wait(context.Background(), WaitCondition{Kind: WaitURL, Value: value}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized wait value error = %v", err)
	}
	if matchGlob(value, "anything") {
		t.Fatal("oversized glob pattern unexpectedly matched")
	}
}

func TestConditionExpressionQuotesSelectorValue(t *testing.T) {
	value := `#quote"\\`
	encoded := strconv.Quote(value)
	if expression := conditionExpression(WaitCondition{Kind: WaitSelector, Value: value, SelectorState: SelectorVisible}); !strings.Contains(expression, encoded) {
		t.Fatalf("condition expression did not contain JSON-encoded selector: %s", expression)
	}
}

func TestMatchGlobSupportsURLWildcards(t *testing.T) {
	for _, test := range []struct {
		pattern string
		value   string
		want    bool
	}{
		{"http://fixture.test/**", "http://fixture.test/spa", true},
		{"*/form", "http://fixture.test/form", true},
		{"http://fixture.test/?", "http://fixture.test/a", true},
		{"http://fixture.test/form", "http://fixture.test/static", false},
	} {
		if got := matchGlob(test.pattern, test.value); got != test.want {
			t.Errorf("matchGlob(%q, %q) = %t, want %t", test.pattern, test.value, got, test.want)
		}
	}
}
