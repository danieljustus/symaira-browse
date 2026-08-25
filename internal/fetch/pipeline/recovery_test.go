package pipeline

import (
	"context"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
)

func TestRecoveryProbeFindsAncestorCandidates(t *testing.T) {
	client := &fakeClient{resp: &fetch.Response{
		StatusCode: 200,
		Body:       []byte(`<html><body><a href="/docs/right-page">Right page</a></body></html>`),
	}}

	hints := Probe(context.Background(), client, "https://example.com/docs/right-page", Options{
		Security: SecurityOptions{AllowPrivate: true},
	})
	if hints == nil {
		t.Fatal("expected recovery hints")
	}
	if hints.NearestAncestor != "https://example.com/docs/" {
		t.Fatalf("NearestAncestor = %q, want https://example.com/docs/", hints.NearestAncestor)
	}
	if len(hints.Candidates) != 1 || hints.Candidates[0].URL != "https://example.com/docs/right-page" {
		t.Fatalf("unexpected candidates: %#v", hints.Candidates)
	}
}

func TestRecoveryParseSitemapXML(t *testing.T) {
	links := parseSitemapXML([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
		<url><loc>https://example.com/docs/guide</loc></url>
		<url><loc>javascript:alert(1)</loc></url>
	</urlset>`))
	if len(links) != 1 || links[0].URL != "https://example.com/docs/guide" {
		t.Fatalf("unexpected sitemap links: %#v", links)
	}
}

func TestRecoveryRankCandidates(t *testing.T) {
	candidates := rankCandidates([]ancestorLink{
		{URL: "https://example.com/docs/guide", Title: "Guide"},
		{URL: "https://example.com/docs/guide-old", Title: "Old guide"},
		{URL: "https://example.com/docs/unrelated", Title: "Unrelated"},
	}, "guide", "test", 2)
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2: %#v", len(candidates), candidates)
	}
	if candidates[0].URL != "https://example.com/docs/guide" {
		t.Fatalf("top candidate = %q, want exact guide URL", candidates[0].URL)
	}
}
