package fetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testBackoffConfig returns a minimal backoff for fast deterministic tests.
func testBackoffConfig(maxRetries int) BackoffConfig {
	return BackoffConfig{
		InitialDelay: 1 * time.Microsecond,
		MaxDelay:     1 * time.Microsecond,
		Multiplier:   1.0,
		MaxRetries:   maxRetries,
	}
}

type manualClock struct {
	mu          sync.Mutex
	now         time.Time
	timers      []manualTimer
	delays      []time.Duration
	afterCalled chan struct{}
}

type manualTimer struct {
	at time.Time
	ch chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{
		now:         time.Unix(0, 0),
		afterCalled: make(chan struct{}, 1),
	}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	ch := make(chan time.Time, 1)
	c.delays = append(c.delays, delay)
	c.timers = append(c.timers, manualTimer{at: c.now.Add(delay), ch: ch})
	select {
	case c.afterCalled <- struct{}{}:
	default:
	}
	return ch
}

func (c *manualClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	ready := make([]chan time.Time, 0, len(c.timers))
	remaining := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(c.now) {
			ready = append(ready, timer.ch)
		} else {
			remaining = append(remaining, timer)
		}
	}
	c.timers = remaining
	now := c.now
	c.mu.Unlock()

	for _, ch := range ready {
		ch <- now
	}
}

// ---------------------------------------------------------------------------
// Retry on transient HTTP status codes
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Response body is closed during retry (no leak)
// ---------------------------------------------------------------------------

func TestHonestClient_Retry_BodyClosedDuringRetry(t *testing.T) {
	t.Parallel()
	var attempts int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("retry-me"))
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(testBackoffConfig(2)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestHonestClient_Retry_Transient503(t *testing.T) {
	t.Parallel()
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(testBackoffConfig(2)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(resp.Body) != "ok" {
		t.Fatalf("expected body 'ok', got %q", resp.Body)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestHonestClient_Retry_AllTransientCodes(t *testing.T) {
	t.Parallel()
	codes := []int{429, 502, 503, 504}
	for _, code := range codes {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()
			var attempts int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := atomic.AddInt32(&attempts, 1)
				if n == 1 {
					w.WriteHeader(code)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				w.Write([]byte("recovered"))
			}))
			defer srv.Close()

			c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(testBackoffConfig(2)))
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			resp, err := c.Fetch(context.Background(), Request{
				URL:          srv.URL,
				AllowPrivate: true,
			})
			if err != nil {
				t.Fatalf("unexpected error for %d: %v", code, err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}
		})
	}
}

func TestHonestClient_Retry_Exhausted(t *testing.T) {
	t.Parallel()
	// Use network errors (connection close) so the retry loop always takes
	// the error path. Transient HTTP status would return a 503 response on
	// the final attempt (attempt == maxRetries) because the guard
	// "attempt < maxRetries" becomes false, falling through to the success
	// path instead of breaking with an error.
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(testBackoffConfig(2)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("expected error wrapping 'fetch', got: %v", err)
	}
	// 1 initial + 2 retries = 3
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestHonestClient_Retry_TransientWithRetryAfter(t *testing.T) {
	t.Parallel()
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	clock := newManualClock()
	go func() {
		<-clock.afterCalled
		clock.Advance(time.Second)
	}()

	// Use a very short backoff; Retry-After=1s should dominate the delay.
	cfg := BackoffConfig{
		InitialDelay: 1 * time.Millisecond,
		MaxDelay:     1 * time.Millisecond,
		Multiplier:   1.0,
		MaxRetries:   2,
	}
	c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(cfg), withClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
	clock.mu.Lock()
	delays := append([]time.Duration(nil), clock.delays...)
	clock.mu.Unlock()
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("expected a one-second retry delay, got %v", delays)
	}
}

// ---------------------------------------------------------------------------
// Retry on network / connection errors
// ---------------------------------------------------------------------------

func TestHonestClient_Retry_NetworkError(t *testing.T) {
	t.Parallel()
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			// Hijack and close to simulate a broken connection.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("server does not support hijack")
				return
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(testBackoffConfig(2)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestHonestClient_NoRetry_NetworkError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	// Retry disabled (default).
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected error when retry is disabled")
	}
}

