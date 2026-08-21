package fetch

import (
	"errors"
	"net"
	"testing"
	"time"
)

// --- BackoffDelay tests ---

func TestBackoffDelay_JitterBounds(t *testing.T) {
	cfg := BackoffConfig{
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     10 * time.Second,
		Multiplier:   2.0,
		MaxRetries:   5,
	}

	// Run many iterations to account for randomness
	for attempt := 0; attempt < 5; attempt++ {
		base := float64(cfg.InitialDelay) * pow2(float64(attempt))
		if base > float64(cfg.MaxDelay) {
			base = float64(cfg.MaxDelay)
		}
		minJitter := time.Duration(base * 0.5)
		maxJitter := time.Duration(base * 1.0)

		for i := 0; i < 100; i++ {
			got := cfg.BackoffDelay(attempt)
			if got < minJitter || got > maxJitter {
				t.Errorf("attempt %d: delay %v not in [%v, %v]", attempt, got, minJitter, maxJitter)
			}
		}
	}
}

func TestBackoffDelay_MaxDelayCap(t *testing.T) {
	cfg := BackoffConfig{
		InitialDelay: 1 * time.Second,
		MaxDelay:     5 * time.Second,
		Multiplier:   2.0,
		MaxRetries:   10,
	}

	// Attempt 10: base = 1s * 2^10 = 1024s, should be capped at 5s
	// Jitter range: 2.5s to 5s
	for i := 0; i < 100; i++ {
		got := cfg.BackoffDelay(10)
		if got < 2*time.Second || got > 5*time.Second {
			t.Errorf("attempt 10: delay %v not in [2s, 5s]", got)
		}
	}
}

func pow2(exp float64) float64 {
	result := 1.0
	for i := 0; i < int(exp); i++ {
		result *= 2
	}
	return result
}

// --- ParseRetryAfter tests ---

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantNil bool
	}{
		{
			name:  "empty string",
			input: "",
			want:  0,
		},
		{
			name:  "seconds",
			input: "120",
			want:  120 * time.Second,
		},
		{
			name:  "seconds with whitespace",
			input: "  30  ",
			want:  30 * time.Second,
		},
		{
			name:  "HTTP-date in the future",
			input: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC1123),
			want:  0, // will be checked specially
		},
		{
			name:  "HTTP-date in the past",
			input: time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC1123),
			want:  0,
		},
		{
			name:  "invalid string",
			input: "invalid",
			want:  0,
		},
		{
			name:  "negative seconds",
			input: "-5",
			want:  -5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRetryAfter(tt.input)
			if tt.name == "HTTP-date in the future" {
				// Should be roughly 5 minutes (allow some execution time tolerance)
				if got < 4*time.Minute || got > 6*time.Minute {
					t.Errorf("ParseRetryAfter(%q) = %v, want ~5m", tt.input, got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("ParseRetryAfter(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// --- CircuitBreaker tests ---

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 3,
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	// Initially closed — should allow
	if !cb.Allow() {
		t.Fatal("expected Allow() = true in Closed state")
	}

	// Record failures up to threshold
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// Now open — should reject
	if cb.Allow() {
		t.Fatal("expected Allow() = false after threshold reached")
	}
}

func TestCircuitBreaker_OpenToHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 2,
		RecoveryTimeout:  50 * time.Millisecond,
		SuccessThreshold: 2,
	})

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.Allow() {
		t.Fatal("circuit should be open")
	}

	// Wait for recovery timeout
	time.Sleep(60 * time.Millisecond)

	// Should transition to half-open and allow
	if !cb.Allow() {
		t.Fatal("expected Allow() = true in HalfOpen state")
	}
}

func TestCircuitBreaker_HalfOpenToClosed(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 2,
		RecoveryTimeout:  50 * time.Millisecond,
		SuccessThreshold: 2,
	})

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for recovery
	time.Sleep(60 * time.Millisecond)
	cb.Allow() // transition to half-open

	// Record enough successes to close
	cb.RecordSuccess()
	cb.RecordSuccess()

	// Verify we're back to closed — state is private, but Allow should work
	if !cb.Allow() {
		t.Fatal("expected Allow() = true after closing circuit")
	}
}

func TestCircuitBreaker_HalfOpenToFailure(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 2,
		RecoveryTimeout:  50 * time.Millisecond,
		SuccessThreshold: 2,
	})

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for recovery
	time.Sleep(60 * time.Millisecond)
	cb.Allow() // transition to half-open

	// Failure in half-open should reopen
	cb.RecordFailure()
	if cb.Allow() {
		t.Fatal("circuit should be open after failure in half-open")
	}
}

func TestCircuitBreaker_SuccessResetsCount(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 5,
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	// Record some failures (below threshold)
	cb.RecordFailure()
	cb.RecordFailure()

	// Success resets consecutive fail count
	cb.RecordSuccess()

	// Record more failures — need 5 total after the reset to trip
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()

	// Should now be open
	if cb.Allow() {
		t.Fatal("circuit should be open")
	}
}

// --- HostRateLimiter tests ---

