package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
	"github.com/danieljustus/symaira-browse/internal/fetch/archive"
	"github.com/danieljustus/symaira-browse/internal/fetch/cache"
	"github.com/danieljustus/symaira-browse/internal/fetch/dom"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/render"
	"github.com/danieljustus/symaira-browse/internal/fetch/semantic"
)

type processedPage struct {
	resp        *fetch.Response
	tree        *dom.Tree
	doc         *agentdom.Document
	bestNode    *html.Node
	output      string
	spaSkeleton bool
	// docTruncated records that the document-level character budget cut the
	// content short, so the response can report it truthfully.
	docTruncated bool
	thin         bool
}

func newCache(o Options) *cache.Cache {
	if o.Cache.NoCache {
		return nil
	}
	if o.Cache.Instance != nil {
		return o.Cache.Instance
	}
	dir := o.Cache.Dir
	if dir == "" {
		dir = cache.DefaultDir()
	}
	ttl := o.Cache.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return cache.New(dir, ttl, o.Cache.MaxSize)
}

func loadCachedResult(rawURL string, o Options, cacher *cache.Cache) (*Result, *cache.Cache) {
	if cacher == nil {
		return nil, nil
	}
	profile := o.Profile
	if profile == "" {
		profile = "chrome"
	}
	body, meta, ok := cacher.Get(rawURL, profile, string(o.Format), o.Session, o.CacheKey())
	if !ok {
		return nil, cacher
	}
	if !o.Security.AllowPrivate && meta.FinalURL != "" && meta.FinalURL != rawURL {
		if err := fetch.CheckSSRF(meta.FinalURL); err != nil {
			slog.Debug("cache hit blocked by SSRF (redirect target)", "url", rawURL, "finalURL", meta.FinalURL)
			return nil, nil
		}
	}
	slog.Debug("cache hit", "url", rawURL)
	return &Result{
		Doc:    &agentdom.Document{URL: rawURL, FinalURL: meta.FinalURL},
		Output: string(body),
		Meta: agentdom.Meta{
			FinalURL: meta.FinalURL, StatusCode: meta.StatusCode, Protocol: meta.Protocol,
		},
	}, cacher
}

func checkRobots(ctx context.Context, rawURL string, o Options) error {
	if !o.Security.Robots || o.Security.RobotsChecker == nil {
		return nil
	}
	ua := o.Security.UserAgent
	if ua == "" {
		ua = "symfetch"
	}
	allowed, err := o.Security.RobotsChecker.Check(ctx, ua, rawURL)
	if err != nil {
		slog.Debug("robots check error", "url", rawURL, "error", err)
		return nil
	}
	if !allowed {
		return &BlockedError{URL: rawURL, Reason: "disallowed by robots.txt"}
	}
	return nil
}

func fetchInitial(ctx context.Context, c fetch.Client, rawURL string, o Options) (*fetch.Response, error) {
	resp, err := c.Fetch(ctx, fetch.Request{
		URL: rawURL, Method: o.Request.Method, Headers: o.Request.Headers,
		Body: o.Request.Body, AllowPrivate: o.Security.AllowPrivate, Session: o.Session,
	})
	if err != nil {
		return nil, &FetchError{URL: rawURL, Err: err}
	}
	if isWaybackURL(rawURL) {
		resp.Body = []byte(archive.StripWaybackToolbar(string(resp.Body)))
	}
	return resp, nil
}

func handleHTTPError(ctx context.Context, c fetch.Client, eng Engine, rawURL string, resp *fetch.Response, o Options) (*Result, error) {
	fe := &FetchError{URL: rawURL, Err: fmt.Errorf("HTTP %d", resp.StatusCode), StatusCode: resp.StatusCode}
	if o.WaybackFallback && !isWaybackURL(rawURL) && (resp.StatusCode == 404 || resp.StatusCode == 410) {
		waybackURL := archive.RewriteURL(rawURL, o.WaybackTimestamp)
		if fbResult, _, ok := tryFetchAndProcess(ctx, c, eng, waybackURL, rawURL, o); ok {
			slog.Debug("wayback fallback succeeded (4xx)", "original", rawURL, "wayback", waybackURL, "chars", fbResult.Meta.CharCount)
			return fbResult, nil
		}
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		fe.Recovery = Probe(ctx, c, rawURL, o)
	}
	return nil, fe
}

