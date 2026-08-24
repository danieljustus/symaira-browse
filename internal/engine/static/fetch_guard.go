package static

import (
	"time"

	"github.com/danieljustus/symaira-browse/internal/fetch/cache"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
	"github.com/danieljustus/symaira-browse/internal/fetch/robots"
)

// defaultUserAgent is the User-Agent used for robots.txt checks and fetches.
const defaultUserAgent = "symbrowse-static/1.0"

// GuardOptions configures the fetch-hardening and pipeline capabilities applied by the static engine.
type GuardOptions struct {
	// SSRFEnabled guards against RFC1918/loopback fetches. Browse's default
	// is true for static navigation; the daemon can disable it for explicit
	// local-network use (mirrors the Chrome engine's SSRFEnabled).
	SSRFEnabled bool
	// AllowPrivate permits RFC1918/loopback targets when SSRFEnabled is
	// false or when explicitly relaxed via --allow-private.
	AllowPrivate bool
	// RobotsEnabled checks robots.txt before fetching (fetch-absorbed
	// capability, repo consolidation step 5).
	RobotsEnabled bool
	// UserAgent used for the robots check and, when no impersonation
	// profile is configured, for the fetch itself.
	UserAgent string
	// RobotsChecker is an optional custom checker for robots.txt.
	RobotsChecker *robots.Checker
	// Client is an optional custom fetch.Client (e.g. for testing).
	Client fetch.Client

	// Cache options
	NoCache       bool
	CacheDir      string
	CacheTTL      time.Duration
	CacheMaxSize  int64
	CacheInstance *cache.Cache

	// Content and pipeline capabilities
	MaxChars         int
	IncludeLinks     bool
	CSSSelector      string
	Frontmatter      bool
	StoreFullText    bool
	CharLimit        int
	StoreDir         string
	WaybackFallback  bool
	WaybackTimestamp string
}

func (g GuardOptions) userAgent() string {
	if g.UserAgent != "" {
		return g.UserAgent
	}
	return defaultUserAgent
}

// pipelineOptions converts the static engine's guard and capability options
// into pipeline.Options for pipeline.Run execution.
func (g GuardOptions) pipelineOptions() pipeline.Options {
	allowPrivate := g.AllowPrivate || !g.SSRFEnabled

	checker := g.RobotsChecker
	if g.RobotsEnabled && checker == nil {
		checker = robots.NewChecker().WithPrivate(allowPrivate)
	}

	maxChars := g.MaxChars
	if maxChars <= 0 {
		maxChars = 20000
	}

	headers := make(map[string]string)
	headers["User-Agent"] = g.userAgent()

	return pipeline.Options{
		Format: pipeline.FormatMarkdown,
		Content: pipeline.ContentOptions{
			MaxChars:     maxChars,
			IncludeLinks: g.IncludeLinks,
		},
		Cache: pipeline.CacheOptions{
			// A cache hit returns early without calling Materialize, so the
			// engine would keep a nil DOM and every subsequent inspection
			// would fail with "no page loaded". The static engine must always
			// materialize its own document, therefore the pipeline response
			// cache stays off for engine navigation regardless of options.
			NoCache:  true,
			Dir:      g.CacheDir,
			TTL:      g.CacheTTL,
			MaxSize:  g.CacheMaxSize,
			Instance: g.CacheInstance,
		},
		Security: pipeline.SecurityOptions{
			AllowPrivate:  allowPrivate,
			Robots:        g.RobotsEnabled,
			RobotsChecker: checker,
			UserAgent:     g.userAgent(),
		},
		Request: pipeline.RequestOptions{
			Headers: headers,
		},
		CSSSelector:      g.CSSSelector,
		Frontmatter:      g.Frontmatter,
		StoreFullText:    g.StoreFullText,
		CharLimit:        g.CharLimit,
		StoreDir:         g.StoreDir,
		WaybackFallback:  g.WaybackFallback,
		WaybackTimestamp: g.WaybackTimestamp,
	}
}
