package agentdom

import (
	"encoding/json"

	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
)

// Element is a semantic DOM element, optionally tagged with an agent ID.
type Element struct {
	AgentID  string            `json:"id,omitempty"` // "@e1" for interactive elements
	Category semantic.Category `json:"category"`
	Tag      string            `json:"tag,omitempty"`
	Text     string            `json:"text,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Children []Element         `json:"children,omitempty"`
}

// DataIsland proxies semantic.DataIsland.
type DataIsland struct {
	Source string          `json:"source"`
	JSON   json.RawMessage `json:"json"`
}

// Document is the agent-optimised view of a fetched page.
type Document struct {
	URL         string       `json:"url"`
	FinalURL    string       `json:"final_url,omitempty"`
	Title       string       `json:"title,omitempty"`
	Lang        string       `json:"lang,omitempty"`
	Content     []Element    `json:"content"`     // scored main content, document order
	Interactive []Element    `json:"interactive"` // flat list of @eN-tagged elements
	Islands     []DataIsland `json:"islands,omitempty"`
	// Escalate mirrors Meta.Escalate so the JSON contract carries the
	// tier-0 -> tier-1 hint too (docs/tiers.md). It is omitted for pages a
	// plain HTTP fetch retrieved in full.
	Escalate *EscalationHint `json:"escalate,omitempty"`
}

// EscalationHint is a non-blocking hint emitted when the fetched page is
// likely a client-rendered shell (SPA skeleton, thin content, or a JS
// challenge page) whose real content a plain HTTP fetch cannot retrieve.
// Consumers MAY use the suggested tool/command to re-fetch the page with a
// JS-capable browser; the fetch itself never fails because of this hint.
//
// Command is the CLI invocation, MCPTool the tool name an MCP client calls
// instead. Both name the same escalation; a client uses whichever matches the
// surface it is driving.
type EscalationHint struct {
	Tool    string `json:"tool"`
	MCPTool string `json:"mcp_tool,omitempty"`
	Reason  string `json:"reason"`
	Command string `json:"command"`
}

// Meta holds response metadata returned alongside the rendered output.
type Meta struct {
	FinalURL             string          `json:"final_url"`
	StatusCode           int             `json:"status_code"`
	Title                string          `json:"title,omitempty"`
	Lang                 string          `json:"lang,omitempty"`
	CharCount            int             `json:"char_count"`
	EstTokens            int             `json:"est_tokens"` // chars / 4
	Truncated            bool            `json:"truncated"`
	Protocol             string          `json:"protocol,omitempty"`
	LikelyClientRendered bool            `json:"likely_client_rendered"`
	Escalate             *EscalationHint `json:"escalate,omitempty"`
}
