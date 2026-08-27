// Package engine defines the browser-engine boundary used by symbrowse.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
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

// OptionalInterfaceNames lists the canonical names of every optional engine
// extension. The set is the contract for capability reporting (issue #295):
// an engine that implements an extension reports its canonical name here, and
// callers can derive unsupported operations from the complement. Keep the
// list sorted; names are stable machine-readable identifiers.
var OptionalInterfaceNames = []string{
	"A11yAuditor",
	"AXSelectorResolver",
	"ClickDiagnosticEngine",
	"CookieEngine",
	"DialogController",
	"FileTransfer",
	"FrameManager",
	"InspectionEngine",
	"InteractionEngine",
	"NavigationStateProvider",
	"NetworkEvents",
	"NetworkPolicyReporter",
	"OverlayHost",
	"RuntimeEvents",
	"ScreenshotEngine",
	"ScreenshotOptionsEngine",
	"ScriptDisabler",
	"SettingsEngine",
	"TabManager",
}

// Capabilities is the stable, machine-readable capability report an engine
// gives for itself (issue #295). Kind names the engine implementation
// ("chrome", "static", …); LaunchMode reports how the engine acquired its
// browser ("launch" when it started one, "attach" when it connected to an
// existing session, empty when not applicable); Interfaces lists the optional
// extensions the engine genuinely implements; Unsupported lists the known
// optional extensions it does not. Agents use this report to adapt instead of
// guessing whether an operation will work.
type Capabilities struct {
	Kind        string   `json:"kind"`
	LaunchMode  string   `json:"launch_mode,omitempty"`
	Interfaces  []string `json:"interfaces"`
	Unsupported []string `json:"unsupported,omitempty"`
}

// CapabilityReporter is an optional engine extension (issue #295). Engines
// report which optional interfaces they genuinely implement so callers can
// adapt to the active engine instead of type-asserting per call site.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// CapabilitiesFor builds a capability report for an engine of the given kind
// that implements the named optional interfaces. The report is complete:
// Interfaces and Unsupported are the two halves of OptionalInterfaceNames.
func CapabilitiesFor(kind string, implemented ...string) Capabilities {
	present := make(map[string]bool, len(implemented))
	for _, name := range implemented {
		present[name] = true
	}
	caps := Capabilities{Kind: kind}
	for _, name := range OptionalInterfaceNames {
		if present[name] {
			caps.Interfaces = append(caps.Interfaces, name)
		} else {
			caps.Unsupported = append(caps.Unsupported, name)
		}
	}
	return caps
}

// UnsupportedOperationError is the typed failure for an operation the active
// engine does not support (issue #295). It is deliberately distinct from a
// runtime failure of a supported operation: callers can detect it with
// errors.As and adapt, while every other error is a real execution failure.
type UnsupportedOperationError struct {
	// Engine is the engine implementation that refused the operation.
	Engine string
	// Operation names the refused operation (for example "network.har").
	Operation string
}

func (e *UnsupportedOperationError) Error() string {
	return fmt.Sprintf("engine %q does not support %s", e.Engine, e.Operation)
}

// UnsupportedOperation builds the typed unsupported-operation error.
func UnsupportedOperation(engineName, operation string) error {
	return &UnsupportedOperationError{Engine: engineName, Operation: operation}
}
