package pipeline

import (
	"context"
	"encoding/xml"
	"log/slog"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
)

// Probe walks ancestor pages and sitemaps to produce recovery hints for a
// not-found URL. The heuristics and security checks live in this file so the
// fetch pipeline only needs one recovery entry point.
func Probe(ctx context.Context, c fetch.Client, rawURL string, o Options) *RecoveryHints {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}

	path := u.Path
	if path == "" {
		path = "/"
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) <= 1 {
		return nil
	}

	failedSegment := strings.ToLower(segments[len(segments)-1])

	for i := len(segments) - 1; i >= 1; i-- {
		ancestorPath := "/" + strings.Join(segments[:i], "/") + "/"
		ancestorURL := *u
		ancestorURL.Path = ancestorPath
		ancestorURL.RawPath = ""
		ancestorURL.RawQuery = ""
		ancestorStr := ancestorURL.String()

		if !o.Security.AllowPrivate {
			if err := fetch.CheckSSRF(ancestorStr); err != nil {
				slog.Debug("ancestor SSRF blocked", "url", ancestorStr)
				return nil
			}
		}
		if err := fetch.CheckAllowlist(ancestorStr, o.Security.Allowlist); err != nil {
			slog.Debug("ancestor blocked by domain allowlist", "url", ancestorStr)
			return nil
		}

		if o.Security.Robots && o.Security.RobotsChecker != nil {
			ua := o.Security.UserAgent
			if ua == "" {
				ua = "symfetch"
			}
			allowed, err := o.Security.RobotsChecker.Check(ctx, ua, ancestorStr)
			if err != nil {
				slog.Debug("ancestor robots check error", "url", ancestorStr, "error", err)
			} else if !allowed {
				slog.Debug("ancestor blocked by robots.txt", "url", ancestorStr)
				return nil
			}
		}

		resp, err := c.Fetch(ctx, fetch.Request{
			URL:          ancestorStr,
			AllowPrivate: o.Security.AllowPrivate,
			Session:      o.Session,
		})
		if err != nil {
			slog.Debug("ancestor probe error", "url", ancestorStr, "error", err)
			return nil
		}

		if resp.StatusCode < 400 {
			hints := &RecoveryHints{
				NearestAncestor: ancestorStr,
				AncestorStatus:  resp.StatusCode,
			}
			hints.Candidates = findCandidatesFromAncestor(resp, ancestorStr, failedSegment)
			if len(hints.Candidates) == 0 {
				hints.Candidates = findCandidatesFromSitemaps(ctx, c, u, failedSegment, o)
			}
			return hints
		}
	}

	rootURL := *u
	rootURL.Path = "/"
	rootURL.RawPath = ""
	rootURL.RawQuery = ""
	rootStr := rootURL.String()

	if !o.Security.AllowPrivate {
		if err := fetch.CheckSSRF(rootStr); err != nil {
			return nil
		}
	}
	if err := fetch.CheckAllowlist(rootStr, o.Security.Allowlist); err != nil {
		return nil
	}

	if o.Security.Robots && o.Security.RobotsChecker != nil {
		ua := o.Security.UserAgent
		if ua == "" {
			ua = "symfetch"
		}
		allowed, err := o.Security.RobotsChecker.Check(ctx, ua, rootStr)
		if err != nil {
			slog.Debug("root robots check error", "url", rootStr, "error", err)
		} else if !allowed {
			return nil
		}
	}

	resp, err := c.Fetch(ctx, fetch.Request{
		URL:          rootStr,
		AllowPrivate: o.Security.AllowPrivate,
		Session:      o.Session,
	})
	if err != nil {
		return nil
	}

	if resp.StatusCode < 400 {
		hints := &RecoveryHints{
			NearestAncestor: rootStr,
			AncestorStatus:  resp.StatusCode,
		}
		hints.Candidates = findCandidatesFromAncestor(resp, rootStr, failedSegment)
		if len(hints.Candidates) == 0 {
			hints.Candidates = findCandidatesFromSitemaps(ctx, c, u, failedSegment, o)
		}
		return hints
	}

	return nil
}

// probeAncestors is retained for package-internal regression tests; production
// callers use the exported Probe entry point.
func probeAncestors(ctx context.Context, c fetch.Client, rawURL string, o Options) *RecoveryHints {
	return Probe(ctx, c, rawURL, o)
}

