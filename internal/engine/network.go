package engine

import (
	"context"
	"encoding/json"
	"time"
)

// NetworkRequest is one captured HTTP request (issue #59). Header values
// for sensitive keys (Authorization, Cookie, ...) are masked by the engine
// before they reach the protocol.
type NetworkRequest struct {
	ID              string            `json:"id"`
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Type            string            `json:"type"`
	Status          int               `json:"status"`
	StatusText      string            `json:"status_text,omitempty"`
	MimeType        string            `json:"mime_type,omitempty"`
	StartedDateTime time.Time         `json:"started_at"`
	Finished        bool              `json:"finished"`
	Failed          string            `json:"failed,omitempty"`
	RequestHeaders  map[string]string `json:"request_headers,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	EncodedBodySize int64             `json:"encoded_body_size,omitempty"`
}

// NetworkRoute mocks one request pattern. Pattern is a literal URL or a
// URL prefix when it ends with "*". Action is "abort" or "mock".
type NetworkRoute struct {
	Pattern     string          `json:"pattern"`
	Action      string          `json:"action"` // abort | mock
	Status      int             `json:"status,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	ContentType string          `json:"content_type,omitempty"`
}

// HAROptions controls HAR export (issue #59): Content "all" includes
// response bodies, "none" only metadata.
type HAROptions struct {
	Content string // "all" | "none"
}

// NetworkEvents is the optional engine capability behind the network
// commands (issue #59). EnableNetworkCapture turns on Network domain
// capture for a page session (idempotent); routes intercept matching
// requests via Fetch.
type NetworkEvents interface {
	EnableNetworkCapture(context.Context, Page) error
	Requests(Page) []NetworkRequest
	Request(Page, string) (NetworkRequest, bool)
	RouteRequests(context.Context, Page, NetworkRoute) error
	UnrouteRequests(context.Context, Page, string) (bool, error)
	HAR(context.Context, Page, HAROptions) ([]byte, error)
}