func processPage(ctx context.Context, eng Engine, rawURL string, resp *fetch.Response, o Options) (*processedPage, error) {
	tree, err := eng.Materialize(ctx, resp)
	if err != nil {
		return nil, &ParseError{URL: rawURL, Err: err}
	}
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
	doc := &agentdom.Document{URL: rawURL, FinalURL: resp.FinalURL, Title: tree.Title, Lang: tree.Lang}
	builder := agentdom.NewBuilder(o.Content.MaxChars)
	builder.Build(bestNode, doc)
	for _, island := range rawIslands {
		doc.Islands = append(doc.Islands, agentdom.DataIsland{Source: island.Source, JSON: island.JSON})
	}

	output, err := renderPage(doc, bestNode, resp.Body, o)
	if err != nil {
		return nil, err
	}
	if o.Query != "" {
		output = applyRelevanceFilter(output, o.Format, o.Query, o.TopK, doc)
	}
	if o.StoreFullText {
		output = storeLongOutput(output, rawURL, o)
	}
	return &processedPage{
		resp: resp, tree: tree, doc: doc, bestNode: bestNode, output: output,
		spaSkeleton:  spaSkeleton,
		docTruncated: builder.Truncated(),
		thin:         isThinContent(bestNode, o.Content.CharThreshold, spaSkeleton),
	}, nil
}

func renderPage(doc *agentdom.Document, bestNode *html.Node, body []byte, o Options) (string, error) {
	switch o.Format {
	case FormatJSON:
		output, err := render.JSON(doc)
		if err != nil {
			return "", &RenderError{Format: "json", Err: err}
		}
		return output, nil
	case FormatText:
		return render.Text(doc), nil
	case FormatHTML:
		return rawHTMLFallback(body), nil
	default:
		if o.SchemaPath != "" {
			result, queryErr := render.QuerySchema(doc.Islands, o.SchemaPath)
			if queryErr != nil {
				var miss *render.SchemaMiss
				if errors.As(queryErr, &miss) {
					slog.Warn("schema query miss", "path", o.SchemaPath, "detail", miss.Msg)
					return "", nil
				}
				return "", &SchemaError{Path: o.SchemaPath, Err: queryErr.Error()}
			}
			return result, nil
		}
		output, err := render.Markdown(doc, bestNode, o.Content.IncludeLinks)
		if err != nil {
			return "", &RenderError{Format: "markdown", Err: err}
		}
		return output, nil
	}
}

// EscalationCommandPrefix is the CLI invocation an escalation hint suggests,
// up to the URL. It is exported so the command-line package can assert the
// suggested command actually resolves to a registered command — a hint that
// does not run is worse than no hint.
const EscalationCommandPrefix = "symbrowse read "

// EscalationMCPTool is the MCP tool an escalation hint names, the equivalent
// of EscalationCommandPrefix for clients driving the MCP surface.
const EscalationMCPTool = "read"

// escalationHint builds the tier-0 -> tier-1 hint for a page whose real
// content a plain HTTP fetch could not retrieve. Command is the CLI
// invocation and MCPTool the equivalent MCP tool, so a client on either
// surface can act on it (docs/tiers.md).
func escalationHint(reason, rawURL string) *agentdom.EscalationHint {
	return &agentdom.EscalationHint{
		Tool:    "symbrowse",
		MCPTool: EscalationMCPTool,
		Reason:  reason,
		Command: EscalationCommandPrefix + rawURL,
	}
}

// truncationMarker terminates a body cut short by the character budget, so a
// reader can tell a partial page from a complete one even without the
// metadata.
const truncationMarker = "\n\n… [truncated: character budget reached]"

// HasMetaHeader reports whether a markdown response carries the metadata
// header. The header is emitted only when there is something the caller has
// to act on — an escalation hint, a truncated body, or a page the pipeline
// believes is client-rendered — so a page a plain fetch retrieved in full
// renders without it.
func HasMetaHeader(meta agentdom.Meta) bool {
	return meta.Escalate != nil || meta.Truncated || meta.LikelyClientRendered
}

// composeWithinBudget builds the final payload for the requested format and
// keeps it inside the character budget. For markdown the metadata header is
// part of what the caller receives, so it is composed here and counts against
// the budget rather than being bolted on afterwards.
func composeWithinBudget(page *processedPage, o Options, meta agentdom.Meta) (string, agentdom.Meta) {
	body := page.output
	maxChars := o.Content.MaxChars

	// JSON and raw HTML are never cut here: JSON is budgeted at the document
	// level by agentdom.Builder, and slicing a serialized document would hand
	// the caller invalid JSON.
	if o.Format == FormatJSON || o.Format == FormatHTML || maxChars <= 0 {
		meta.Truncated = page.docTruncated
		meta.CharCount = utf8.RuneCountInString(body)
		meta.EstTokens = meta.CharCount / 4
		return body, meta
	}

	budget := maxChars
	if o.Format == FormatMarkdown {
		// Reserve room for the header at its worst case — with the truncation
		// warning and the pre-cap token estimate — so composing it afterwards
		// cannot push the payload past the budget.
		worst := meta
		worst.Truncated = true
		worst.CharCount = utf8.RuneCountInString(body)
		worst.EstTokens = worst.CharCount / 4
		if HasMetaHeader(worst) {
			budget -= utf8.RuneCountInString(render.FormatMarkdownWithMeta(worst, ""))
		}
	}

	body, cut := truncateRunes(body, budget)
	meta.Truncated = cut || page.docTruncated
	meta.CharCount = utf8.RuneCountInString(body)
	meta.EstTokens = meta.CharCount / 4
	if o.Format == FormatMarkdown && HasMetaHeader(meta) {
		body = render.FormatMarkdownWithMeta(meta, body)
	}
	return body, meta
}

