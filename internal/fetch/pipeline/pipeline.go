package pipeline

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
	"github.com/danieljustus/symaira-browse/internal/fetch/archive"
	"github.com/danieljustus/symaira-browse/internal/fetch/cache"
	"github.com/danieljustus/symaira-browse/internal/fetch/dom"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/relevance"
	"github.com/danieljustus/symaira-browse/internal/fetch/render"
	"github.com/danieljustus/symaira-browse/internal/fetch/robots"
	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
	"golang.org/x/net/html"
)

// Format is the output format for the rendered result.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatJSON     Format = "json"
	FormatText     Format = "text"
	FormatHTML     Format = "html"
)

// ParseFormat parses a string into a Format. Empty values default to markdown;
// unsupported values return an error.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(s) {
	case "", "markdown":
		return FormatMarkdown, nil
	case "json":
		return FormatJSON, nil
	case "text":
		return FormatText, nil
	case "html":
		return FormatHTML, nil
	default:
		return FormatMarkdown, fmt.Errorf("unsupported format %q: expected markdown, json, text, or html", s)
	}
}

// Options configures the pipeline run.
type Options struct {
	Format           Format
	Content          ContentOptions
	Cache            CacheOptions
	Profile          string
	Session          string
	Security         SecurityOptions
	CSSSelector      string // optional CSS selector for targeted extraction
	Frontmatter      bool   // optional YAML frontmatter output
	SchemaPath       string // optional JSON-LD query path like "@Recipe:name"
	DisableFallback  bool   // when true, skip thin-content retry (prevents recursion)
	Request          RequestOptions
	StoreFullText    bool   // enable Hermes-style truncate-and-store for long pages
	CharLimit        int    // per-page char limit for truncate-and-store (default 15000)
	StoreDir         string // directory for storing full text files (default ~/.cache/symfetch/fulltext)
	WaybackFallback  bool   // enable Wayback Machine fallback on 404/thin-content
	WaybackTimestamp string // specific Wayback timestamp to fetch (empty = latest)
	Query            string // optional BM25 query for relevance filtering
	TopK             int    // optional number of top sections to return (0 = all)
}

// RequestOptions carries per-request HTTP parameters for the processed path.
type RequestOptions struct {
	Method  string
	Headers map[string]string
	Body    []byte
}

// ContentOptions controls content extraction limits and scoring.
type ContentOptions struct {
	MaxChars       int // character budget for content output
	IncludeLinks   bool
	CharThreshold  int // minimum chars for content scoring; below this triggers retry
	MaxIslandBytes int // max size of a single data island
}

// CacheOptions controls response caching.
type CacheOptions struct {
	NoCache  bool
	Dir      string
	TTL      time.Duration
	MaxSize  int64        // max cache size in bytes; 0 uses default (100 MB)
	Instance *cache.Cache // shared cache instance; when nil, per-call cache is created
}

// SecurityOptions controls SSRF protection and robots.txt compliance.
type SecurityOptions struct {
	AllowPrivate  bool
	Robots        bool
	RobotsChecker *robots.Checker
}

func (o *Options) setDefaults() {
	if o.Content.MaxChars <= 0 {
		o.Content.MaxChars = 20000
	}
	if o.Content.CharThreshold <= 0 {
		o.Content.CharThreshold = 500
	}
	if o.Content.MaxIslandBytes <= 0 {
		o.Content.MaxIslandBytes = o.Content.MaxChars / 4
	}
	if o.StoreFullText && o.CharLimit <= 0 {
		o.CharLimit = DefaultCharLimit
	}
}

// ContentKey returns a deterministic string encoding every option that
// affects the rendered output so the cache can distinguish requests that
// would produce different results.
func (o *ContentOptions) ContentKey() string {
	return fmt.Sprintf("mc=%d il=%v ct=%d mi=%d", o.MaxChars, o.IncludeLinks, o.CharThreshold, o.MaxIslandBytes)
}

// CacheKey returns a deterministic string encoding every option that
// affects the cached output, including CSSSelector, Frontmatter,
// SchemaPath, StoreFullText, and CharLimit in addition to ContentOptions fields.
func (o *Options) CacheKey() string {
	return fmt.Sprintf("%s cs=%s fm=%v sp=%s sft=%v cl=%d q=%s tk=%d",
		o.Content.ContentKey(), o.CSSSelector, o.Frontmatter, o.SchemaPath,
		o.StoreFullText, o.CharLimit, o.Query, o.TopK)
}