type ancestorLink struct {
	URL   string
	Title string
}

func findCandidatesFromAncestor(resp *fetch.Response, ancestorStr, failedSegment string) []CandidateURL {
	if len(resp.Body) == 0 {
		return nil
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(resp.Body)))
	if err != nil {
		return nil
	}
	base, err := url.Parse(ancestorStr)
	if err != nil {
		return nil
	}
	var links []ancestorLink
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists || !isSafeHref(href) {
			return
		}
		// Parse the href before resolving it. Putting the raw string into
		// URL.Path treats an absolute href as a path segment, which
		// concatenates it onto the base and yields a URL that goes nowhere.
		ref, err := url.Parse(href)
		if err != nil {
			return
		}
		resolved := base.ResolveReference(ref)
		if resolved == nil || !isHTTPScheme(resolved.Scheme) {
			return
		}
		title := strings.TrimSpace(s.Text())
		links = append(links, ancestorLink{URL: resolved.String(), Title: title})
	})
	return rankCandidates(links, failedSegment, "ancestor-links", 3)
}

func rankCandidates(links []ancestorLink, failedSegment, source string, topN int) []CandidateURL {
	if len(links) == 0 || failedSegment == "" {
		return nil
	}
	var candidates []CandidateURL
	for _, l := range links {
		linkURL, err := url.Parse(l.URL)
		if err != nil {
			continue
		}
		linkPath := linkURL.Path
		linkSegments := strings.Split(strings.Trim(linkPath, "/"), "/")
		if len(linkSegments) == 0 {
			continue
		}
		linkSlug := strings.ToLower(linkSegments[len(linkSegments)-1])
		titleLower := strings.ToLower(l.Title)
		score := fuzzyScore(failedSegment, linkSlug, titleLower)
		if score > 0 {
			candidates = append(candidates, CandidateURL{
				URL:    l.URL,
				Title:  l.Title,
				Source: source,
				Score:  score,
			})
		}
	}
	sortCandidates(candidates)
	if len(candidates) > topN {
		candidates = candidates[:topN]
	}
	return candidates
}

func fuzzyScore(target, slug, title string) float64 {
	if target == "" || slug == "" {
		return 0
	}
	if target == slug {
		return 1.0
	}
	if slug == target || strings.HasPrefix(slug, target) || strings.HasSuffix(slug, target) {
		return 0.8
	}
	if len(target) >= 3 && strings.Contains(slug, target) {
		return 0.7
	}
	if len(target) >= 3 && strings.Contains(title, target) {
		return 0.6
	}
	lev := levenshtein(target, slug)
	maxLen := len(target)
	if len(slug) > maxLen {
		maxLen = len(slug)
	}
	if maxLen == 0 {
		return 0
	}
	sim := 1.0 - float64(lev)/float64(maxLen)
	if sim >= 0.6 {
		return sim * 0.9
	}
	return 0
}

// maxLevenshteinInput bounds attacker-controlled sitemap/link strings before
// the quadratic comparison runs. Fixed-size buffers also avoid an allocation
// whose capacity computation could overflow on a very large input.
const maxLevenshteinInput = 4096

