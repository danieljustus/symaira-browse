// Package engine defines the browser-engine boundary used by symbrowse.
package engine

import (
	"context"
	"encoding/json"
)

// Engine owns a browser process and exposes protocol-neutral browser operations.
type Engine interface {
	Launch(context.Context) error
	NewContext(context.Context) (Context, error)
	NewPage(context.Context, Context, string) (Page, error)
	Navigate(context.Context, Page, string) (NavigationResult, error)
	Evaluate(context.Context, Page, string) (EvaluationResult, error)
	AXTree(context.Context, Page) ([]AXNode, error)
	Screenshot(context.Context, Page) ([]byte, error)
	Close() error
}

// Context identifies an isolated browser context.
type Context struct{ ID string }

// Page identifies a target and its attached protocol session.
type Page struct {
	ID        string
	SessionID string
}

// NavigationResult contains the stable result fields returned by Page.navigate.
type NavigationResult struct {
	FrameID   string
	LoaderID  string
	ErrorText string
}

// EvaluationResult contains a protocol-neutral JavaScript result.
type EvaluationResult struct {
	Value         json.RawMessage
	Type          string
	Description   string
	ExceptionText string
}

// AXNode contains the raw, versioned accessibility-node payload while keeping
// generated protocol types out of the engine boundary.
type AXNode struct{ Raw json.RawMessage }

// BlockedRequest describes one distinct URL denied by the active network
// policy and how often it was denied. Reason names the denying policy
// (domain allowlist or SSRF guard) for the warnings[] payload.
type BlockedRequest struct {
	URL          string `json:"url"`
	ResourceType string `json:"resource_type"`
	Count        int    `json:"count"`
	Reason       string `json:"reason,omitempty"`
}

// NetworkPolicyReporter is an optional engine extension. It lets callers
// surface domain-allowlist enforcement as warnings: which requests were
// denied and which known limitations apply to the running engine. Engines
// without a network policy return empty slices.
type NetworkPolicyReporter interface {
	// BlockedRequests returns the denied requests since the engine started.
	BlockedRequests() []BlockedRequest
	// Limitations returns startup warnings for configurations in which the
	// allowlist cannot be fully enforced (for example a reused profile).
	Limitations() []string
}