// Result holds the pipeline output.
type Result struct {
	Doc    *agentdom.Document
	Output string
	Meta   agentdom.Meta
}

// Run executes the full semantic pipeline:
// fetch → materialize → filter → score → classify → agentdom → render.
func Run(ctx context.Context, c fetch.Client, eng Engine, rawURL string, o Options) (*Result, error) {
	o.setDefaults()

	if !o.Security.AllowPrivate {
		if err := fetch.CheckSSRF(rawURL); err != nil {
			return nil, err
		}
	}

	var cacher *cache.Cache
	if !o.Cache.NoCache {
		if o.Cache.Instance != nil {
			cacher = o.Cache.Instance
		} else {
			dir := o.Cache.Dir
			if dir == "" {
				dir = cache.DefaultDir()
			}
			ttl := o.Cache.TTL
			if ttl <= 0 {
				ttl = 24 * time.Hour
			}
			cacher = cache.New(dir, ttl, o.Cache.MaxSize)
		}
	}

	if cacher != nil {
		profile := o.Profile
		if profile == "" {
			profile = "chrome"
		}
		ck := o.CacheKey()
		if body, meta, ok := cacher.Get(rawURL, profile, string(o.Format), o.Session, ck); ok {
			if !o.Security.AllowPrivate && meta.FinalURL != "" && meta.FinalURL != rawURL {
				if err := fetch.CheckSSRF(meta.FinalURL); err != nil {
					slog.Debug("cache hit blocked by SSRF (redirect target)", "url", rawURL, "finalURL", meta.FinalURL)
					cacher = nil
				}
			}
			if cacher != nil {
				slog.Debug("cache hit", "url", rawURL)
				return &Result{
					Doc: &agentdom.Document{
						URL:      rawURL,
						FinalURL: meta.FinalURL,
					},
					Output: string(body),
					Meta: agentdom.Meta{
						FinalURL:   meta.FinalURL,
						StatusCode: meta.StatusCode,
						Protocol:   meta.Protocol,
					},
				}, nil
			}
		}
	}

	if o.Security.Robots && o.Security.RobotsChecker != nil {
		allowed, err := o.Security.RobotsChecker.Check(ctx, "symfetch", rawURL)
		if err != nil {
			slog.Debug("robots check error", "url", rawURL, "error", err)
		} else if !allowed {
			return nil, &BlockedError{URL: rawURL, Reason: "disallowed by robots.txt"}
		}
	}

	resp, err := c.Fetch(ctx, fetch.Request{
		URL:          rawURL,
		Method:       o.Request.Method,
		Headers:      o.Request.Headers,
		Body:         o.Request.Body,
		AllowPrivate: o.Security.AllowPrivate,
		Session:      o.Session,
	})
	if err != nil {
		return nil, &FetchError{URL: rawURL, Err: err}
	}

	// Strip Wayback Machine toolbar if fetched from archive.
	if isWaybackURL(rawURL) {
		resp.Body = []byte(archive.StripWaybackToolbar(string(resp.Body)))
	}

	if resp.StatusCode >= 400 {
		fe := &FetchError{URL: rawURL, Err: fmt.Errorf("HTTP %d", resp.StatusCode), StatusCode: resp.StatusCode}

		// Try Wayback fallback for 404/410 before ancestor probing.
		if o.WaybackFallback && !isWaybackURL(rawURL) && (resp.StatusCode == 404 || resp.StatusCode == 410) {
			waybackURL := archive.RewriteURL(rawURL, o.WaybackTimestamp)
			if fbResult, _, ok := tryFetchAndProcess(ctx, c, eng, waybackURL, rawURL, o); ok {
				slog.Debug("wayback fallback succeeded (4xx)", "original", rawURL, "wayback", waybackURL, "chars", fbResult.Meta.CharCount)
				return fbResult, nil
			}
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			fe.Recovery = probeAncestors(ctx, c, rawURL, o)
		}
		return nil, fe
	}

	tree, err := eng.Materialize(ctx, resp)
	if err != nil {
		return nil, &ParseError{URL: rawURL, Err: err}
	}

	// 4. Extract data islands BEFORE filtering (islands are in <script> tags)
	rawIslands := semantic.ExtractIslands(tree.Root, o.Content.MaxIslandBytes)

	spaSkeleton := DetectSPASkeleton(resp.Body, tree.Root, rawIslands)

	dom.Filter(tree.Root)

	var bestNode *html.Node
	if o.CSSSelector != "" {
		bestNode = extractBySelector(tree.Root, o.CSSSelector)
		if bestNode == nil {
			return nil, &SelectorError{Selector: o.CSSSelector}
		}
	} else {
		bestNode = semantic.BestBlock(tree.Root, o.Content.CharThreshold)
	}
	doc := &agentdom.Document{
		URL:      rawURL,
		FinalURL: resp.FinalURL,
		Title:    tree.Title,
		Lang:     tree.Lang,
	}

	builder := agentdom.NewBuilder(o.Content.MaxChars)
	builder.Build(bestNode, doc)

	// Convert islands
	for _, island := range rawIslands {
		doc.Islands = append(doc.Islands, agentdom.DataIsland{
			Source: island.Source,
			JSON:   island.JSON,
		})
	}

	var output string
	switch o.Format {
	case FormatJSON:
		output, err = render.JSON(doc)
		if err != nil {
			return nil, &RenderError{Format: "json", Err: err}
		}
	case FormatText:
		output = render.Text(doc)
	case FormatHTML:
		output = rawHTMLFallback(resp.Body)
	default:
		if o.SchemaPath != "" {
			result, queryErr := render.QuerySchema(doc.Islands, o.SchemaPath)
			if queryErr != nil {
				var miss *render.SchemaMiss
				if errors.As(queryErr, &miss) {
					slog.Warn("schema query miss", "path", o.SchemaPath, "detail", miss.Msg)
					output = ""
				} else {
					return nil, &SchemaError{Path: o.SchemaPath, Err: queryErr.Error()}
				}
			} else {
				output = result
			}
		} else {
			output, err = render.Markdown(doc, bestNode, o.Content.IncludeLinks)
			if err != nil {
				return nil, &RenderError{Format: "markdown", Err: err}
			}
		}
	}

	if o.Query != "" {
		output = applyRelevanceFilter(output, o.Format, o.Query, o.TopK, doc)
	}

	if o.StoreFullText {
		output = render.StripBase64Images(output)
		storeDir := o.StoreDir
		if storeDir == "" {
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				storeDir = filepath.Join(os.TempDir(), "symfetch", "fulltext")
			} else {
				storeDir = filepath.Join(home, ".cache", "symfetch", "fulltext")
			}
		}
		storeResult, stored, storeErr := TruncateAndStore(output, StoreOptions{
			CharLimit: o.CharLimit,
			StoreDir:  storeDir,
			HeadRatio: 0.8,
			TailRatio: 0.2,
			MaxStored: DefaultMaxStored,
		})
		if storeErr != nil {
			slog.Debug("truncate-and-store failed", "url", rawURL, "error", storeErr)
		} else if stored {
			output = storeResult
		}
	}

	detectedThin := isThinContent(bestNode, o.Content.CharThreshold, spaSkeleton)
	if detectedThin && !o.DisableFallback {
		if fbResult, fbResp, ok := tryFallback(ctx, c, eng, rawURL, o); ok {
			fbCharCount := utf8.RuneCountInString(fbResult.Output)
			if fbCharCount >= o.Content.CharThreshold {
				slog.Debug("thin-content fallback applied", "url", rawURL, "chars", fbCharCount)

				// Cache under original request key per acceptance criteria.
				if cacher != nil {
					profile := o.Profile
					if profile == "" {
						profile = "chrome"
					}
					ck := o.CacheKey()
					if err := cacher.Put(rawURL, profile, string(o.Format), o.Session, ck, []byte(fbResult.Output), cache.Meta{
						URL:         rawURL,
						FinalURL:    fbResult.Meta.FinalURL,
						StatusCode:  fbResult.Meta.StatusCode,
						ContentType: fbResp.ContentType,
						Protocol:    fbResult.Meta.Protocol,
						Headers:     fbResp.Headers,
					}); err != nil {
						slog.Debug("cache put failed (fallback)", "url", rawURL, "error", err)
					}
				}

				return fbResult, nil
			}
		}
	}

	charCount := utf8.RuneCountInString(output)
	truncated := charCount >= o.Content.MaxChars

	meta := agentdom.Meta{
		FinalURL:             resp.FinalURL,
		StatusCode:           resp.StatusCode,
		Title:                tree.Title,
		Lang:                 tree.Lang,
		CharCount:            charCount,
		EstTokens:            charCount / 4,
		Truncated:            truncated,
		Protocol:             resp.Protocol,
		LikelyClientRendered: detectedThin,
	}

	// Non-blocking escalation hint: when the page is a client-rendered shell
	// (SPA skeleton, thin content, or JS challenge), suggest symbrowse as the
	// JS-capable re-fetch tool. This is purely advisory — the result below is
	// still returned normally and browse is never invoked from here. Only set
	// on the final result: if a fallback succeeded above we returned early
	// with the (non-thin) fallback result, so no hint applies there.
	if spaSkeleton {
		meta.Escalate = &agentdom.EscalationHint{
			Tool:    "symbrowse",
			Reason:  "spa_skeleton",
			Command: "symbrowse " + rawURL,
		}
	} else if detectedThin {
		// JS-challenge pages surface here too: they render thin/link-heavy
		// content with no separate detector.
		meta.Escalate = &agentdom.EscalationHint{
			Tool:    "symbrowse",
			Reason:  "thin_content",
			Command: "symbrowse " + rawURL,
		}
	}

	if cacher != nil {
		profile := o.Profile
		if profile == "" {
			profile = "chrome"
		}
		ck := o.CacheKey()
		if err := cacher.Put(rawURL, profile, string(o.Format), o.Session, ck, []byte(output), cache.Meta{
			URL:         rawURL,
			FinalURL:    resp.FinalURL,
			StatusCode:  resp.StatusCode,
			ContentType: resp.ContentType,
			Protocol:    resp.Protocol,
			Headers:     resp.Headers,
		}); err != nil {
			slog.Debug("cache put failed", "url", rawURL, "error", err)
		}
	}

	return &Result{Doc: doc, Output: output, Meta: meta}, nil
}

