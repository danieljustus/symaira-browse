package formflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// ErrElementNotFound is returned by Driver.Fill and Driver.Click when no
// selector variant of the target element could be located on the page.
var ErrElementNotFound = errors.New("formflow: element not found")

// Driver is the narrow browser surface formflow needs. It is an interface so
// the runner logic is fully testable without a browser; NewEngineDriver
// adapts the real symbrowse engine onto it.
type Driver interface {
	// Navigate loads url. A timeout is reported as an error wrapping the
	// engine wait-timeout so the runner can classify it.
	Navigate(ctx context.Context, url string) error
	// CurrentURL returns the page's current URL.
	CurrentURL(ctx context.Context) (string, error)
	// PageText returns the visible body text of the current page.
	PageText(ctx context.Context) (string, error)
	// PageHTML returns the serialized document of the current page.
	PageHTML(ctx context.Context) (string, error)
	// Screenshot captures the current viewport as PNG bytes.
	Screenshot(ctx context.Context) ([]byte, error)
	// Fill locates the element described by sel (semantic first, CSS as
	// fallback) and replaces its content with value.
	Fill(ctx context.Context, sel Selector, value string) error
	// Click locates the element described by sel and clicks it.
	Click(ctx context.Context, sel Selector) error
	// WaitForURL waits until the current URL matches the glob pattern.
	WaitForURL(ctx context.Context, glob string) error
	// WaitSettled waits until the page reaches network idle.
	WaitSettled(ctx context.Context) error
}

// EngineDriver adapts an engine.Engine page to the Driver interface.
type EngineDriver struct {
	browser engine.Engine
	page    engine.Page
	nav     *engine.NavigationService
}

// NewEngineDriver wraps one engine page with default navigation options.
func NewEngineDriver(browser engine.Engine, page engine.Page) *EngineDriver {
	return &EngineDriver{
		browser: browser,
		page:    page,
		nav:     engine.NewNavigationService(browser, page, engine.NavigationOptions{}),
	}
}

// NewEngineDriverWithOptions wraps one engine page with explicit navigation
// options (timeouts, poll intervals).
func NewEngineDriverWithOptions(browser engine.Engine, page engine.Page, options engine.NavigationOptions) *EngineDriver {
	return &EngineDriver{
		browser: browser,
		page:    page,
		nav:     engine.NewNavigationService(browser, page, options),
	}
}

// Navigate implements Driver.
func (d *EngineDriver) Navigate(ctx context.Context, url string) error {
	_, err := d.nav.Goto(ctx, url)
	return err
}

// CurrentURL implements Driver.
func (d *EngineDriver) CurrentURL(ctx context.Context) (string, error) {
	return d.evalString(ctx, "location.href")
}

// PageText implements Driver.
func (d *EngineDriver) PageText(ctx context.Context) (string, error) {
	return d.evalString(ctx, "document.body ? document.body.innerText : ''")
}

// PageHTML implements Driver.
func (d *EngineDriver) PageHTML(ctx context.Context) (string, error) {
	return d.evalString(ctx, "document.documentElement ? document.documentElement.outerHTML : ''")
}

// Screenshot implements Driver.
func (d *EngineDriver) Screenshot(ctx context.Context) ([]byte, error) {
	return d.browser.Screenshot(ctx, d.page)
}

// Fill implements Driver: semantic find first, CSS fallback.
func (d *EngineDriver) Fill(ctx context.Context, sel Selector, value string) error {
	target, err := d.resolve(ctx, sel, engine.FindFill, value)
	if err != nil {
		return err
	}
	_, err = d.nav.Interact(ctx, engine.InteractionRequest{Action: engine.ActionFill, Selector: target, Value: value})
	return err
}

// Click implements Driver: semantic find first, CSS fallback.
func (d *EngineDriver) Click(ctx context.Context, sel Selector) error {
	target, err := d.resolve(ctx, sel, engine.FindClick, "")
	if err != nil {
		return err
	}
	_, err = d.nav.Interact(ctx, engine.InteractionRequest{Action: engine.ActionClick, Selector: target})
	return err
}

// WaitForURL implements Driver.
func (d *EngineDriver) WaitForURL(ctx context.Context, glob string) error {
	_, err := d.nav.Wait(ctx, engine.WaitCondition{Kind: engine.WaitURL, Value: glob})
	return err
}

// WaitSettled implements Driver.
func (d *EngineDriver) WaitSettled(ctx context.Context) error {
	_, err := d.nav.Wait(ctx, engine.WaitCondition{Kind: engine.WaitLoad, LoadState: engine.LoadNetworkIdle})
	return err
}

// resolve locates the element for a selector. Semantic kinds are tried in a
// stability-ranked order (label, placeholder, testid, role, text); an
// explicit CSS selector is the final fallback.
func (d *EngineDriver) resolve(ctx context.Context, sel Selector, action engine.FinderAction, value string) (string, error) {
	type attempt struct {
		kind  engine.FinderKind
		query string
	}
	attempts := []attempt{
		{engine.FindLabel, sel.Label},
		{engine.FindPlaceholder, sel.Placeholder},
		{engine.FindTestID, sel.TestID},
		{engine.FindRole, sel.Role},
		{engine.FindText, sel.Text},
	}
	for _, candidate := range attempts {
		if candidate.query == "" {
			continue
		}
		result, err := d.nav.Find(ctx, engine.FindRequest{
			Kind:   candidate.kind,
			Query:  candidate.query,
			Action: engine.FindRef,
			Value:  value,
			Exact:  sel.Exact,
		})
		if err != nil || result.Ref == "" {
			continue
		}
		return result.Ref, nil
	}
	if sel.CSS != "" {
		return sel.CSS, nil
	}
	_ = action
	return "", fmt.Errorf("%w: %s", ErrElementNotFound, describe(sel))
}

// evalString evaluates an expression and decodes the result as a string.
func (d *EngineDriver) evalString(ctx context.Context, expression string) (string, error) {
	result, err := d.nav.Evaluate(ctx, expression)
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(result.Value, &value); err != nil {
		return "", fmt.Errorf("evaluate %q: %w", expression, err)
	}
	return value, nil
}

// describe renders a selector for error messages without leaking values.
func describe(sel Selector) string {
	switch {
	case sel.Label != "":
		return "label " + sel.Label
	case sel.Placeholder != "":
		return "placeholder " + sel.Placeholder
	case sel.TestID != "":
		return "testid " + sel.TestID
	case sel.Role != "":
		return "role " + sel.Role
	case sel.Text != "":
		return "text " + sel.Text
	case sel.CSS != "":
		return "css " + sel.CSS
	default:
		return "empty selector"
	}
}