func levenshtein(a, b string) int {
	la := len(a)
	lb := len(b)
	if la > maxLevenshteinInput || lb > maxLevenshteinInput {
		if la > lb {
			return la
		}
		return lb
	}
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	var prev [maxLevenshteinInput + 1]int
	var curr [maxLevenshteinInput + 1]int
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func sortCandidates(cands []CandidateURL) {
	for i := 1; i < len(cands); i++ {
		key := cands[i]
		j := i - 1
		for j >= 0 && cands[j].Score < key.Score {
			cands[j+1] = cands[j]
			j--
		}
		cands[j+1] = key
	}
}

const maxSitemapBytes = 5 << 20
const maxSitemapEntries = 1000

type sitemapURL struct {
	Loc string `xml:"loc"`
}

type sitemapURLset struct {
	XMLName xml.Name     `xml:"urlset"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapSitemap struct {
	Loc string `xml:"loc"`
}

type sitemapIndex struct {
	XMLName  xml.Name         `xml:"sitemapindex"`
	Sitemaps []sitemapSitemap `xml:"sitemap"`
}

func findCandidatesFromSitemaps(ctx context.Context, c fetch.Client, u *url.URL, failedSegment string, o Options) []CandidateURL {
	if o.Security.RobotsChecker == nil {
		return nil
	}
	ua := o.Security.UserAgent
	if ua == "" {
		ua = "symfetch"
	}
	sitemapURLs, err := o.Security.RobotsChecker.Sitemaps(ctx, ua, u.String())
	if err != nil || len(sitemapURLs) == 0 {
		return nil
	}
	var allLinks []ancestorLink
	for _, smURL := range sitemapURLs {
		if !o.Security.AllowPrivate {
			if err := fetch.CheckSSRF(smURL); err != nil {
				slog.Debug("sitemap SSRF blocked", "url", smURL)
				continue
			}
		}
		if err := fetch.CheckAllowlist(smURL, o.Security.Allowlist); err != nil {
			slog.Debug("sitemap blocked by domain allowlist", "url", smURL)
			continue
		}
		if o.Security.Robots {
			allowed, err := o.Security.RobotsChecker.Check(ctx, ua, smURL)
			if err != nil {
				slog.Debug("sitemap robots check error", "url", smURL, "error", err)
				continue
			}
			if !allowed {
				continue
			}
		}
		entries := fetchSitemap(ctx, c, smURL, o)
		allLinks = append(allLinks, entries...)
		if len(allLinks) >= maxSitemapEntries {
			break
		}
	}
	if len(allLinks) > maxSitemapEntries {
		allLinks = allLinks[:maxSitemapEntries]
	}
	return rankCandidates(allLinks, failedSegment, "sitemap", 3)
}

func fetchSitemap(ctx context.Context, c fetch.Client, smURL string, o Options) []ancestorLink {
	resp, err := c.Fetch(ctx, fetch.Request{
		URL:          smURL,
		AllowPrivate: o.Security.AllowPrivate,
		Session:      o.Session,
		MaxBody:      maxSitemapBytes,
		Allowlist:    o.Security.Allowlist,
	})
	if err != nil {
		slog.Debug("sitemap fetch error", "url", smURL, "error", err)
		return nil
	}
	body := resp.Body
	if int64(len(body)) > maxSitemapBytes {
		body = body[:maxSitemapBytes]
	}
	trimmed := strings.TrimSpace(string(body))
	var links []ancestorLink
	if strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<urlset") || strings.HasPrefix(trimmed, "<sitemapindex") {
		links = append(links, parseSitemapXML(body)...)
	}
	return links
}

func parseSitemapXML(data []byte) []ancestorLink {
	var urls sitemapURLset
	if err := xml.Unmarshal(data, &urls); err == nil && len(urls.URLs) > 0 {
		var links []ancestorLink
		for _, u := range urls.URLs {
			if u.Loc != "" && isSafeHref(u.Loc) {
				parsed, err := url.Parse(u.Loc)
				if err != nil || parsed == nil || !isHTTPScheme(parsed.Scheme) {
					continue
				}
				links = append(links, ancestorLink{URL: u.Loc})
			}
		}
		return links
	}
	var idx sitemapIndex
	if err := xml.Unmarshal(data, &idx); err == nil && len(idx.Sitemaps) > 0 {
		var links []ancestorLink
		for _, sm := range idx.Sitemaps {
			if sm.Loc != "" && isSafeHref(sm.Loc) {
				parsed, err := url.Parse(sm.Loc)
				if err != nil || parsed == nil || !isHTTPScheme(parsed.Scheme) {
					continue
				}
				links = append(links, ancestorLink{URL: sm.Loc})
			}
		}
		return links
	}
	return nil
}

// isSafeHref reports whether href is a candidate for recovery suggestion.
// It rejects empty values, fragment-only anchors, and executable/data pseudo-schemes
// (javascript:, data:, vbscript:) to avoid reflecting attacker-controlled URLs.
func isSafeHref(href string) bool {
	if href == "" || href[0] == '#' {
		return false
	}
	lower := strings.ToLower(href)
	return !strings.HasPrefix(lower, "javascript:") &&
		!strings.HasPrefix(lower, "data:") &&
		!strings.HasPrefix(lower, "vbscript:")
}

// isHTTPScheme reports whether scheme is http or https, the only schemes recovery
// suggestions are allowed to surface.
func isHTTPScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

// isWaybackURL reports whether the URL targets the Wayback Machine.
func isWaybackURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://web.archive.org/web/")
}