// truncateRunes cuts s to at most budget runes, leaving an explicit marker so
// a partial body is never mistaken for a complete one.
func truncateRunes(s string, budget int) (string, bool) {
	if budget < 0 {
		budget = 0
	}
	if utf8.RuneCountInString(s) <= budget {
		return s, false
	}
	room := budget - utf8.RuneCountInString(truncationMarker)
	if room < 0 {
		room = 0
	}
	cut := 0
	for count := 0; count < room && cut < len(s); count++ {
		_, size := utf8.DecodeRuneInString(s[cut:])
		cut += size
	}
	return strings.TrimRight(s[:cut], " \n\t") + truncationMarker, true
}

func storeLongOutput(output, rawURL string, o Options) string {
	output = render.StripBase64Images(output)
	storeDir := o.StoreDir
	if storeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			storeDir = filepath.Join(os.TempDir(), "symfetch", "fulltext")
		} else {
			storeDir = filepath.Join(home, ".cache", "symfetch", "fulltext")
		}
	}
	storedOutput, stored, err := TruncateAndStore(output, StoreOptions{
		CharLimit: o.CharLimit, StoreDir: storeDir, HeadRatio: 0.8, TailRatio: 0.2, MaxStored: DefaultMaxStored,
	})
	if err != nil {
		slog.Debug("truncate-and-store failed", "url", rawURL, "error", err)
		return output
	}
	if stored {
		return storedOutput
	}
	return output
}

func tryThinFallback(ctx context.Context, c fetch.Client, eng Engine, rawURL string, o Options, cacher *cache.Cache) (*Result, bool) {
	fbResult, fbResp, ok := tryFallback(ctx, c, eng, rawURL, o)
	if !ok || utf8.RuneCountInString(fbResult.Output) < o.Content.CharThreshold {
		return nil, false
	}
	slog.Debug("thin-content fallback applied", "url", rawURL, "chars", utf8.RuneCountInString(fbResult.Output))
	if cacher != nil {
		cacheResult(cacher, rawURL, o, fbResult.Output, cache.Meta{
			URL: rawURL, FinalURL: fbResult.Meta.FinalURL, StatusCode: fbResult.Meta.StatusCode,
			ContentType: fbResp.ContentType, Protocol: fbResult.Meta.Protocol, Headers: fbResp.Headers,
		}, "cache put failed (fallback)")
	}
	return fbResult, true
}

func finalizePage(page *processedPage, rawURL string, o Options, cacher *cache.Cache) *Result {
	meta := agentdom.Meta{
		FinalURL: page.resp.FinalURL, StatusCode: page.resp.StatusCode, Title: page.tree.Title,
		Lang: page.tree.Lang, Protocol: page.resp.Protocol,
		LikelyClientRendered: page.thin,
	}
	if page.spaSkeleton {
		meta.Escalate = escalationHint("spa_skeleton", rawURL)
	} else if page.thin {
		meta.Escalate = escalationHint("thin_content", rawURL)
	}
	// The JSON contract carries the hint on the document; every other format
	// reads it from Meta. Both point at the same hint (docs/tiers.md).
	if page.doc != nil {
		page.doc.Escalate = meta.Escalate
	}

	// Enforce the character budget on the rendered text. The markdown
	// renderer converts the source DOM node directly, so it never passed
	// through the document-level budget agentdom.Builder applies; without
	// this cap max_chars would stay advisory for exactly the format most
	// callers use.
	page.output, meta = composeWithinBudget(page, o, meta)
	result := &Result{Doc: page.doc, Output: page.output, Meta: meta}
	if cacher != nil {
		cacheResult(cacher, rawURL, o, page.output, cache.Meta{
			URL: rawURL, FinalURL: page.resp.FinalURL, StatusCode: page.resp.StatusCode,
			ContentType: page.resp.ContentType, Protocol: page.resp.Protocol, Headers: page.resp.Headers,
		}, "cache put failed")
	}
	return result
}

func cacheResult(cacher *cache.Cache, rawURL string, o Options, output string, meta cache.Meta, failureMessage string) {
	profile := o.Profile
	if profile == "" {
		profile = "chrome"
	}
	if err := cacher.Put(rawURL, profile, string(o.Format), o.Session, o.CacheKey(), []byte(output), meta); err != nil {
		slog.Debug(failureMessage, "url", rawURL, "error", err)
	}
}
