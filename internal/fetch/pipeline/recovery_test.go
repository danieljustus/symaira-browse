package pipeline

import (
	"context"
	"net/url"
	"strings"
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

func TestLevenshteinBoundsOversizedInput(t *testing.T) {
	large := strings.Repeat("x", maxLevenshteinInput+1)
	if got := levenshtein(large, "target"); got != len(large) {
		t.Fatalf("levenshtein oversized input = %d, want %d", got, len(large))
	}
	if got := fuzzyScore("target", large, ""); got != 0 {
		t.Fatalf("fuzzyScore oversized input = %f, want 0", got)
	}
}

// TestFindCandidatesResolvesHrefForms verifies candidate URLs are resolved
// rather than concatenated. An absolute href used to be pushed into the
// base's path, producing a URL that leads nowhere — and because the hints
// were never delivered, nothing noticed.
func TestFindCandidatesResolvesHrefForms(t *testing.T) {
	const ancestor = "https://example.com/docs/"
	body := `<!doctype html><html><body>
		<a href="https://example.com/docs/getting-started">Absolute</a>
		<a href="/docs/getting-started-relative">Root relative</a>
		<a href="getting-started-doc">Path relative</a>
		<a href="getting-started-query?v=2">With query</a>
	</body></html>`

	candidates := findCandidatesFromAncestor(
		&fetch.Response{Body: []byte(body)}, ancestor, "getting-started")

	if len(candidates) == 0 {
		t.Fatal("no candidates extracted")
	}
	for _, candidate := range candidates {
		parsed, err := url.Parse(candidate.URL)
		if err != nil {
			t.Errorf("candidate %q is not a valid URL: %v", candidate.URL, err)
			continue
		}
		if parsed.Host != "example.com" {
			t.Errorf("candidate %q has host %q, want example.com", candidate.URL, parsed.Host)
		}
		if strings.Contains(parsed.Path, "https:") || strings.Contains(parsed.Path, "//") {
			t.Errorf("candidate %q has a concatenated path %q", candidate.URL, parsed.Path)
		}
	}
}
