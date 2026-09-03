package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/budget"
	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
	"github.com/danieljustus/symaira-browse/internal/fetch/cache"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/relevance"
	"github.com/danieljustus/symaira-browse/internal/fetch/render"
	"github.com/danieljustus/symaira-browse/internal/fetch/robots"
	"github.com/danieljustus/symaira-browse/internal/policy"
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
	Format  Format
	Content ContentOptions
	Cache   CacheOptions
	// StoreCache is the unified output cache used by truncate-and-store.
	StoreCache       *budget.Cache
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
	StoreDir         string // legacy direct-file directory; production uses StoreCache
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
	UserAgent     string
	Allowlist     *policy.Allowlist
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
	// SourceHTML is the fetched page before semantic rendering. It lets
	// daemon-level safety checks inspect hostile markup without exposing it in
	// the response schema.
	SourceHTML []byte `json:"-"`
}

// Run executes the full semantic pipeline:
// fetch → materialize → filter → score → classify → agentdom → render.
func Run(ctx context.Context, c fetch.Client, eng Engine, rawURL string, o Options) (*Result, error) {
	o.setDefaults()
	if err := fetch.CheckAllowlist(rawURL, o.Security.Allowlist); err != nil {
		return nil, err
	}
	if !o.Security.AllowPrivate {
		if err := fetch.CheckSSRF(rawURL); err != nil {
			return nil, err
		}
	}

	cacher := newCache(o)
	if cached, validCacher := loadCachedResult(rawURL, o, cacher); cached != nil {
		return cached, nil
	} else {
		cacher = validCacher
	}

	if err := checkRobots(ctx, rawURL, o); err != nil {
		return nil, err
	}
	resp, err := fetchInitial(ctx, c, rawURL, o)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return handleHTTPError(ctx, c, eng, rawURL, resp, o)
	}

	page, err := processPage(ctx, eng, rawURL, resp, o)
	if err != nil {
		return nil, err
	}
	if page.thin && !o.DisableFallback {
		if fallback, ok := tryThinFallback(ctx, c, eng, rawURL, o, cacher); ok {
			return fallback, nil
		}
	}
	return finalizePage(page, rawURL, o, cacher), nil
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
				_, _ = fmt.Fprintf(&sb, "- **%s**: keys=%v\n", island.Source, keys)
				continue
			}
		}
		_, _ = fmt.Fprintf(&sb, "- **%s**: (raw JSON, %d bytes)\n", island.Source, len(island.JSON))
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
