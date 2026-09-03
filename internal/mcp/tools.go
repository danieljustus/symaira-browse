// Package mcp implements the symbrowse MCP tool surface: a JSON-RPC 2.0
// stdio server (corekit/mcpserver) whose tools proxy to the local symbrowse
// daemon over its Unix socket. Every tool accepts an optional session
// argument; logging goes exclusively through slog to stderr so that no byte
// other than JSON-RPC frames reaches stdout.
package mcp

import (
	"fmt"
	"strings"
	"time"
)

// Profile is the tool-profile name from docs/mcp.md. The profile
// assignment is a data table: every tool belongs to exactly one profile and
// the all profile is the union of all profiles.
type Profile string

const (
	ProfileCore    Profile = "core"
	ProfileNav     Profile = "nav"
	ProfileState   Profile = "state"
	ProfileNetwork Profile = "network"
	ProfileDebug   Profile = "debug"
	ProfileFlows   Profile = "flows"
)

// toolSchema is a JSON-Schema fragment builder. Every tool accepts an
// optional "session" string argument; the schema helpers append it.
type schema map[string]any

func (s schema) withSession() schema {
	properties, _ := s["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
		s["properties"] = properties
	}
	properties["session"] = map[string]any{
		"type":        "string",
		"description": "session name (default: the server's default session)",
	}
	return s
}