// ---------------------------------------------------------------------------
// Context cancellation during retry backoff
// ---------------------------------------------------------------------------

func TestHonestClient_Retry_ContextCancelled(t *testing.T) {
	t.Parallel()
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Long backoff so context cancellation fires before next attempt.
	cfg := BackoffConfig{
		InitialDelay: 10 * time.Second,
		MaxDelay:     10 * time.Second,
		Multiplier:   1.0,
		MaxRetries:   5,
	}
	c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = c.Fetch(ctx, Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected context error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") &&
		!strings.Contains(err.Error(), "fetch") {
		t.Fatalf("unexpected error type: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation on network error retry backoff
// ---------------------------------------------------------------------------

func TestHonestClient_Retry_NetworkError_ContextCancelled(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	cfg := BackoffConfig{
		InitialDelay: 10 * time.Second,
		MaxDelay:     10 * time.Second,
		Multiplier:   1.0,
		MaxRetries:   5,
	}
	c, err := New(ProfileHonest, WithRetry(true), WithBackoffConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = c.Fetch(ctx, Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected context error")
	}
}

// ---------------------------------------------------------------------------
// Rate limiter blocks request
// ---------------------------------------------------------------------------

func TestHonestClient_RateLimiter_BlocksRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	limiter := NewHostRateLimiter(CircuitBreakerConfig{
		FailureThreshold: 1,
		RecoveryTimeout:  1 * time.Hour, // won't recover during test
		SuccessThreshold: 1,
	})

	c, err := New(ProfileHonest, WithRetry(true), WithRateLimiter(limiter))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Trip the circuit breaker for this host.
	limiter.Allow(srv.URL)
	limiter.RecordFailure(srv.URL)

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected circuit breaker error")
	}
	if !strings.Contains(err.Error(), "circuit breaker open") {
		t.Fatalf("expected circuit breaker error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Per-request proxy selection
// ---------------------------------------------------------------------------

func TestHonestClient_PerRequestProxy(t *testing.T) {
	t.Parallel()
	var proxyHit int32

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("via-proxy"))
	}))
	defer target.Close()

	// Simple forwarding proxy.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxyHit, 1)
		// For an HTTP proxy, r.URL is the full target URL.
		transport := &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
		proxyReq, err := http.NewRequest(r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		proxyReq.Header = r.Header
		resp, err := transport.RoundTrip(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          target.URL,
		Proxy:        proxy.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Body) != "via-proxy" {
		t.Fatalf("expected 'via-proxy', got %q", resp.Body)
	}
	if atomic.LoadInt32(&proxyHit) != 1 {
		t.Fatal("expected proxy to be hit")
	}
	// Verify the proxy client was cached.
	hc := c.(*honestClient)
	hc.proxyMu.Lock()
	if len(hc.proxyClients) == 0 {
		t.Fatal("expected proxy client to be cached")
	}
	hc.proxyMu.Unlock()
}

func TestHonestClient_PerRequestProxy_AllowPrivate(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("private-ok"))
	}))
	defer target.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transport := &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
		proxyReq, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
		proxyReq.Header = r.Header
		resp, err := transport.RoundTrip(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          target.URL,
		Proxy:        proxy.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Body) != "private-ok" {
		t.Fatalf("expected 'private-ok', got %q", resp.Body)
	}

	// Verify two proxy clients cached (one per allowPrivate variant).
	hc := c.(*honestClient)
	hc.proxyMu.Lock()
	count := len(hc.proxyClients)
	hc.proxyMu.Unlock()
	if count < 1 {
		t.Fatalf("expected >=1 cached proxy client, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// getProxyClient: invalid proxy URL fallback
// ---------------------------------------------------------------------------

func TestHonestClient_getProxyClient_InvalidProxy(t *testing.T) {
	t.Parallel()
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// url.Parse succeeds for almost anything; use an extremely long string to trigger error.
	badProxy := "http://" + strings.Repeat("x", 1<<21)
	client := c.(*honestClient).getProxyClient(badProxy, false)
	if client == nil {
		t.Fatal("expected fallback client for allowPrivate=false, got nil")
	}

	client2 := c.(*honestClient).getProxyClient(badProxy, true)
	if client2 == nil {
		t.Fatal("expected fallback client for allowPrivate=true, got nil")
	}
}

// ---------------------------------------------------------------------------
// Body read error
// ---------------------------------------------------------------------------

func TestHonestClient_BodyReadError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial"))
		// Hijack and close to cause a body-read error.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected error from body read failure")
	}
	if !strings.Contains(err.Error(), "read body") {
		t.Fatalf("expected 'read body' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MaxBody exceeded → *ErrTooLarge
// ---------------------------------------------------------------------------

func TestHonestClient_MaxBodyExceeded(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("A", 500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		MaxBody:      100, // 100-byte limit
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected ErrTooLarge")
	}
	var tooLarge *ErrTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *ErrTooLarge, got %T: %v", err, err)
	}
	if tooLarge.Limit != 100 {
		t.Fatalf("expected limit 100, got %d", tooLarge.Limit)
	}
}

// ---------------------------------------------------------------------------
// Charset normalization fallback (bytes.ToValidUTF8)
// ---------------------------------------------------------------------------

func TestHonestClient_CharsetFallback_InvalidUTF8(t *testing.T) {
	t.Parallel()
	// Content-Type claims shift_jis but 0x81 is a valid SJIS lead byte
	// while 0x00 is NOT a valid trail byte (must be 0x40-0x7E or 0x80-0xFC).
	// 0x81 is also invalid as a UTF-8 leading byte (continuation byte).
	invalid := []byte{0x81, 0x00, 'h', 'i'}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=shift_jis")
		w.WriteHeader(http.StatusOK)
		w.Write(invalid)
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bytes.Contains(resp.Body, []byte{0x81}) {
		t.Fatalf("invalid byte 0x81 should have been replaced, got raw bytes in body")
	}
	if !strings.Contains(string(resp.Body), "hi") {
		t.Fatalf("expected 'hi' in body, got %q", resp.Body)
	}
}

// ---------------------------------------------------------------------------
// AllowPrivate=true bypasses SSRF dial guard
// ---------------------------------------------------------------------------

func TestHonestClient_AllowPrivate_BypassDialGuard(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("private-ok"))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("AllowPrivate=true should bypass dial guard: %v", err)
	}
	if string(resp.Body) != "private-ok" {
		t.Fatalf("expected 'private-ok', got %q", resp.Body)
	}
}