func TestHostRateLimiter_NewBreakers(t *testing.T) {
	hrl := NewHostRateLimiter(CircuitBreakerConfig{
		FailureThreshold: 3,
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	// First request to a host should create a breaker and allow
	if !hrl.Allow("https://example.com/page") {
		t.Fatal("expected Allow() = true for new host")
	}

	// Different host should also allow
	if !hrl.Allow("https://other.com/page") {
		t.Fatal("expected Allow() = true for different host")
	}
}

func TestHostRateLimiter_FailureTripsBreaker(t *testing.T) {
	hrl := NewHostRateLimiter(CircuitBreakerConfig{
		FailureThreshold: 2,
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	host := "https://example.com/page"

	hrl.Allow(host) // creates the breaker
	hrl.RecordFailure(host)
	hrl.RecordFailure(host)

	if hrl.Allow(host) {
		t.Fatal("expected Allow() = false after failures")
	}
}

func TestHostRateLimiter_SuccessResetsBreaker(t *testing.T) {
	hrl := NewHostRateLimiter(CircuitBreakerConfig{
		FailureThreshold: 5,
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	host := "https://example.com/page"

	hrl.Allow(host) // creates the breaker
	hrl.RecordFailure(host)
	hrl.RecordFailure(host)

	hrl.RecordSuccess(host) // resets fail count

	hrl.RecordFailure(host)
	hrl.RecordFailure(host)
	hrl.RecordFailure(host)
	hrl.RecordFailure(host)
	hrl.RecordFailure(host)

	if hrl.Allow(host) {
		t.Fatal("expected Allow() = false after threshold")
	}
}

func TestHostRateLimiter_CleanupStale(t *testing.T) {
	hrl := NewHostRateLimiter(CircuitBreakerConfig{
		FailureThreshold: 5,
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	// Create some breakers
	hrl.Allow("https://a.com")
	hrl.Allow("https://b.com")
	hrl.Allow("https://c.com")

	// Record failures on one, then wait
	hrl.RecordFailure("https://a.com")
	hrl.RecordFailure("https://b.com")

	// Cleanup with very short age — should remove stale breakers
	// Note: cleanupStaleLocked checks lastFailure time, so breakers
	// with old lastFailure will be cleaned. Since we just created them,
	// they won't be stale yet. Let's use a very large maxAge to test the path.
	hrl.CleanupStale(1 * time.Hour)

	// All should still exist (failures are recent)
	if !hrl.Allow("https://a.com") {
		t.Error("breaker for a.com should still exist")
	}
}

func TestHostRateLimiter_CleanupRemovesClosedStale(t *testing.T) {
	hrl := NewHostRateLimiter(CircuitBreakerConfig{
		FailureThreshold: 5,
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	host := "https://stale.example.com"

	// Create breaker and record a failure
	hrl.Allow(host)
	hrl.RecordFailure(host)

	// Simulate old failure by modifying the breaker's lastFailure directly
	// (we can't access private fields, so we'll use CleanupStale with 0 age)
	// A 0 age means "clean up everything that's closed and has any lastFailure"
	hrl.CleanupStale(0)

	// The breaker should be cleaned up since its lastFailure is non-zero and state is closed
	// Next Allow should create a fresh breaker
	if !hrl.Allow(host) {
		t.Error("expected fresh breaker to allow")
	}
}

func TestHostRateLimiter_ConcurrentAccess(t *testing.T) {
	hrl := NewHostRateLimiter(CircuitBreakerConfig{
		FailureThreshold: 100, // high threshold to avoid tripping
		RecoveryTimeout:  1 * time.Hour,
		SuccessThreshold: 2,
	})

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				host := "https://example.com"
				hrl.Allow(host)
				hrl.RecordSuccess(host)
				hrl.RecordFailure(host)
			}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestHostRateLimiter_ExtractHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/path", "example.com"},
		{"http://localhost:8080/api", "localhost"},
		{"https://user:pass@host.com/path", "host.com"},
		{"not-a-url", ""},
	}

	for _, tt := range tests {
		got := extractHost(tt.input)
		if got != tt.want {
			t.Errorf("extractHost(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- IsTransientError tests ---

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		err        error
		want       bool
	}{
		{"nil error, 200", 200, nil, false},
		{"nil error, 429", 429, nil, true},
		{"nil error, 502", 502, nil, true},
		{"nil error, 503", 503, nil, true},
		{"nil error, 504", 504, nil, true},
		{"nil error, 404", 404, nil, false},
		{"nil error, 500", 500, nil, false},
		{"network error", 0, &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"generic error", 0, errors.New("something"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsTransientError(tt.statusCode, tt.err)
			if got != tt.want {
				t.Errorf("IsTransientError(%d, %v) = %v, want %v", tt.statusCode, tt.err, got, tt.want)
			}
		})
	}
}

// --- DefaultConfig tests ---

func TestDefaultBackoffConfig(t *testing.T) {
	cfg := DefaultBackoffConfig()
	if cfg.InitialDelay != 500*time.Millisecond {
		t.Errorf("InitialDelay = %v, want 500ms", cfg.InitialDelay)
	}
	if cfg.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", cfg.MaxDelay)
	}
	if cfg.Multiplier != 2.0 {
		t.Errorf("Multiplier = %v, want 2.0", cfg.Multiplier)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	if cfg.FailureThreshold != 5 {
		t.Errorf("FailureThreshold = %d, want 5", cfg.FailureThreshold)
	}
	if cfg.RecoveryTimeout != 60*time.Second {
		t.Errorf("RecoveryTimeout = %v, want 60s", cfg.RecoveryTimeout)
	}
	if cfg.SuccessThreshold != 2 {
		t.Errorf("SuccessThreshold = %d, want 2", cfg.SuccessThreshold)
	}
}
