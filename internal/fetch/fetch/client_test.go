package fetch

import (
	"testing"
	"time"
)

func TestParseProfile(t *testing.T) {
	tests := []struct {
		input string
		want  Profile
	}{
		{"chrome", ProfileChrome},
		{"firefox", ProfileFirefox},
		{"opera", ProfileOpera},
		{"safari", ProfileSafari},
		{"edge", ProfileEdge},
		{"ios", ProfileIos},
		{"honest", ProfileHonest},
		{"random", ProfileRandom},
		{"", ProfileChrome},
		{"unknown", ProfileChrome},
		{"CHROME", ProfileChrome},
		{"Chrome", ProfileChrome},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseProfile(tt.input)
			if got != tt.want {
				t.Errorf("ParseProfile(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRandomProfile(t *testing.T) {
	// Verify randomProfile always returns a verified preset.
	seen := make(map[Profile]bool)
	for i := 0; i < 50; i++ {
		p := randomProfile()
		if !isVerifiedPreset(p) {
			t.Errorf("randomProfile returned non-verified preset: %q", p)
		}
		seen[p] = true
	}

	// With 50 iterations across 6 presets, we should see more than one
	// distinct value (p(the same) = (1/6)^49 ≈ 10^-38).
	if len(seen) < 2 {
		t.Errorf("randomProfile produced only one distinct preset after 50 calls: %v", seen)
	}
}

func TestNew_HonestClient(t *testing.T) {
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNew_HonestClientWithProxy(t *testing.T) {
	c, err := New(ProfileHonest, WithProxy("http://localhost:9050"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNew_HonestClientWithInvalidProxy(t *testing.T) {
	_, err := New(ProfileHonest, WithProxy("://invalid"))
	if err == nil {
		t.Fatal("expected error for invalid proxy")
	}
}

func TestNew_OptionsApplied(t *testing.T) {
	c, err := New(ProfileHonest,
		WithTimeout(10),
		WithMaxBody(5),
		WithRetry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Type assert to check internal options
	hc, ok := c.(*honestClient)
	if !ok {
		t.Fatal("expected *honestClient")
	}

	if hc.opts.timeoutSeconds != 10 {
		t.Errorf("timeoutSeconds = %d, want 10", hc.opts.timeoutSeconds)
	}
	if hc.opts.maxBodyMB != 5 {
		t.Errorf("maxBodyMB = %d, want 5", hc.opts.maxBodyMB)
	}
	if !hc.opts.enableRetry {
		t.Error("enableRetry should be true")
	}
}

func TestNew_RetryCreatesRateLimiter(t *testing.T) {
	c, err := New(ProfileHonest, WithRetry(true))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	if hc.opts.rateLimiter == nil {
		t.Error("expected rateLimiter to be created when retry is enabled")
	}
}

func TestNew_NoRetryNoRateLimiter(t *testing.T) {
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	if hc.opts.rateLimiter != nil {
		t.Error("expected rateLimiter to be nil when retry is disabled")
	}
}

func TestNew_CustomBackoffConfig(t *testing.T) {
	cfg := BackoffConfig{
		InitialDelay: 1 * time.Second,
		MaxDelay:     60 * time.Second,
		Multiplier:   3.0,
		MaxRetries:   5,
	}

	c, err := New(ProfileHonest,
		WithRetry(true),
		WithBackoffConfig(cfg),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	if hc.opts.backoffConfig.InitialDelay != 1*time.Second {
		t.Errorf("InitialDelay = %v, want 1s", hc.opts.backoffConfig.InitialDelay)
	}
	if hc.opts.backoffConfig.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", hc.opts.backoffConfig.MaxRetries)
	}
}

func TestNew_CustomRateLimiter(t *testing.T) {
	limiter := NewHostRateLimiter(DefaultCircuitBreakerConfig())

	c, err := New(ProfileHonest, WithRateLimiter(limiter))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	if hc.opts.rateLimiter != limiter {
		t.Error("expected custom rate limiter to be used")
	}
}

func TestDefaultOptions(t *testing.T) {
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	if hc.opts.timeoutSeconds != 30 {
		t.Errorf("default timeoutSeconds = %d, want 30", hc.opts.timeoutSeconds)
	}
	if hc.opts.maxBodyMB != 10 {
		t.Errorf("default maxBodyMB = %d, want 10", hc.opts.maxBodyMB)
	}
	if hc.opts.enableRetry {
		t.Error("default enableRetry should be false")
	}
}
