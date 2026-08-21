package pipeline_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
)

func TestFetchError(t *testing.T) {
	inner := errors.New("connection refused")
	err := &pipeline.FetchError{URL: "https://example.com", Err: inner}

	if err.Error() != "fetch https://example.com: connection refused" {
		t.Errorf("Error() = %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap() should return inner error")
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error")
	}
}

func TestParseError(t *testing.T) {
	inner := errors.New("invalid HTML")
	err := &pipeline.ParseError{URL: "https://example.com", Err: inner}

	if err.Error() != "parse https://example.com: invalid HTML" {
		t.Errorf("Error() = %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap() should return inner error")
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error")
	}
}

func TestRenderError(t *testing.T) {
	inner := errors.New("template execution failed")
	err := &pipeline.RenderError{Format: "markdown", Err: inner}

	if err.Error() != "render markdown: template execution failed" {
		t.Errorf("Error() = %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap() should return inner error")
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error")
	}
}

func TestBlockedError(t *testing.T) {
	err := &pipeline.BlockedError{URL: "http://127.0.0.1", Reason: "SSRF blocked"}

	if err.Error() != "blocked http://127.0.0.1: SSRF blocked" {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestErrorsAs_FetchError(t *testing.T) {
	inner := fmt.Errorf("timeout")
	err := &pipeline.FetchError{URL: "https://example.com", Err: inner}

	var target *pipeline.FetchError
	if !errors.As(err, &target) {
		t.Error("errors.As should find *FetchError")
	}
	if target.URL != "https://example.com" {
		t.Errorf("target.URL = %q", target.URL)
	}
}

func TestErrorsAs_ParseError(t *testing.T) {
	inner := fmt.Errorf("bad markup")
	err := &pipeline.ParseError{URL: "https://example.com", Err: inner}

	var target *pipeline.ParseError
	if !errors.As(err, &target) {
		t.Error("errors.As should find *ParseError")
	}
}

func TestErrorsAs_RenderError(t *testing.T) {
	inner := fmt.Errorf("render failed")
	err := &pipeline.RenderError{Format: "json", Err: inner}

	var target *pipeline.RenderError
	if !errors.As(err, &target) {
		t.Error("errors.As should find *RenderError")
	}
	if target.Format != "json" {
		t.Errorf("target.Format = %q", target.Format)
	}
}

func TestErrorsAs_BlockedError(t *testing.T) {
	err := &pipeline.BlockedError{URL: "http://10.0.0.1", Reason: "private"}

	var target *pipeline.BlockedError
	if !errors.As(err, &target) {
		t.Error("errors.As should find *BlockedError")
	}
	if target.Reason != "private" {
		t.Errorf("target.Reason = %q", target.Reason)
	}
}

func TestFetchErrorWithRecovery(t *testing.T) {
	inner := fmt.Errorf("HTTP 404")
	err := &pipeline.FetchError{
		URL: "https://example.com/a/b/c",
		Err: inner,
		Recovery: &pipeline.RecoveryHints{
			NearestAncestor: "https://example.com/a/b/",
			AncestorStatus:  200,
		},
	}

	if err.Error() != "fetch https://example.com/a/b/c: HTTP 404" {
		t.Errorf("Error() = %q", err.Error())
	}
	if err.Unwrap() != inner {
		t.Error("Unwrap() should return inner error")
	}
	if err.Recovery == nil {
		t.Fatal("expected non-nil Recovery")
	}
	if err.Recovery.NearestAncestor != "https://example.com/a/b/" {
		t.Errorf("Recovery.NearestAncestor = %q", err.Recovery.NearestAncestor)
	}
	if err.Recovery.AncestorStatus != 200 {
		t.Errorf("Recovery.AncestorStatus = %d", err.Recovery.AncestorStatus)
	}
}

func TestFetchErrorWithoutRecovery(t *testing.T) {
	inner := fmt.Errorf("HTTP 500")
	err := &pipeline.FetchError{
		URL: "https://example.com",
		Err: inner,
	}

	if err.Recovery != nil {
		t.Errorf("expected nil Recovery, got %+v", err.Recovery)
	}
}

func TestFetchErrorRecoveryJSON(t *testing.T) {
	fe := &pipeline.FetchError{
		URL: "https://example.com/a/b/c",
		Err: fmt.Errorf("HTTP 404"),
		Recovery: &pipeline.RecoveryHints{
			NearestAncestor: "https://example.com/a/b/",
			AncestorStatus:  200,
		},
	}

	type jsonErr struct {
		URL      string `json:"url"`
		Recovery *struct {
			NearestAncestor string `json:"nearest_ancestor"`
			AncestorStatus  int    `json:"ancestor_status"`
		} `json:"recovery,omitempty"`
	}

	j := jsonErr{
		URL: fe.URL,
	}
	if fe.Recovery != nil {
		j.Recovery = &struct {
			NearestAncestor string `json:"nearest_ancestor"`
			AncestorStatus  int    `json:"ancestor_status"`
		}{
			NearestAncestor: fe.Recovery.NearestAncestor,
			AncestorStatus:  fe.Recovery.AncestorStatus,
		}
	}

	data, marshalErr := json.Marshal(j)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}

	var out jsonErr
	if unmarshalErr := json.Unmarshal(data, &out); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if out.Recovery == nil {
		t.Fatal("expected non-nil Recovery after JSON round-trip")
	}
	if out.Recovery.NearestAncestor != "https://example.com/a/b/" {
		t.Errorf("NearestAncestor = %q after round-trip", out.Recovery.NearestAncestor)
	}
	if out.Recovery.AncestorStatus != 200 {
		t.Errorf("AncestorStatus = %d after round-trip", out.Recovery.AncestorStatus)
	}
}

func TestFetchErrorRecoveryUnwrap(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	err := &pipeline.FetchError{
		URL: "https://example.com",
		Err: inner,
		Recovery: &pipeline.RecoveryHints{
			NearestAncestor: "https://example.com/",
			AncestorStatus:  200,
		},
	}

	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error even with Recovery set")
	}

	var target *pipeline.FetchError
	if !errors.As(err, &target) {
		t.Error("errors.As should find *FetchError")
	}
	if target.Recovery == nil {
		t.Fatal("Recovery should survive errors.As")
	}
}

