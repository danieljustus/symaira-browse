package archive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCDXLookup_SingleSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") != "https://example.com" {
			t.Errorf("expected url=https://example.com, got %s", r.URL.Query().Get("url"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([][]string{
			{"timestamp", "original", "mimetype", "statuscode", "digest", "length"},
			{"20260101120000", "https://example.com", "text/html", "200", "abc123", "1234"},
		})
	}))
	defer server.Close()

	client := NewCDXClient(server.URL, server.Client())
	snaps, err := client.Lookup(context.Background(), CDXQuery{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	s := snaps[0]
	if s.Timestamp != "20260101120000" {
		t.Errorf("timestamp = %q, want %q", s.Timestamp, "20260101120000")
	}
	if s.Original != "https://example.com" {
		t.Errorf("original = %q, want %q", s.Original, "https://example.com")
	}
	if s.MimeType != "text/html" {
		t.Errorf("mimetype = %q, want %q", s.MimeType, "text/html")
	}
	if s.StatusCode != "200" {
		t.Errorf("statuscode = %q, want %q", s.StatusCode, "200")
	}
}

func TestCDXLookup_EmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Header row only — no data rows.
		json.NewEncoder(w).Encode([][]string{
			{"timestamp", "original", "mimetype", "statuscode", "digest", "length"},
		})
	}))
	defer server.Close()

	client := NewCDXClient(server.URL, server.Client())
	snaps, err := client.Lookup(context.Background(), CDXQuery{URL: "https://no-snapshots.example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots, got %d", len(snaps))
	}
}

func TestCDXLookup_MultipleSnapshots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([][]string{
			{"timestamp", "original", "mimetype", "statuscode", "digest", "length"},
			{"20260101000000", "https://example.com", "text/html", "200", "aaa", "1000"},
			{"20260201000000", "https://example.com", "text/html", "200", "bbb", "1100"},
			{"20260301000000", "https://example.com", "text/html", "404", "ccc", "500"},
		})
	}))
	defer server.Close()

	client := NewCDXClient(server.URL, server.Client())
	snaps, err := client.Lookup(context.Background(), CDXQuery{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snaps) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(snaps))
	}
	if snaps[2].StatusCode != "404" {
		t.Errorf("third snapshot status = %q, want %q", snaps[2].StatusCode, "404")
	}
}

func TestCDXLookup_Parameters(t *testing.T) {
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([][]string{
			{"timestamp", "original", "mimetype", "statuscode", "digest", "length"},
		})
	}))
	defer server.Close()

	client := NewCDXClient(server.URL, server.Client())
	_, err := client.Lookup(context.Background(), CDXQuery{
		URL:       "https://example.com",
		From:      "20260101",
		To:        "20260630",
		Limit:     10,
		MatchType: "prefix",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, key := range []string{"url", "from", "to", "limit", "matchType"} {
		if !containsParam(captured, key) {
			t.Errorf("missing parameter %q in query: %s", key, captured)
		}
	}
}

func TestCDXLookup_EmptyURL(t *testing.T) {
	client := NewCDXClient("", nil)
	_, err := client.Lookup(context.Background(), CDXQuery{URL: ""})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestCDXLookup_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewCDXClient(server.URL, server.Client())
	_, err := client.Lookup(context.Background(), CDXQuery{URL: "https://example.com"})
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}

func TestCDXLookup_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response — context should be cancelled first.
		select {}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	client := NewCDXClient(server.URL, server.Client())
	_, err := client.Lookup(ctx, CDXQuery{URL: "https://example.com"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func containsParam(rawQuery, key string) bool {
	for _, part := range splitQuery(rawQuery) {
		kv := splitPair(part)
		if len(kv) == 2 && kv[0] == key {
			return true
		}
	}
	return false
}

func splitQuery(s string) []string {
	if s == "" {
		return nil
	}
	result := make([]string, 0)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '&' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

func splitPair(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
