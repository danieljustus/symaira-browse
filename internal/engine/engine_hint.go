package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// JSRequired answers the --engine-hint question (issue #35): would a static
// fetch (Tier 0) have produced the same content, or was JavaScript actually
// needed? The page is loaded a second time in a probe page with JavaScript
// disabled (Emulation.setScriptExecutionDisabled) and the rendered HTML is
// compared with the script-enabled capture. The comparison is content-based
// (body text), so a probe page that merely differs in markup noise does not
// count as JS-dependent.
func (s *NavigationService) JSRequired(ctx context.Context, url, enabledHTML string) (JSRequiredResult, error) {
	if strings.TrimSpace(url) == "" {
		return JSRequiredResult{}, errors.New("engine hint requires the page URL")
	}
	if s.probeContext.ID == "" {
		return JSRequiredResult{}, errors.New("engine hint requires a probe context (NavigationOptions.ProbeContext)")
	}
	disabler, ok := s.engine.(ScriptDisabler)
	if !ok {
		return JSRequiredResult{}, errors.New("browser engine does not support script-disabled probing")
	}

	probe, err := s.engine.NewPage(ctx, s.probeContext, "about:blank")
	if err != nil {
		return JSRequiredResult{}, fmt.Errorf("create engine-hint probe page: %w", err)
	}
	if err := disabler.DisableScripts(ctx, probe); err != nil {
		return JSRequiredResult{}, fmt.Errorf("disable scripts on probe page: %w", err)
	}
	if _, err := s.engine.Navigate(ctx, probe, url); err != nil {
		return JSRequiredResult{}, fmt.Errorf("navigate probe page: %w", err)
	}
	if err := s.waitForProbeLoad(ctx, probe); err != nil {
		return JSRequiredResult{}, err
	}

	disabledHTML, err := s.probeHTML(ctx, probe)
	if err != nil {
		return JSRequiredResult{}, err
	}

	enabledText := bodyText(enabledHTML)
	disabledText := bodyText(disabledHTML)
	if enabledText == disabledText {
		return JSRequiredResult{
			Required: false,
			Reason:   "static content: the page renders identically with JavaScript disabled",
		}, nil
	}
	return JSRequiredResult{
		Required: true,
		Reason:   "page content differs when JavaScript is disabled (a static fetch would miss content)",
	}, nil
}

// waitForProbeLoad polls the probe page until its document reaches the
// complete ready state, bounded by the service navigation timeout.
func (s *NavigationService) waitForProbeLoad(ctx context.Context, page Page) error {
	waitCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	for {
		result, err := s.engine.Evaluate(waitCtx, page, "document.readyState")
		if err == nil {
			var state string
			if err := json.Unmarshal(result.Value, &state); err == nil && state == "complete" {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return &WaitTimeoutError{Awaited: "probe page load", Timeout: s.timeout}
		case <-time.After(s.pollInterval):
		}
	}
}

// probeHTML reads the rendered HTML of the probe page.
func (s *NavigationService) probeHTML(ctx context.Context, page Page) (string, error) {
	result, err := s.engine.Evaluate(ctx, page, "document.documentElement.outerHTML")
	if err != nil {
		return "", fmt.Errorf("read probe page html: %w", err)
	}
	if result.ExceptionText != "" {
		return "", fmt.Errorf("read probe page html: %s", result.ExceptionText)
	}
	var html string
	if err := json.Unmarshal(result.Value, &html); err != nil {
		return "", fmt.Errorf("decode probe page html: %w", err)
	}
	return html, nil
}

// bodyText extracts the normalized visible text of a document body. A
// document without a body (error pages, about:blank) yields the empty
// string.
func bodyText(pageHTML string) string {
	document, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		return ""
	}
	var body *html.Node
	var findBody func(*html.Node)
	findBody = func(node *html.Node) {
		if body != nil {
			return
		}
		if node.Type == html.ElementNode && strings.ToLower(node.Data) == "body" {
			body = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			findBody(child)
		}
	}
	findBody(document)
	if body == nil {
		return ""
	}
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
			text.WriteString(" ")
			return
		}
		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "script", "style", "noscript", "template":
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(body)
	return strings.Join(strings.Fields(text.String()), " ")
}
