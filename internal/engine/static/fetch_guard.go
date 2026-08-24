package static

import (
	"context"
	"fmt"

	fetchpipeline "github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/robots"
)

// defaultUserAgent is the User-Agent used for robots.txt checks and fetches.
const defaultUserAgent = "symbrowse-static/1.0"

// GuardOptions configures the fetch-hardening applied by the static engine.
type GuardOptions struct {
	// SSRFEnabled guards against RFC1918/loopback fetches. Browse's default
	// is true for static navigation; the daemon can disable it for explicit
	// local-network use (mirrors the Chrome engine's SSRFEnabled).
	SSRFEnabled bool
	// AllowPrivate permits RFC1918/loopback targets when SSRFEnabled is
	// false. Mirrors the Chrome engine option.
	AllowPrivate bool
	// RobotsEnabled checks robots.txt before fetching (fetch-absorbed
	// capability, repo consolidation step 5).
	RobotsEnabled bool
	// UserAgent used for the robots check and, when no impersonation
	// profile is configured, for the fetch itself.
	UserAgent string
}

// checkBeforeFetch applies the fetch-hardening steps that must run before a
// navigation: SSRF guard and (optionally) robots.txt. It returns a
// descriptive error when the target is blocked.
func (g GuardOptions) checkBeforeFetch(ctx context.Context, target string) error {
	if g.SSRFEnabled && !g.AllowPrivate {
		if err := fetchpipeline.CheckSSRF(target); err != nil {
			return fmt.Errorf("static engine: %w", err)
		}
	}

	if g.RobotsEnabled {
		checker := robots.NewChecker().WithPrivate(g.AllowPrivate)
		allowed, err := checker.Check(ctx, g.userAgent(), target)
		if err != nil {
			// A robots.txt fetch failure must not hard-block navigation;
			// treat it as "no policy" (fail-open with a clear log line is
			// handled by the caller; here we degrade to allowed).
			return nil
		}
		if !allowed {
			return fmt.Errorf("static engine: robots.txt disallows %s", target)
		}
	}
	return nil
}

func (g GuardOptions) userAgent() string {
	if g.UserAgent != "" {
		return g.UserAgent
	}
	return defaultUserAgent
}