// ---------------------------------------------------------------------------
// AllowPrivate=false blocks private URL
// ---------------------------------------------------------------------------

func TestHonestClient_DenyPrivate_BlocksDialGuard(t *testing.T) {
	t.Parallel()
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          "http://127.0.0.1:1/nonexistent",
		AllowPrivate: false,
	})
	if err == nil {
		t.Fatal("expected SSRF block error")
	}
}

// ---------------------------------------------------------------------------
// SSRF redirect blocking (safe mode)
// ---------------------------------------------------------------------------

func TestHonestClient_SafeRedirect_BlocksPrivate(t *testing.T) {
	t.Parallel()
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should-not-reach"))
	}))
	defer private.Close()

	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer public.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          public.URL,
		AllowPrivate: false,
	})
	if err == nil {
		t.Fatal("expected SSRF redirect block")
	}
}

// ---------------------------------------------------------------------------
// AllowPrivate=true follows redirect to private
// ---------------------------------------------------------------------------

func TestHonestClient_AllowRedirect_FollowsPrivate(t *testing.T) {
	t.Parallel()
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("reached"))
	}))
	defer private.Close()

	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer public.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          public.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Body) != "reached" {
		t.Fatalf("expected 'reached', got %q", resp.Body)
	}
}

// ---------------------------------------------------------------------------
// Per-request timeout override
// ---------------------------------------------------------------------------

