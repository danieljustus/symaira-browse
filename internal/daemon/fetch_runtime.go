package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/danieljustus/symaira-browse/internal/fetch/archive"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
	"github.com/danieljustus/symaira-browse/internal/fetch/render"
	"github.com/danieljustus/symaira-browse/internal/fetch/robots"
)

// FetchRuntime exposes the absorbed SymFetch fetch pipeline through the
// daemon protocol without requiring a browser session. It serves the
// fetch.url, fetch.batch and wayback.snapshots compatibility frames (issue
// #258): the three SymFetch MCP contracts that Hermes relied on before the
// archived symfetch runtime was retired. All three work on plain HTTP and
// never launch a browser.
type FetchRuntime struct {
	mu           sync.Mutex
	client       fetch.Client
	allowPrivate bool
	robots       bool
	userAgent    string
	cacheDir     string
	cacheTTL     time.Duration
}

// FetchRuntimeOptions configures the fetch runtime.
type FetchRuntimeOptions struct {
	// AllowPrivate relaxes the SSRF guard for plain HTTP fetches
	// (mirrors the daemon --allow-private opt-in).
	AllowPrivate bool
	// Robots enables robots.txt compliance checks before fetching.
	Robots bool
	// UserAgent is used for the robots check and honest fetches.
	UserAgent string
	// CacheDir and CacheTTL configure the response cache (empty disables
	// the shared cache instance; the pipeline falls back to its default).
	CacheDir string
	CacheTTL time.Duration
}

// NewFetchRuntime creates the runtime with an honest (CGO-free) fetch
// client. The honest profile keeps the binary free of the browser-impersonation
// dependency tree while preserving the stable response semantics clients
// rely on.
func NewFetchRuntime(options FetchRuntimeOptions) (*FetchRuntime, error) {
	client, err := fetch.New(fetch.ProfileHonest)
	if err != nil {
		return nil, fmt.Errorf("create fetch client: %w", err)
	}
	if options.UserAgent == "" {
		options.UserAgent = "symbrowse/1.0"
	}
	return &FetchRuntime{
		client:       client,
		allowPrivate: options.AllowPrivate,
		robots:       options.Robots,
		userAgent:    options.UserAgent,
		cacheDir:     options.CacheDir,
		cacheTTL:     options.CacheTTL,
	}, nil
}

// Close releases the underlying fetch client.
func (r *FetchRuntime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return nil
	}
	return r.client.Close()
}

// Handle executes one fetch frame.
func (r *FetchRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	switch frame.Cmd {
	case "fetch.url":
		return r.handleFetchURL(ctx, frame)
	case "fetch.batch":
		return r.handleFetchBatch(ctx, frame)
	case "wayback.snapshots":
		return r.handleWaybackSnapshots(ctx, frame)
	default:
		return nil, nil, NewError(ErrorUnknownCommand, fmt.Sprintf("command %q is not implemented by the fetch runtime", frame.Cmd))
	}
}

// fetchURLArgs is the fetch.url frame payload. Field names match the
// SymFetch fetch_url contract so existing clients keep working (issue #258).
type fetchURLArgs struct {
	URL              string `json:"url"`
	Format           string `json:"format"` // markdown (default), json, text
	MaxChars         int    `json:"max_chars"`
	CharLimit        int    `json:"char_limit"`
	CSSSelector      string `json:"css_selector"`
	Frontmatter      bool   `json:"frontmatter"`
	IncludeLinks     bool   `json:"include_links"`
	Query            string `json:"query"`
	Raw              bool   `json:"raw"`
	SchemaPath       string `json:"schema_path"`
	StoreFullText    bool   `json:"store_full_text"`
	WaybackTimestamp string `json:"wayback_timestamp"`
	WaybackFallback  bool   `json:"wayback_fallback"`
}

