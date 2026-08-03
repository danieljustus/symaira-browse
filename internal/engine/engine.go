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