// RunRaw fetches a URL and returns the raw decoded body without any pipeline processing.
func RunRaw(ctx context.Context, c fetch.Client, rawURL string, req fetch.Request) (*fetch.Response, error) {
	req.URL = rawURL
	return c.Fetch(ctx, req)
}

func rawHTMLFallback(body []byte) string {
	return string(body)
}

// IslandSummary renders a short summary of data islands for Markdown mode.
func IslandSummary(islands []agentdom.DataIsland) string {
	if len(islands) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, island := range islands {
		var preview interface{}
		if err := json.Unmarshal(island.JSON, &preview); err == nil {
			if m, ok := preview.(map[string]interface{}); ok {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sb.WriteString(fmt.Sprintf("- **%s**: keys=%v\n", island.Source, keys))
				continue
			}
		}
		sb.WriteString(fmt.Sprintf("- **%s**: (raw JSON, %d bytes)\n", island.Source, len(island.JSON)))
	}
	return sb.String()
}

func extractBySelector(root *html.Node, selector string) *html.Node {
	doc := goquery.NewDocumentFromNode(root)
	sel := doc.Find(selector)
	if sel.Length() == 0 {
		return nil
	}

	container := &html.Node{
		Type: html.ElementNode,
		Data: "div",
	}
	sel.Each(func(_ int, s *goquery.Selection) {
		for _, n := range s.Clone().Nodes {
			container.AppendChild(n)
		}
	})
	return container
}