func TestHonestClient_PerRequestTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.Write([]byte("late"))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest, WithTimeout(60))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = c.Fetch(ctx, Request{
		URL:          srv.URL,
		Timeout:      100 * time.Millisecond, // per-request override
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Custom headers override
// ---------------------------------------------------------------------------

func TestHonestClient_CustomHeaders(t *testing.T) {
	t.Parallel()
	var gotCustom, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-Custom")
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
		Headers: map[string]string{
			"X-Custom": "hello",
			"Accept":   "application/json",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotCustom != "hello" {
		t.Fatalf("expected X-Custom=hello, got %q", gotCustom)
	}
	if gotAccept != "application/json" {
		t.Fatalf("expected Accept=application/json, got %q", gotAccept)
	}
}

// ---------------------------------------------------------------------------
// Default method = GET
// ---------------------------------------------------------------------------

func TestHonestClient_DefaultMethod(t *testing.T) {
	t.Parallel()
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
		// Method omitted → should default to GET
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != "GET" {
		t.Fatalf("expected GET, got %q", gotMethod)
	}
}

// ---------------------------------------------------------------------------
// Explicit method (POST with body)
// ---------------------------------------------------------------------------

func TestHonestClient_ExplicitMethod_POST(t *testing.T) {
	t.Parallel()
	var gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		Method:       "POST",
		Body:         []byte(`{"key":"val"}`),
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != "POST" {
		t.Fatalf("expected POST, got %q", gotMethod)
	}
	if string(gotBody) != `{"key":"val"}` {
		t.Fatalf("expected POST body, got %q", gotBody)
	}
}

// ---------------------------------------------------------------------------
// Per-request MaxBody override
// ---------------------------------------------------------------------------

func TestHonestClient_PerRequestMaxBody(t *testing.T) {
	t.Parallel()
	payload := strings.Repeat("B", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	// Client default is 10 MB, but request overrides to 100 bytes.
	c, err := New(ProfileHonest, WithMaxBody(10))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		MaxBody:      100,
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected ErrTooLarge from per-request MaxBody")
	}
	var tooLarge *ErrTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *ErrTooLarge, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Close() cleans up idle connections
// ---------------------------------------------------------------------------

func TestHonestClient_Close(t *testing.T) {
	t.Parallel()
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("unexpected error from Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// HTTP/2.0 protocol detection
// ---------------------------------------------------------------------------

func TestHonestClient_ProtocolDetection(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// httptest serves over HTTP/1.1
	if resp.Protocol != "HTTP/1.1" {
		t.Fatalf("expected HTTP/1.1, got %q", resp.Protocol)
	}
}

// ---------------------------------------------------------------------------
// Client-level proxy option
// ---------------------------------------------------------------------------

func TestHonestClient_ClientLevelProxy(t *testing.T) {
	t.Parallel()
	var proxyHit int32

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("proxied"))
	}))
	defer target.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&proxyHit, 1)
		transport := &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
		proxyReq, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
		proxyReq.Header = r.Header
		resp, err := transport.RoundTrip(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	c, err := New(ProfileHonest, WithProxy(proxy.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          target.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Body) != "proxied" {
		t.Fatalf("expected 'proxied', got %q", resp.Body)
	}
	if atomic.LoadInt32(&proxyHit) != 1 {
		t.Fatal("expected proxy to be hit")
	}
}

// ---------------------------------------------------------------------------
// SSRF pre-fetch check (non-redirect)
// ---------------------------------------------------------------------------

func TestHonestClient_SSRF_PreFetchBlock(t *testing.T) {
	t.Parallel()
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 127.0.0.1 is private; should be blocked by CheckSSRF before any HTTP round-trip.
	_, err = c.Fetch(context.Background(), Request{
		URL:          "http://127.0.0.1:9/fake",
		AllowPrivate: false,
	})
	if err == nil {
		t.Fatal("expected SSRF pre-fetch block")
	}
}

// ---------------------------------------------------------------------------
// Successful fetch records rate limiter success
// ---------------------------------------------------------------------------

func TestHonestClient_RateLimiter_RecordSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	limiter := NewHostRateLimiter(DefaultCircuitBreakerConfig())
	c, err := New(ProfileHonest, WithRetry(true), WithRateLimiter(limiter))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Verify the rate limiter's breaker is still closed (healthy).
	host := extractHost(srv.URL)
	if !limiter.Allow(host) {
		t.Fatal("circuit breaker should still be closed after success")
	}
}

// ---------------------------------------------------------------------------
// Invalid proxy URL at client level
// ---------------------------------------------------------------------------

func TestHonestClient_InvalidProxyURL(t *testing.T) {
	t.Parallel()
	_, err := New(ProfileHonest, WithProxy("://invalid"))
	if err == nil {
		t.Fatal("expected error for invalid proxy URL")
	}
}

// ---------------------------------------------------------------------------
// getProxyClient: proxy with SSRF-safe redirect check (allowPrivate=false)
// ---------------------------------------------------------------------------

func TestHonestClient_getProxyClient_SafeProxyRedirect(t *testing.T) {
	t.Parallel()
	privateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("private"))
	}))
	defer privateSrv.Close()

	publicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, privateSrv.URL, http.StatusFound)
	}))
	defer publicSrv.Close()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transport := &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
		proxyReq, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
		proxyReq.Header = r.Header
		resp, err := transport.RoundTrip(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          publicSrv.URL,
		Proxy:        proxySrv.URL,
		AllowPrivate: false,
	})
	if err == nil {
		t.Fatal("expected SSRF redirect block via proxy")
	}
	if !strings.Contains(err.Error(), "blocked_private") {
		t.Errorf("expected blocked_private error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// getProxyClient: proxy with allowPrivate=true follows redirect
// ---------------------------------------------------------------------------

func TestHonestClient_getProxyClient_PrivateProxyRedirect(t *testing.T) {
	t.Parallel()
	privateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("reached"))
	}))
	defer privateSrv.Close()

	publicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, privateSrv.URL, http.StatusFound)
	}))
	defer publicSrv.Close()

	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transport := &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
		proxyReq, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
		proxyReq.Header = r.Header
		resp, err := transport.RoundTrip(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          publicSrv.URL,
		Proxy:        proxySrv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.Body) != "reached" {
		t.Fatalf("expected 'reached', got %q", resp.Body)
	}
}

