package fetch

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// Profile controls TLS/HTTP fingerprint impersonation behavior.
type Profile string

const (
	ProfileChrome  Profile = "chrome"
	ProfileFirefox Profile = "firefox"
	ProfileOpera   Profile = "opera"
	ProfileSafari  Profile = "safari"
	ProfileEdge    Profile = "edge"
	ProfileIos     Profile = "ios"
	ProfileHonest  Profile = "honest"
	ProfileRandom  Profile = "random"
)

// verifiedPresets lists all azuretls presets that have been verified to work
// against real fetch targets. Missing presets are excluded with reasons
// recorded in the issue tracking.
var verifiedPresets = []Profile{
	ProfileChrome,
	ProfileFirefox,
	ProfileOpera,
	ProfileSafari,
	ProfileEdge,
	ProfileIos,
}

// isVerifiedPreset returns true if p is a verified azuretls preset.
func isVerifiedPreset(p Profile) bool {
	for _, vp := range verifiedPresets {
		if p == vp {
			return true
		}
	}
	return false
}

// ParseProfile parses a string into a Profile. Returns ProfileChrome as default.
func ParseProfile(s string) Profile {
	switch s {
	case "firefox":
		return ProfileFirefox
	case "opera":
		return ProfileOpera
	case "safari":
		return ProfileSafari
	case "edge":
		return ProfileEdge
	case "ios":
		return ProfileIos
	case "honest":
		return ProfileHonest
	case "random":
		return ProfileRandom
	default:
		if s != "" && s != "chrome" {
			slog.Warn("unknown profile, defaulting to chrome", "profile", s)
		}
		return ProfileChrome
	}
}

// randomProfile selects a random verified azuretls preset.
func randomProfile() Profile {
	return verifiedPresets[rand.Intn(len(verifiedPresets))]
}

// Request describes a single HTTP fetch operation.
type Request struct {
	URL     string
	Method  string            // defaults to GET
	Headers map[string]string // additional/override headers
	Body    []byte

	Timeout      time.Duration     // 0 = use client default (30s)
	Proxy        string            // http(s)://, socks5://; overrides client-level proxy
	Session      string            // named cookie jar; "" = ephemeral
	MaxBody      int64             // max body bytes; 0 = use client default (10 MiB)
	AllowPrivate bool              // allow RFC1918/loopback targets (SSRF override)
	Allowlist    *policy.Allowlist // optional domain allowlist for this request
}

// Response is the result of a fetch.
type Response struct {
	FinalURL    string
	StatusCode  int
	Headers     map[string][]string
	Body        []byte // decoded (gzip/br/zstd), UTF-8 normalised
	Protocol    string // "HTTP/1.1", "HTTP/2.0", "HTTP/3.0"
	ContentType string
	Elapsed     time.Duration
	FromCache   bool
}

// Client is the core fetch abstraction. Implementations may use
// browser-impersonating transports (AzureTLS) or plain net/http (honest).
type Client interface {
	Fetch(ctx context.Context, req Request) (*Response, error)
	Close() error
}

// Option configures a Client at construction time.
type Option func(*clientOptions)

type clientOptions struct {
	proxy          string
	timeoutSeconds int
	maxBodyMB      int
	enableRetry    bool
	backoffConfig  BackoffConfig
	rateLimiter    *HostRateLimiter
	clock          clock
}

// clock isolates timer behavior so retry tests can advance time without
// waiting on the wall clock.
type clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) After(delay time.Duration) <-chan time.Time {
	return time.After(delay)
}

func withClock(c clock) Option {
	return func(o *clientOptions) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithProxy sets a default proxy for all requests.
func WithProxy(proxy string) Option {
	return func(o *clientOptions) { o.proxy = proxy }
}

// WithTimeout sets the default request timeout in seconds.
func WithTimeout(seconds int) Option {
	return func(o *clientOptions) { o.timeoutSeconds = seconds }
}

// WithMaxBody sets the default maximum response body size in MB.
func WithMaxBody(mb int) Option {
	return func(o *clientOptions) { o.maxBodyMB = mb }
}

// WithRetry enables automatic retry with exponential backoff for transient errors.
func WithRetry(enable bool) Option {
	return func(o *clientOptions) { o.enableRetry = enable }
}

// WithBackoffConfig sets custom backoff configuration.
func WithBackoffConfig(config BackoffConfig) Option {
	return func(o *clientOptions) { o.backoffConfig = config }
}

// WithRateLimiter sets a per-host rate limiter with circuit breaker.
func WithRateLimiter(limiter *HostRateLimiter) Option {
	return func(o *clientOptions) { o.rateLimiter = limiter }
}

// New creates a Client for the given Profile.
func New(p Profile, opts ...Option) (Client, error) {
	o := &clientOptions{
		timeoutSeconds: 30,
		maxBodyMB:      10,
		backoffConfig:  DefaultBackoffConfig(),
		clock:          realClock{},
	}
	for _, opt := range opts {
		opt(o)
	}
	if o.enableRetry && o.rateLimiter == nil {
		o.rateLimiter = NewHostRateLimiter(DefaultCircuitBreakerConfig())
	}
	switch p {
	case ProfileHonest:
		return newHonestClient(o)
	default:
		return newAzureClient(p, o)
	}
}
