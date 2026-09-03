package daemon

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// fetchError maps a pipeline failure onto the daemon error taxonomy
// (docs/errors.md). Every fetch failure used to arrive as operation_failed,
// which left a caller unable to tell a refused target from a mistyped
// selector from a host that was merely slow — and therefore unable to decide
// whether retrying could help.
func fetchError(err error) *Error {
	if err == nil {
		return nil
	}

	// A refused target is a policy decision, not a failure of the fetch:
	// retrying it unchanged will be refused again.
	var blockedPrivate *policy.BlockedPrivateError
	if errors.As(err, &blockedPrivate) {
		return typedFetchError(ErrorPeerDenied, err, false,
			"the target is a private or loopback address; start the daemon with --allow-private to permit it")
	}
	var blocked *pipeline.BlockedError
	if errors.As(err, &blocked) {
		return typedFetchError(ErrorPeerDenied, err, false,
			"the site's robots.txt disallows this path")
	}

	// Bad arguments: the request itself has to change.
	var selector *pipeline.SelectorError
	if errors.As(err, &selector) {
		return typedFetchError(ErrorMalformedRequest, err, false,
			"pick a selector that matches the page, or omit css_selector to fetch the whole document")
	}
	var schema *pipeline.SchemaError
	if errors.As(err, &schema) {
		return typedFetchError(ErrorMalformedRequest, err, false,
			"check schema_path against the page's JSON-LD, or omit it")
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return typedFetchError(ErrorOperationTimeout, err, true,
			"retry, or raise the operation timeout for slow hosts")
	}

	var fetchErr *pipeline.FetchError
	if errors.As(err, &fetchErr) {
		return fetchTransportError(err, fetchErr)
	}

	// Parse and render failures are deterministic: the same bytes produce the
	// same failure, so retrying is pointless.
	var parseErr *pipeline.ParseError
	var renderErr *pipeline.RenderError
	if errors.As(err, &parseErr) || errors.As(err, &renderErr) {
		return typedFetchError(ErrorOperationFailed, err, false,
			"try format=text or raw=true to retrieve the page without semantic processing")
	}

	return typedFetchError(ErrorOperationFailed, err, false, "")
}

// fetchTransportError classifies a fetch that reached the transport layer and
// attaches the 404 recovery candidates the pipeline discovered.
func fetchTransportError(err error, fetchErr *pipeline.FetchError) *Error {
	// A network-level failure carries no status: DNS, connection reset and
	// timeouts are all worth another attempt.
	var netErr net.Error
	if fetchErr.StatusCode == 0 {
		if errors.As(err, &netErr) && netErr.Timeout() {
			return typedFetchError(ErrorOperationTimeout, err, true,
				"retry, or raise the operation timeout for slow hosts")
		}
		return typedFetchError(ErrorOperationFailed, err, true,
			"retry; the host was not reachable on this attempt")
	}

	switch {
	case fetchErr.StatusCode == 408 || fetchErr.StatusCode == 429:
		return typedFetchError(ErrorOperationTimeout, err, true,
			"the host asked to slow down; retry after a pause")
	case fetchErr.StatusCode >= 500:
		return typedFetchError(ErrorOperationFailed, err, true,
			"the host failed on its side; retry")
	case fetchErr.StatusCode == 401 || fetchErr.StatusCode == 403:
		return typedFetchError(ErrorPeerDenied, err, false,
			"the host refused the request; a browser session with credentials may be required")
	}

	// 4xx, including the 404 the recovery probe runs for.
	daemonErr := typedFetchError(ErrorOperationFailed, err, false,
		"check the URL; the response carries candidate replacements when the probe found any")
	if recovery := recoveryDetails(fetchErr.Recovery); recovery != nil {
		daemonErr.Details = map[string]any{"recovery": recovery}
	}
	return daemonErr
}

// recoveryDetails renders the pipeline's 404 recovery hints as the
// machine-readable details payload. The probe costs real HTTP round-trips, so
// its result belongs in the response rather than in a discarded struct field.
func recoveryDetails(hints *pipeline.RecoveryHints) map[string]any {
	if hints == nil {
		return nil
	}
	candidates := make([]map[string]any, 0, len(hints.Candidates))
	for _, candidate := range hints.Candidates {
		candidates = append(candidates, map[string]any{
			"url":    candidate.URL,
			"title":  candidate.Title,
			"source": candidate.Source,
			"score":  candidate.Score,
		})
	}
	if hints.NearestAncestor == "" && len(candidates) == 0 {
		return nil
	}
	details := map[string]any{"candidates": candidates}
	if hints.NearestAncestor != "" {
		details["nearest_ancestor"] = hints.NearestAncestor
		details["ancestor_status"] = hints.AncestorStatus
	}
	return details
}

// typedFetchError builds the structured error payload.
func typedFetchError(code string, err error, retryable bool, resumeHint string) *Error {
	daemonErr := NewError(code, err.Error())
	daemonErr.Retryable = &retryable
	daemonErr.ResumeHint = resumeHint
	return daemonErr
}