func objectSchema(properties map[string]any, required ...string) schema {
	s := schema{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s.withSession()
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func boolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// guidance renders the compact model-facing description contract (issue #4):
// when to use the tool, when not to, what it returns, and the suggested next
// step. Every registered tool description carries this block so agents can
// select the right tool without separate documentation.
func guidance(useWhen, doNotUseWhen, returns, next string) string {
	var b strings.Builder
	b.WriteString("Use when: ")
	b.WriteString(useWhen)
	b.WriteString(". ")
	if doNotUseWhen != "" {
		b.WriteString("Do not use when: ")
		b.WriteString(doNotUseWhen)
		b.WriteString(". ")
	}
	b.WriteString("Returns: ")
	b.WriteString(returns)
	b.WriteString(".")
	if next != "" {
		b.WriteString(" Next: ")
		b.WriteString(next)
		b.WriteString(".")
	}
	return b.String()
}

// fetchEscalationNote distinguishes ordinary Fetch reads from browser
// escalation. It is embedded in the read/open/snapshot guidance (issue #4;
// see docs/tiers.md): agents should reach for this browser tool only when
// JavaScript, browser state or interaction is needed.
const fetchEscalationNote = "For plain static content a normal HTTP fetch is cheaper; use this tool only when JavaScript, browser state or interaction is required"

// mcpBudgetedCommands are the output-heavy daemon commands that get a
// stricter default token budget in MCP mode: snapshot, read, get html,
// console, network requests and the plain-HTTP fetches. fetch.url and
// fetch.batch belong here for the same reason the browser reads do — a
// single call can return a whole page, and fetch.batch multiplies that by
// up to 20 URLs.
var mcpBudgetedCommands = map[string]bool{
	"snapshot":         true,
	"read":             true,
	"get.html":         true,
	"console.list":     true,
	"errors.list":      true,
	"network.requests": true,
	"a11y":             true,
	"network.har":      true,
	"fetch.url":        true,
	"fetch.batch":      true,
}

// mcpDefaultMaxTokens is the stricter MCP-mode budget (issue #23); TTY mode
// applies no default and only honors an explicit --max-tokens flag.
const mcpDefaultMaxTokens = 4000

// ProxyTool describes one daemon-proxied MCP tool.
type ProxyTool struct {
	// Name and Description are exposed through tools/list.
	Name        string
	Description string
	// Profile is the tool-profile assignment (see docs/mcp.md).
	Profile Profile
	// Schema is the JSON-Schema input description (without "session",
	// which is appended automatically).
	Schema map[string]any
	// Cmd is the daemon frame command. When Command is set it takes
	// precedence and may resolve the command from the tool input.
	Cmd string
	// Command optionally resolves the daemon frame command from the
	// decoded tool input (used by tools that span several frames).
	Command func(input map[string]any) (string, error)
	// Args builds the frame arguments from the decoded tool input. It may
	// return nil, in which case no args are sent (only valid for frames
	// whose daemon dispatch does not require args).
	Args func(input map[string]any) (any, error)
	// Result transforms the daemon response data. Nil returns the data
	// unchanged.
	Result func(data any) (any, error)
	// Aliases are compatibility names for the canonical tool ID (issue
	// #2). They register as separate deprecated tool entries whose calls
	// resolve to the canonical tool; tools/list exposes them with a
	// replacement note. An alias must not collide with another canonical
	// name or alias.
	Aliases []string
}

// tools is the complete tool table. The profile assignment is the single
// source of truth for the MCP tool profiles (issue #31): tools not present
// here are not exposed, and the all profile is derived as the union.
var tools = []ProxyTool{
	{
		Name:        "open",
		Description: "Open a URL in the browser and wait for the page to load. " + guidance("you need a real browser: the page needs JavaScript, redirects, cookies or browser state", fetchEscalationNote, "the final URL and HTTP status", "snapshot or read to inspect the page"),
		Profile:     ProfileCore,
		Schema:      objectSchema(map[string]any{"url": stringProp("the URL to open (http or https)")}, "url"),
		Cmd:         "open",
		// "goto" is a compatibility alias: the daemon accepts both
		// open and goto, and agents coming from other tools often say
		// goto. The canonical tool id is "open".
		Aliases: []string{"goto"},
		Args: func(input map[string]any) (any, error) {
			url, err := requiredString(input, "url")
			if err != nil {
				return nil, err
			}
			return map[string]string{"url": url}, nil
		},
	},
	{
		Name:        "snapshot",
		Description: "Render the accessibility tree of the current page as text. " + guidance("you need the current interactive state of the page with stable @refs for elements", "you only need the page text (use read or a plain fetch); "+fetchEscalationNote, "the accessibility tree (with --diff: only the changes since the last snapshot)", "click, fill or get on a @ref"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"selector":    stringProp("CSS selector or @ref restricting the snapshot subtree"),
			"depth":       integerProp("maximum tree depth; 0 means unlimited"),
			"interactive": boolProp("include only interactive nodes"),
			"compact":     boolProp("omit non-interactive structural nodes"),
			"urls":        boolProp("include link URLs"),
		}),
		Cmd: "snapshot",
		Args: func(input map[string]any) (any, error) {
			options := map[string]any{
				"interactive": boolValue(input, "interactive"),
				"compact":     boolValue(input, "compact"),
				"depth":       intValue(input, "depth"),
				"urls":        boolValue(input, "urls"),
			}
			if selector, ok := input["selector"].(string); ok {
				options["selector"] = selector
			}
			return options, nil
		},
	},
	{
		Name:        "click",
		Description: "Click the element matching a CSS selector or @ref. " + guidance("you need to activate an element (button, link, menu item)", "you only need to read state (use get or snapshot)", "the click result", "snapshot to observe the new page state"),
		Profile:     ProfileCore,
		Schema:      objectSchema(map[string]any{"selector": stringProp("CSS selector or @ref of the element")}, "selector"),
		Cmd:         "click",
		Args: func(input map[string]any) (any, error) {
			selector, err := requiredString(input, "selector")
			if err != nil {
				return nil, err
			}
			return map[string]string{"selector": selector}, nil
		},
	},
	{
		Name:        "fill",
		Description: "Fill a text input or textarea with a value, replacing its current content. " + guidance("you need to set a form field to a known value (replaces existing content)", "the element already has the value or you need to append (use type)", "the fill result", "press Enter or click the submit button"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"selector": stringProp("CSS selector or @ref of the input"),
			"value":    stringProp("the value to fill"),
		}, "selector", "value"),
		Cmd: "fill",
		Args: func(input map[string]any) (any, error) {
			selector, err := requiredString(input, "selector")
			if err != nil {
				return nil, err
			}
			value, err := requiredString(input, "value")
			if err != nil {
				return nil, err
			}
			return map[string]string{"selector": selector, "value": value}, nil
		},
	},
	{
		Name:        "type",
		Description: "Type text into the focused or selected element, appending to its current content. " + guidance("you need to type text that appends to the current field content", "you need to replace the content (use fill)", "the type result", "press Enter or snapshot the field state"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"selector": stringProp("CSS selector or @ref of the input (optional)"),
			"value":    stringProp("the text to type"),
		}, "value"),
		Cmd: "type",
		Args: func(input map[string]any) (any, error) {
			value, err := requiredString(input, "value")
			if err != nil {
				return nil, err
			}
			args := map[string]string{"value": value}
			if selector, ok := input["selector"].(string); ok {
				args["selector"] = selector
			}
			return args, nil
		},
	},
	{
		Name:        "press",
		Description: "Press a keyboard key (e.g. Enter, Escape, Tab, ArrowDown). " + guidance("you need to send a key such as Enter or Escape", "you need to type a longer text (use type or fill)", "the press result", "snapshot to observe the new state"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"selector": stringProp("CSS selector or @ref of the element to receive the key (optional)"),
			"key":      stringProp("the key to press"),
		}, "key"),
		Cmd: "press",
		Args: func(input map[string]any) (any, error) {
			key, err := requiredString(input, "key")
			if err != nil {
				return nil, err
			}
			args := map[string]string{"key": key}
			if selector, ok := input["selector"].(string); ok {
				args["selector"] = selector
			}
			return args, nil
		},
	},
	{
		Name:        "wait",
		Description: "Wait for a browser condition (kind: selector, text, url, ms, load). " + guidance("the page is still loading or an element is not yet in the expected state", "you need a fixed pause (prefer an explicit condition over a sleep)", "the wait result", "snapshot or read to inspect the settled page"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"kind":  stringProp("condition kind: selector, text, url, ms, load"),
			"value": stringProp("selector, text, or URL substring depending on kind"),
			"state": stringProp("selector state: visible, hidden, attached, detached"),
			"ms":    integerProp("duration in milliseconds for kind=ms"),
			"load":  stringProp("load state: load, domcontentloaded, networkidle"),
		}, "kind"),
		Cmd: "wait",
		Args: func(input map[string]any) (any, error) {
			kind, err := requiredString(input, "kind")
			if err != nil {
				return nil, err
			}
			condition := map[string]any{"kind": kind}
			if value, ok := input["value"].(string); ok {
				condition["value"] = value
			}
			if state, ok := input["state"].(string); ok {
				condition["state"] = state
			}
			if load, ok := input["load"].(string); ok {
				condition["load_state"] = load
			}
			if ms, ok := input["ms"].(float64); ok && ms > 0 {
				condition["duration"] = time.Duration(ms) * time.Millisecond
			}
			return condition, nil
		},
	},
	{
		Name:        "read",
		Description: "Render the current page (or the page at url) as markdown in the symfetch output schema. " + guidance("you need the page content as clean markdown with frontmatter (title, url, fetched_at)", fetchEscalationNote, "the rendered markdown document", "open another URL or find links on the page"),
		Profile:     ProfileCore,
		Schema:      objectSchema(map[string]any{"url": stringProp("URL to open and read (optional; defaults to the current page)")}),
		Cmd:         "read",
		Args: func(input map[string]any) (any, error) {
			args := map[string]string{"url": ""}
			if url, ok := input["url"].(string); ok {
				args["url"] = url
			}
			return args, nil
		},
	},
	{
		Name:        "get",
		Description: "Inspect the page or an element (kind: text, html, value, attr, title, url, count, box, styles, visible, enabled, checked). " + guidance("you need one specific value of the page or an element", "you need the whole interactive tree (use snapshot)", "the inspected value", "snapshot or find for further navigation"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"kind":      stringProp("inspection kind"),
			"selector":  stringProp("CSS selector or @ref (not needed for title/url)"),
			"attribute": stringProp("attribute name for kind=attr"),
		}, "kind"),
		Cmd: "get.text",
		Command: func(input map[string]any) (string, error) {
			kind, err := requiredString(input, "kind")
			if err != nil {
				return "", err
			}
			return inspectionCommand(kind)
		},
		Args: func(input map[string]any) (any, error) {
			kind, err := requiredString(input, "kind")
			if err != nil {
				return nil, err
			}
			args := map[string]any{"kind": kind}
			if selector, ok := input["selector"].(string); ok {
				args["selector"] = selector
			}
			if attribute, ok := input["attribute"].(string); ok {
				args["attribute"] = attribute
			}
			return args, nil
		},
	},
	{
		Name:        "find",
		Description: "Find elements by semantic query (kind: text, role, label, placeholder, alt, css, xpath, ref; action: goto, focus, click). " + guidance("you need to locate elements semantically and address them by stable @refs", "you already have a working CSS selector or @ref", "the matching elements with their stable @refs", "click, fill or get on the returned @ref"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"kind":   stringProp("query kind: text, role, label, placeholder, alt, css, xpath, ref"),
			"query":  stringProp("the query value"),
			"action": stringProp("action to perform on the match: goto, focus, click"),
			"value":  stringProp("optional value for the action"),
			"name":   stringProp("optional accessible name filter"),
			"exact":  boolProp("require an exact text match"),
			"index":  integerProp("match index when several elements match (0-based)"),
		}, "kind", "query"),
		Cmd: "find",
		Args: func(input map[string]any) (any, error) {
			kind, err := requiredString(input, "kind")
			if err != nil {
				return nil, err
			}
			query, err := requiredString(input, "query")
			if err != nil {
				return nil, err
			}
			args := map[string]any{"kind": kind, "query": query}
			if action, ok := input["action"].(string); ok {
				args["action"] = action
			}
			if value, ok := input["value"].(string); ok {
				args["value"] = value
			}
			if name, ok := input["name"].(string); ok {
				args["name"] = name
			}
			if exact, ok := input["exact"].(bool); ok {
				args["exact"] = exact
			}
			if index, ok := input["index"].(float64); ok {
				args["index"] = int(index)
			}
			return args, nil
		},
	},
	{
		Name:        "back",
		Description: "Navigate back in the page history. " + guidance("the previous page of this tab is needed", "you need a fresh load of the same URL (use open or reload)", "the navigation result", "snapshot to inspect the previous page"),
		Profile:     ProfileNav,
		Schema:      objectSchema(nil),
		Cmd:         "back",
	},
	{
		Name:        "forward",
		Description: "Navigate forward in the page history. " + guidance("a page you navigated back from is needed again", "the target page is not in this tab history (use open)", "the navigation result", "snapshot to inspect the page"),
		Profile:     ProfileNav,
		Schema:      objectSchema(nil),
		Cmd:         "forward",
	},
	{
		Name:        "reload",
		Description: "Reload the current page. " + guidance("the page changed server-side and a fresh load is needed", "you only need to re-read the current DOM (use snapshot or read)", "the navigation result", "snapshot or read the reloaded page"),
		Profile:     ProfileNav,
		Schema:      objectSchema(nil),
		Cmd:         "reload",
	},
	{
		Name:        "fetch_url",
		Description: "Fetch a web page with the plain-HTTP fetch pipeline (no browser) and return LLM-optimized content in the SymFetch contract. " + guidance("you need fast fetch of static content, batch fetch, or Wayback discovery without a browser session", "you need JavaScript, browser state or interaction (use open/snapshot/read)", "markdown (default), json, or text in the SymFetch output schema", "read or open when the page needs a real browser"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"url":               stringProp("the URL to fetch (http or https)"),
			"format":            map[string]any{"type": "string", "description": "Output format: markdown (default), json, text", "enum": []string{"markdown", "json", "text"}},
			"max_chars":         integerProp("Maximum characters in output (default 20000)"),
			"char_limit":        integerProp("Per-page character limit for truncate-and-store (default 15000)"),
			"css_selector":      stringProp("CSS selector to extract specific elements (e.g. 'table.pricing', '.article-body')"),
			"frontmatter":       boolProp("Prepend YAML frontmatter with metadata (title, url, fetched_at, lang, tokens)"),
			"include_links":     boolProp("Append a Links section with all hrefs (default false)"),
			"query":             stringProp("BM25 query for relevance filtering. Returns only sections matching the query, preserving headings and structure."),
			"raw":               boolProp("Return raw decoded response body without semantic processing"),
			"schema_path":       stringProp("JSON-LD query path. Typed selectors (e.g. '@Recipe:name') filter by @type then traverse a dot-path."),
			"store_full_text":   boolProp("Enable truncate-and-store for long pages (default false)"),
			"wayback_timestamp": stringProp("Specific Wayback timestamp to fetch (empty = latest)"),
			"wayback_fallback":  boolProp("Fall back to the Wayback Machine on 404/thin content"),
		}, "url"),
		Cmd: "fetch.url",
		Args: func(input map[string]any) (any, error) {
			return buildFetchArgs(input)
		},
	},
	{
		Name:        "fetch_batch",
		Description: "Fetch multiple URLs concurrently with the plain-HTTP fetch pipeline and return results in input order. " + guidance("you need several pages at once and each is independent", "you need one page (use fetch_url) or browser interaction", "a per-URL array in input order; one failure does not abort the batch", "read or open when a result needs a real browser"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"urls":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "URLs to fetch (max 20)"},
			"format":          map[string]any{"type": "string", "description": "Output format for each result: markdown, json, text"},
			"max_chars":       integerProp("Per-page character budget (default 20000)"),
			"char_limit":      integerProp("Per-page character limit for truncate-and-store (default 15000)"),
			"concurrency":     integerProp("Maximum parallel fetches (default 4, max 8)"),
			"store_full_text": boolProp("Enable truncate-and-store for each page (default false)"),
			"frontmatter":     boolProp("Prepend YAML frontmatter to each result"),
			"include_links":   boolProp("Append a Links section to each result"),
		}, "urls"),
		Cmd: "fetch.batch",
		Args: func(input map[string]any) (any, error) {
			urls, ok := input["urls"].([]any)
			if !ok || len(urls) == 0 {
				return nil, fmt.Errorf("missing required argument %q", "urls")
			}
			stringURLs := make([]string, 0, len(urls))
			for _, u := range urls {
				s, ok := u.(string)
				if !ok {
					return nil, fmt.Errorf("urls entries must be strings")
				}
				stringURLs = append(stringURLs, s)
			}
			args, err := buildFetchArgs(input, map[string]any{"urls": stringURLs})
			if err != nil {
				return nil, err
			}
			return args, nil
		},
	},
	{
		Name:        "wayback_snapshots",
		Description: "List available Wayback Machine snapshots for a URL. Returns timestamps, HTTP status codes, and MIME types for each captured version. " + guidance("you need to know whether and when a page was archived, or find a historical version", "you need the current live page (use fetch_url)", "an array of snapshots with timestamp, url, status, mime_type, digest", "fetch the archived URL directly if a snapshot fits"),
		Profile:     ProfileCore,
		Schema: objectSchema(map[string]any{
			"url":        stringProp("The URL to look up in the Wayback Machine"),
			"from":       stringProp("Start date filter (format: YYYYMMDD or YYYYMMDDHHmmss)"),
			"to":         stringProp("End date filter (format: YYYYMMDD or YYYYMMDDHHmmss)"),
			"limit":      integerProp("Maximum number of snapshots to return (default 100)"),
			"match_type": map[string]any{"type": "string", "description": "URL matching mode: exact (default), prefix, or host", "enum": []string{"exact", "prefix", "host"}},
		}, "url"),
		Cmd: "wayback.snapshots",
		Args: func(input map[string]any) (any, error) {
			url, err := requiredString(input, "url")
			if err != nil {
				return nil, err
			}
			args := map[string]any{"url": url}
			if from, ok := input["from"].(string); ok {
				args["from"] = from
			}
			if to, ok := input["to"].(string); ok {
				args["to"] = to
			}
			if limit, ok := input["limit"].(float64); ok {
				args["limit"] = int(limit)
			}
			if mt, ok := input["match_type"].(string); ok {
				args["match_type"] = mt
			}
			return args, nil
		},
	},
}