func probeAncestors(ctx context.Context, c fetch.Client, rawURL string, o Options) *RecoveryHints {
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

		if o.Security.Robots && o.Security.RobotsChecker != nil {
			allowed, err := o.Security.RobotsChecker.Check(ctx, "symfetch", ancestorStr)
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

	if o.Security.Robots && o.Security.RobotsChecker != nil {
		allowed, err := o.Security.RobotsChecker.Check(ctx, "symfetch", rootStr)
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
		resolved := base.ResolveReference(&url.URL{Path: href})
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

func levenshtein(a, b string) int {
	la := len(a)
	lb := len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
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
	sitemapURLs, err := o.Security.RobotsChecker.Sitemaps(ctx, "symfetch", u.String())
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
		if o.Security.Robots {
			allowed, err := o.Security.RobotsChecker.Check(ctx, "symfetch", smURL)
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

func applyRelevanceFilter(output string, format Format, query string, topK int, doc *agentdom.Document) string {
	if query == "" {
		return output
	}

	switch format {
	case FormatMarkdown:
		sections := relevance.SplitMarkdownSections(output)
		totalSections := len(sections)
		ranked := relevance.RankSections(query, sections, topK)
		return relevance.ReassembleMarkdown(ranked, totalSections, len(ranked))
	case FormatJSON:
		if doc == nil {
			return output
		}
		filtered := relevance.FilterJSONContent(query, doc.Content, func(el agentdom.Element) string {
			return el.Text
		}, topK)
		doc.Content = filtered
		data, err := render.JSON(doc)
		if err != nil {
			slog.Debug("relevance JSON re-render failed", "error", err)
			return output
		}
		return data
	default:
		return output
	}
}