func (r *FetchRuntime) handleFetchURL(ctx context.Context, frame Frame) (any, []Warning, error) {
	var args fetchURLArgs
	if err := decodeOptionalArgs(frame, &args); err != nil {
		return nil, nil, err
	}
	if args.URL == "" {
		return nil, nil, NewError(ErrorMalformedRequest, "fetch.url requires a url argument")
	}

	format, err := pipeline.ParseFormat(args.Format)
	if err != nil {
		return nil, nil, NewError(ErrorMalformedRequest, err.Error())
	}

	if args.Raw {
		// The raw contract returns the decoded response body without any
		// pipeline processing (SymFetch fetch_url raw=true).
		resp, err := pipeline.RunRaw(ctx, r.client, args.URL, fetch.Request{
			AllowPrivate: r.allowPrivate,
		})
		if err != nil {
			return nil, nil, NewError(ErrorOperationFailed, err.Error())
		}
		return map[string]any{
			"url":          args.URL,
			"final_url":    resp.FinalURL,
			"status":       resp.StatusCode,
			"content":      string(resp.Body),
			"content_type": resp.ContentType,
		}, nil, nil
	}

	result, err := pipeline.Run(ctx, r.client, pipeline.StaticEngine{}, args.URL, r.pipelineOptions(args.pipelineFields(), format))
	if err != nil {
		return nil, nil, NewError(ErrorOperationFailed, err.Error())
	}

	switch format {
	case pipeline.FormatJSON:
		// Contract-true: the agentdom.Document serializes to exactly the
		// SymFetch fetch_url json schema (url, final_url, title, lang,
		// content, interactive).
		return result.Doc, nil, nil
	case pipeline.FormatText:
		return map[string]any{
			"url":     result.Doc.URL,
			"content": render.Text(result.Doc),
		}, nil, nil
	default:
		return map[string]any{
			"url":      result.Doc.URL,
			"title":    result.Doc.Title,
			"markdown": result.Output,
		}, nil, nil
	}
}

// fetchBatchArgs is the fetch.batch frame payload (SymFetch fetch_batch
// contract: urls up to 20, optional concurrency, per-page format/budget).
type fetchBatchArgs struct {
	URLs          []string `json:"urls"`
	Format        string   `json:"format"`
	MaxChars      int      `json:"max_chars"`
	CharLimit     int      `json:"char_limit"`
	Concurrency   int      `json:"concurrency"`
	StoreFullText bool     `json:"store_full_text"`
	Frontmatter   bool     `json:"frontmatter"`
	IncludeLinks  bool     `json:"include_links"`
}