func TestFetchErrorRecoveryCandidatesJSON(t *testing.T) {
	fe := &pipeline.FetchError{
		URL: "https://example.com/a/b/c",
		Err: fmt.Errorf("HTTP 404"),
		Recovery: &pipeline.RecoveryHints{
			NearestAncestor: "https://example.com/a/b/",
			AncestorStatus:  200,
			Candidates: []pipeline.CandidateURL{
				{URL: "https://example.com/a/b/x", Title: "X Page", Source: "ancestor-links", Score: 0.9},
				{URL: "https://example.com/a/b/y", Title: "Y Page", Source: "sitemap", Score: 0.7},
			},
		},
	}

	type jsonCand struct {
		URL    string  `json:"url"`
		Title  string  `json:"title"`
		Source string  `json:"source"`
		Score  float64 `json:"score"`
	}
	type jsonRecovery struct {
		NearestAncestor string     `json:"nearest_ancestor"`
		AncestorStatus  int        `json:"ancestor_status"`
		Candidates      []jsonCand `json:"candidates,omitempty"`
	}
	type jsonErr struct {
		URL      string        `json:"url"`
		Recovery *jsonRecovery `json:"recovery,omitempty"`
	}

	j := jsonErr{
		URL: fe.URL,
		Recovery: &jsonRecovery{
			NearestAncestor: fe.Recovery.NearestAncestor,
			AncestorStatus:  fe.Recovery.AncestorStatus,
			Candidates: []jsonCand{
				{URL: fe.Recovery.Candidates[0].URL, Title: fe.Recovery.Candidates[0].Title, Source: fe.Recovery.Candidates[0].Source, Score: fe.Recovery.Candidates[0].Score},
				{URL: fe.Recovery.Candidates[1].URL, Title: fe.Recovery.Candidates[1].Title, Source: fe.Recovery.Candidates[1].Source, Score: fe.Recovery.Candidates[1].Score},
			},
		},
	}

	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}

	var out jsonErr
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Recovery == nil || len(out.Recovery.Candidates) != 2 {
		t.Fatalf("expected 2 candidates after round-trip, got %d", len(out.Recovery.Candidates))
	}
	if out.Recovery.Candidates[0].URL != "https://example.com/a/b/x" {
		t.Errorf("candidate 0 URL = %q", out.Recovery.Candidates[0].URL)
	}
	if out.Recovery.Candidates[0].Score != 0.9 {
		t.Errorf("candidate 0 Score = %f", out.Recovery.Candidates[0].Score)
	}
	if out.Recovery.Candidates[1].Source != "sitemap" {
		t.Errorf("candidate 1 Source = %q", out.Recovery.Candidates[1].Source)
	}
}

func TestFetchErrorRecoveryCandidatesUnwrap(t *testing.T) {
	inner := fmt.Errorf("HTTP 404")
	err := &pipeline.FetchError{
		URL: "https://example.com/a/b/c",
		Err: inner,
		Recovery: &pipeline.RecoveryHints{
			NearestAncestor: "https://example.com/a/b/",
			AncestorStatus:  200,
			Candidates: []pipeline.CandidateURL{
				{URL: "https://example.com/a/b/x", Title: "X", Source: "ancestor-links", Score: 0.9},
			},
		},
	}

	if !errors.Is(err, inner) {
		t.Error("errors.Is should find inner error even with Candidates set")
	}
	if len(err.Recovery.Candidates) != 1 {
		t.Errorf("expected 1 candidate, got %d", len(err.Recovery.Candidates))
	}
}
