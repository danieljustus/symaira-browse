package fetch

import (
	"context"
	"net/url"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// CheckAllowlist validates a request URL against the configured domain policy.
// A nil or inactive policy preserves the existing unrestricted behavior.
func CheckAllowlist(rawURL string, allowlist *policy.Allowlist) error {
	if allowlist == nil || !allowlist.Active() {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || !allowlist.AllowsURL(u) {
		return &policy.BlockedDomainError{URL: rawURL}
	}
	return nil
}

type allowlistContextKey struct{}

func withAllowlistContext(ctx context.Context, allowlist *policy.Allowlist) context.Context {
	return context.WithValue(ctx, allowlistContextKey{}, allowlist)
}

func allowlistFromContext(ctx context.Context) *policy.Allowlist {
	if ctx == nil {
		return nil
	}
	allowlist, _ := ctx.Value(allowlistContextKey{}).(*policy.Allowlist)
	return allowlist
}