func (r *FetchRuntime) handleFetchBatch(ctx context.Context, frame Frame) (any, []Warning, error) {
	var args fetchBatchArgs
	if err := decodeOptionalArgs(frame, &args); err != nil {
		return nil, nil, err
	}
	if len(args.URLs) == 0 {
		return nil, nil, NewError(ErrorMalformedRequest, "fetch.batch requires a urls array")
	}
	if len(args.URLs) > 20 {
		return nil, nil, NewError(ErrorMalformedRequest, "fetch.batch supports at most 20 urls")
	}

	format, err := pipeline.ParseFormat(args.Format)
	if err != nil {
		return nil, nil, NewError(ErrorMalformedRequest, err.Error())
	}

	results := make([]any, 0, len(args.URLs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, batchConcurrency(args.Concurrency))

	for _, url := range args.URLs {
		wg.Add(1)
		sem <- struct{}{}
		go func(target string) {
			defer wg.Done()
			defer func() { <-sem }()
			entry := r.fetchBatchEntry(ctx, target, args, format)
			mu.Lock()
			results = append(results, entry)
			mu.Unlock()
		}(url)
	}
	wg.Wait()

	// Preserve input order (SymFetch contract).
	ordered := make([]any, len(args.URLs))
	index := map[string]int{}
	for i, url := range args.URLs {
		index[url] = i
	}
	for _, entry := range results {
		if m, ok := entry.(map[string]any); ok {
			if u, ok := m["url"].(string); ok {
				if i, ok := index[u]; ok {
					ordered[i] = entry
				}
			}
		}
	}
	for i, entry := range ordered {
		if entry == nil {
			ordered[i] = map[string]any{"url": args.URLs[i], "ok": false, "error": "fetch failed"}
		}
	}
	return ordered, nil, nil
}

func (r *FetchRuntime) fetchBatchEntry(ctx context.Context, target string, args fetchBatchArgs, format pipeline.Format) map[string]any {
	result, err := pipeline.Run(ctx, r.client, pipeline.StaticEngine{}, target, r.pipelineOptions(args.pipelineFields(), format))
	if err != nil {
		return map[string]any{"url": target, "ok": false, "error": err.Error()}
	}
	content := result.Output
	if format == pipeline.FormatJSON {
		content = string(mustJSON(result.Doc))
	}
	return map[string]any{"url": target, "ok": true, "content": content}
}

// waybackSnapshotsArgs is the wayback.snapshots frame payload (SymFetch
// wayback_snapshots contract).
type waybackSnapshotsArgs struct {
	URL       string `json:"url"`
	From      string `json:"from"`
	To        string `json:"to"`
	Limit     int    `json:"limit"`
	MatchType string `json:"match_type"`
}

func (r *FetchRuntime) handleWaybackSnapshots(ctx context.Context, frame Frame) (any, []Warning, error) {
	var args waybackSnapshotsArgs
	if err := decodeOptionalArgs(frame, &args); err != nil {
		return nil, nil, err
	}
	if args.URL == "" {
		return nil, nil, NewError(ErrorMalformedRequest, "wayback.snapshots requires a url argument")
	}
	if args.MatchType != "" && args.MatchType != "exact" && args.MatchType != "prefix" && args.MatchType != "host" {
		return nil, nil, NewError(ErrorMalformedRequest, "match_type must be exact, prefix, or host")
	}

	client := archive.NewCDXClient("", nil)
	snapshots, err := client.Lookup(ctx, archive.CDXQuery{
		URL:       args.URL,
		From:      args.From,
		To:        args.To,
		Limit:     args.Limit,
		MatchType: args.MatchType,
	})
	if err != nil {
		return nil, nil, NewError(ErrorOperationFailed, err.Error())
	}

	// Contract-true field mapping (SymFetch wayback_snapshots response):
	// timestamp, url, status, mime_type, digest.
	entries := make([]any, 0, len(snapshots))
	for _, snap := range snapshots {
		entries = append(entries, map[string]any{
			"timestamp": snap.Timestamp,
			"url":       snap.Original,
			"status":    snap.StatusCode,
			"mime_type": snap.MimeType,
			"digest":    snap.Digest,
		})
	}
	return entries, nil, nil
}

func (r *FetchRuntime) pipelineOptions(f pipelineFields, format pipeline.Format) pipeline.Options {
	opts := pipeline.Options{
		Format:           format,
		Frontmatter:      f.Frontmatter,
		CSSSelector:      f.CSSSelector,
		Query:            f.Query,
		SchemaPath:       f.SchemaPath,
		StoreFullText:    f.StoreFullText,
		CharLimit:        f.CharLimit,
		WaybackFallback:  f.WaybackFallback,
		WaybackTimestamp: f.WaybackTimestamp,
		Security: pipeline.SecurityOptions{
			AllowPrivate: r.allowPrivate,
			Robots:       r.robots,
			UserAgent:    r.userAgent,
		},
		Content: pipeline.ContentOptions{
			MaxChars:     f.MaxChars,
			IncludeLinks: f.IncludeLinks,
		},
	}
	if r.robots {
		opts.Security.RobotsChecker = robots.NewChecker().WithPrivate(r.allowPrivate)
	}
	if r.cacheDir != "" {
		opts.Cache.Dir = r.cacheDir
		if r.cacheTTL > 0 {
			opts.Cache.TTL = r.cacheTTL
		}
	}
	return opts
}

// pipelineFields is the shared subset of pipeline options accepted by both
// fetch.url and fetch.batch frames.
type pipelineFields struct {
	MaxChars         int
	CharLimit        int
	CSSSelector      string
	Frontmatter      bool
	IncludeLinks     bool
	Query            string
	SchemaPath       string
	StoreFullText    bool
	WaybackTimestamp string
	WaybackFallback  bool
}

func (a fetchURLArgs) pipelineFields() pipelineFields {
	return pipelineFields{
		MaxChars:         a.MaxChars,
		CharLimit:        a.CharLimit,
		CSSSelector:      a.CSSSelector,
		Frontmatter:      a.Frontmatter,
		IncludeLinks:     a.IncludeLinks,
		Query:            a.Query,
		SchemaPath:       a.SchemaPath,
		StoreFullText:    a.StoreFullText,
		WaybackTimestamp: a.WaybackTimestamp,
		WaybackFallback:  a.WaybackFallback,
	}
}

func (a fetchBatchArgs) pipelineFields() pipelineFields {
	return pipelineFields{
		MaxChars:      a.MaxChars,
		CharLimit:     a.CharLimit,
		Frontmatter:   a.Frontmatter,
		IncludeLinks:  a.IncludeLinks,
		StoreFullText: a.StoreFullText,
	}
}

func batchConcurrency(requested int) int {
	if requested <= 0 {
		return 4
	}
	if requested > 8 {
		return 8
	}
	return requested
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		slog.Debug("marshal fetch batch entry", "error", err)
		return []byte("{}")
	}
	return data
}
