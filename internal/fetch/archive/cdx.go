// Package archive provides a client for the Wayback Machine CDX API.
// The CDX (Capture Index) API lists available snapshots for a given URL.
package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CDXBaseURL is the base URL for the Wayback Machine CDX API.
const CDXBaseURL = "https://web.archive.org/cdx/search/cdx"

// Snapshot represents a single Wayback Machine snapshot entry.
type Snapshot struct {
	// Timestamp is the 14-digit capture timestamp (e.g. "20260709120000").
	Timestamp string `json:"timestamp"`
	// Original is the original URL that was captured.
	Original string `json:"original"`
	// MimeType is the MIME type of the captured resource.
	MimeType string `json:"mimetype"`
	// StatusCode is the HTTP status code of the capture (e.g. "200", "404").
	StatusCode string `json:"statuscode"`
	// Digest is a SHA-1 hash of the content for deduplication.
	Digest string `json:"digest"`
	// Length is the raw byte length of the capture.
	Length string `json:"length"`
}

// CDXQuery holds parameters for a CDX API lookup.
type CDXQuery struct {
	// URL is the target URL to look up.
	URL string
	// From is the start timestamp filter (format: "YYYYMMDD" or "YYYYMMDDHHmmss").
	From string
	// To is the end timestamp filter (format: "YYYYMMDD" or "YYYYMMDDHHmmss").
	To string
	// Limit caps the number of results returned. Zero means no limit.
	Limit int
	// MatchType controls URL matching: "exact", "prefix", or "host".
	// Default is "exact".
	MatchType string
}

// CDXClient queries the Wayback Machine CDX API.
type CDXClient struct {
	baseURL string
	client  *http.Client
}

// NewCDXClient creates a CDXClient with the given base URL and HTTP client.
// If baseURL is empty, the default Wayback CDX endpoint is used.
// If httpClient is nil, http.DefaultClient is used.
func NewCDXClient(baseURL string, httpClient *http.Client) *CDXClient {
	if baseURL == "" {
		baseURL = CDXBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &CDXClient{baseURL: baseURL, client: httpClient}
}

// Lookup queries the CDX API and returns matching snapshots.
// It returns an error if the request fails or the response is invalid.
func (c *CDXClient) Lookup(ctx context.Context, query CDXQuery) ([]Snapshot, error) {
	if query.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	params := url.Values{}
	params.Set("url", query.URL)
	params.Set("output", "json")
	params.Set("fl", "timestamp,original,mimetype,statuscode,digest,length")

	if query.From != "" {
		params.Set("from", query.From)
	}
	if query.To != "" {
		params.Set("to", query.To)
	}
	if query.Limit > 0 {
		params.Set("limit", strconv.Itoa(query.Limit))
	}
	if query.MatchType != "" {
		params.Set("matchType", query.MatchType)
	}

	reqURL := c.baseURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "symfetch/0.2")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cdx request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cdx returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return decodeCDXResponse(resp.Body)
}

// decodeCDXResponse parses the CDX JSON response. The CDX API returns a
// JSON array where the first element is the header row and subsequent
// elements are data rows.
func decodeCDXResponse(body io.Reader) ([]Snapshot, error) {
	var raw [][]string
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode cdx response: %w", err)
	}

	// CDX API returns header row as first element, then data rows.
	if len(raw) < 2 {
		// No data rows — empty result.
		return nil, nil
	}

	// First row is the header; skip it.
	snapshots := make([]Snapshot, 0, len(raw)-1)
	for _, row := range raw[1:] {
		if len(row) < 6 {
			continue
		}
		snapshots = append(snapshots, Snapshot{
			Timestamp:  row[0],
			Original:   row[1],
			MimeType:   row[2],
			StatusCode: row[3],
			Digest:     row[4],
			Length:     row[5],
		})
	}

	return snapshots, nil
}