// buildFetchArgs maps the shared fetch_url/fetch_batch tool inputs onto the
// daemon frame argument shape (both contracts share the pipeline fields).
func buildFetchArgs(input map[string]any, overrides ...map[string]any) (any, error) {
	args := map[string]any{}
	for _, ov := range overrides {
		for k, v := range ov {
			args[k] = v
		}
	}
	for _, key := range []string{"url", "format", "css_selector", "query", "schema_path", "wayback_timestamp"} {
		if v, ok := input[key].(string); ok {
			args[key] = v
		}
	}
	for _, key := range []string{"max_chars", "char_limit", "concurrency"} {
		if v, ok := input[key].(float64); ok {
			args[key] = int(v)
		}
	}
	for _, key := range []string{"frontmatter", "include_links", "raw", "store_full_text", "wayback_fallback"} {
		if v, ok := input[key].(bool); ok {
			args[key] = v
		}
	}
	return args, nil
}

// inspectionCommand maps an MCP get kind to its daemon frame command.
func inspectionCommand(kind string) (string, error) {
	switch kind {
	case "text", "html", "value", "attr", "title", "url", "count", "box", "styles":
		return "get." + kind, nil
	case "visible", "enabled", "checked":
		return "is." + kind, nil
	}
	return "", fmt.Errorf("invalid get kind %q: expected text, html, value, attr, title, url, count, box, styles, visible, enabled, or checked", kind)
}

func requiredString(input map[string]any, key string) (string, error) {
	value, ok := input[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	return value, nil
}

func boolValue(input map[string]any, key string) bool {
	value, _ := input[key].(bool)
	return value
}

func intValue(input map[string]any, key string) int {
	value, _ := input[key].(float64)
	return int(value)
}
