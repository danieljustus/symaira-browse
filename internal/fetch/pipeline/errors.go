package pipeline

import (
	"fmt"
)

// CandidateURL is a suggested replacement URL discovered during 404 recovery.
type CandidateURL struct {
	URL    string  // absolute candidate URL
	Title  string  // link text or <title> of the page, if available
	Source string  // provenance: "ancestor-links" or "sitemap"
	Score  float64 // 0–1 similarity score; higher is better
}

type RecoveryHints struct {
	NearestAncestor string // nearest ancestor URL that returned status < 400
	AncestorStatus  int    // HTTP status of that ancestor
	Candidates      []CandidateURL
}

type FetchError struct {
	URL      string
	Err      error
	Recovery *RecoveryHints // nil when no recovery hint is available
	// StatusCode is the upstream HTTP status code that caused this error,
	// or 0 when the failure occurred before a response was received
	// (network, DNS, timeout, or body-size errors).
	StatusCode int
}

func (e *FetchError) Error() string {
	return fmt.Sprintf("fetch %s: %v", e.URL, e.Err)
}

func (e *FetchError) Unwrap() error {
	return e.Err
}

type ParseError struct {
	URL string
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse %s: %v", e.URL, e.Err)
}

func (e *ParseError) Unwrap() error {
	return e.Err
}

type RenderError struct {
	Format string
	Err    error
}

func (e *RenderError) Error() string {
	return fmt.Sprintf("render %s: %v", e.Format, e.Err)
}

func (e *RenderError) Unwrap() error {
	return e.Err
}

type BlockedError struct {
	URL    string
	Reason string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("blocked %s: %s", e.URL, e.Reason)
}

type SelectorError struct {
	Selector string
}

func (e *SelectorError) Error() string {
	return fmt.Sprintf("selector %q matched no elements", e.Selector)
}

type SchemaError struct {
	Path string
	Err  string
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("schema query %q: %s", e.Path, e.Err)
}
