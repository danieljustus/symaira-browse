package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	thin        bool
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
	agentdom.NewBuilder(o.Content.MaxChars).Build(bestNode, doc)
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
		spaSkeleton: spaSkeleton,
		thin:        isThinContent(bestNode, o.Content.CharThreshold, spaSkeleton),
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
	charCount := utf8.RuneCountInString(page.output)
	meta := agentdom.Meta{
		FinalURL: page.resp.FinalURL, StatusCode: page.resp.StatusCode, Title: page.tree.Title,
		Lang: page.tree.Lang, CharCount: charCount, EstTokens: charCount / 4,
		Truncated: charCount >= o.Content.MaxChars, Protocol: page.resp.Protocol,
		LikelyClientRendered: page.thin,
	}
	if page.spaSkeleton {
		meta.Escalate = &agentdom.EscalationHint{Tool: "symbrowse", Reason: "spa_skeleton", Command: "symbrowse " + rawURL}
	} else if page.thin {
		meta.Escalate = &agentdom.EscalationHint{Tool: "symbrowse", Reason: "thin_content", Command: "symbrowse " + rawURL}
	}
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