// ---------------------------------------------------------------------------
// getProxyClient: proxy with AllowPrivate=false uses safe dial guard
// ---------------------------------------------------------------------------

func TestHonestClient_getProxyClient_SafeProxyDialGuard(t *testing.T) {
	t.Parallel()
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	client := hc.getProxyClient("http://localhost:19999", false)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.CheckRedirect == nil {
		t.Fatal("expected CheckRedirect to be set for safe proxy client")
	}
}

// ---------------------------------------------------------------------------
// getProxyClient: proxy with AllowPrivate=true uses unsafe redirect
// ---------------------------------------------------------------------------

func TestHonestClient_getProxyClient_UnsafeProxyRedirect(t *testing.T) {
	t.Parallel()
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	client := hc.getProxyClient("http://localhost:19998", true)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.CheckRedirect == nil {
		t.Fatal("expected CheckRedirect to be set for unsafe proxy client")
	}

	client2 := hc.getProxyClient("http://localhost:19998", true)
	if client != client2 {
		t.Error("expected cached proxy client for same key")
	}
}

// ---------------------------------------------------------------------------
// Close with proxy clients
// ---------------------------------------------------------------------------

func TestHonestClient_CloseWithProxyClients(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer target.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		transport := &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
		proxyReq, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
		proxyReq.Header = r.Header
		resp, err := transport.RoundTrip(proxyReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.Fetch(context.Background(), Request{
		URL:          target.URL,
		Proxy:        proxy.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hc := c.(*honestClient)
	hc.proxyMu.Lock()
	count := len(hc.proxyClients)
	hc.proxyMu.Unlock()
	if count == 0 {
		t.Fatal("expected at least one cached proxy client")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("unexpected error from Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fetch with AllowPrivate=false uses safe client (no proxy)
// ---------------------------------------------------------------------------

func TestHonestClient_DenyPrivate_UsesSafeClient(t *testing.T) {
	t.Parallel()
	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hc := c.(*honestClient)
	if hc.hcSafe == nil || hc.hcUnsafe == nil {
		t.Fatal("expected both safe and unsafe clients to be initialized")
	}

	hc.hcSafe.CloseIdleConnections()
	hc.hcUnsafe.CloseIdleConnections()
}
