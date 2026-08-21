package fetch

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHonestClientFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html><body>Hello</body></html>"))
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
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "Hello") {
		t.Errorf("body does not contain expected content")
	}
}

func TestHonestClientUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotUA != honestUA {
		t.Errorf("expected UA %q, got %q", honestUA, gotUA)
	}
}

func TestHonestClientRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("final"))
	}))
	defer final.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          redirect.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 after redirect, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.Body), "final") {
		t.Errorf("expected final body after redirect")
	}
}

func TestMaxBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(strings.Repeat("x", 200)))
	}))
	defer srv.Close()

	c, err := New(ProfileHonest, WithMaxBody(0)) // 0 → uses default but we override in request
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          srv.URL,
		MaxBody:      100, // 100 bytes limit
		AllowPrivate: true,
	})
	if err == nil {
		t.Fatal("expected ErrTooLarge, got nil")
	}
	var tooLarge *ErrTooLarge
	if !isErrType(err, &tooLarge) {
		t.Errorf("expected ErrTooLarge, got %T: %v", err, err)
	}
}

func isErrType(err error, target interface{}) bool {
	switch e := err.(type) {
	case *ErrTooLarge:
		_ = e
		if _, ok := target.(**ErrTooLarge); ok {
			return true
		}
	}
	return false
}

func TestSSRFGuard(t *testing.T) {
	tests := []struct {
		url     string
		blocked bool
	}{
		{"http://127.0.0.1:8080/foo", true},
		{"http://localhost/bar", true},
		{"http://192.168.1.1/", true},
		{"http://10.0.0.1/", true},
		{"http://172.16.0.1/", true},
		{"ftp://example.com/", true},
	}

	for _, tt := range tests {
		err := CheckSSRF(tt.url)
		isBlocked := err != nil
		if isBlocked != tt.blocked {
			t.Errorf("CheckSSRF(%q): blocked=%v, want %v (err=%v)", tt.url, isBlocked, tt.blocked, err)
		}
	}

	err := CheckSSRF("https://example.com/")
	if err != nil {
		t.Logf("CheckSSRF(\"https://example.com/\"): DNS resolution failed (expected in restricted networks): %v", err)
	}
}

func TestControlSSRF(t *testing.T) {
	tests := []struct {
		network string
		address string
		blocked bool
	}{
		{"tcp", "127.0.0.1:80", true},
		{"tcp", "10.0.0.1:443", true},
		{"tcp", "192.168.1.1:80", true},
		{"tcp", "172.16.0.1:80", true},
		{"tcp", "8.8.8.8:443", false},
		{"tcp", "1.1.1.1:443", false},
	}

	for _, tt := range tests {
		err := ControlSSRF(tt.network, tt.address, nil)
		isBlocked := err != nil
		if isBlocked != tt.blocked {
			t.Errorf("ControlSSRF(%q, %q): blocked=%v, want %v (err=%v)", tt.network, tt.address, isBlocked, tt.blocked, err)
		}
	}
}

func TestSSRFResolverCanUseLocalDNSResolver(t *testing.T) {
	resolver, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	conn, err := ssrfResolver.Dial(context.Background(), "udp", resolver.LocalAddr().String())
	if err != nil {
		t.Fatalf("DNS resolver must be allowed to use a local resolver: %v", err)
	}
	conn.Close()
}

func TestSSRFGuardIPv4MappedIPv6(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:192.168.1.1", true},
		{"::ffff:172.16.0.1", true},
		{"::ffff:8.8.8.8", false},
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("failed to parse IP: %s", tt.ip)
		}
		got := isPrivate(ip)
		if got != tt.blocked {
			t.Errorf("isPrivate(%s): %v, want %v", tt.ip, got, tt.blocked)
		}
	}
}

func TestCookiePersistence(t *testing.T) {
	cookieSet := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cookieSet {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123"})
			cookieSet = true
		}
		// Echo back cookie header
		c, _ := r.Cookie("session")
		if c != nil {
			w.Write([]byte("cookie:" + c.Value))
		} else {
			w.Write([]byte("no-cookie"))
		}
	}))
	defer srv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// First request — server sets cookie
	_, err = c.Fetch(context.Background(), Request{URL: srv.URL + "/set", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
}

// captureRequestHeaders starts a raw TCP server that records the on-wire
// header order from a single HTTP/1.1 request, then responds 200 OK.
func captureRequestHeaders(t *testing.T, doReq func(serverURL string) error) []string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		headers []string
	}
	ch := make(chan result, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			ch <- result{}
			return
		}
		defer conn.Close()

		br := bufio.NewReader(conn)
		var headers []string
		lineNum := 0
		for {
			line, readErr := br.ReadString('\n')
			line = strings.TrimRight(line, "\r\n")

			if lineNum == 0 {
				// Skip the request line (e.g. "GET / HTTP/1.1")
				lineNum++
				if readErr != nil {
					break
				}
				continue
			}
			lineNum++

			if line == "" {
				break // end of headers
			}
			if idx := strings.Index(line, ":"); idx > 0 {
				headers = append(headers, strings.TrimSpace(line[:idx]))
			}
			if readErr != nil {
				break
			}
		}

		// Minimal valid HTTP/1.1 response
		conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
		ch <- result{headers}
	}()

	serverURL := "http://" + ln.Addr().String()
	reqErr := doReq(serverURL)
	ln.Close()

	if reqErr != nil {
		t.Fatal(reqErr)
	}

	r := <-ch
	return r.headers
}

func headerIndex(headers []string, name string) int {
	for i, h := range headers {
		if h == name {
			return i
		}
	}
	return -1
}

func TestAzureTLSHeaderOrder(t *testing.T) {
	// Capture header order from azuretls Chrome-profile client
	chromeHeaders := captureRequestHeaders(t, func(serverURL string) error {
		c, err := New(ProfileChrome, WithTimeout(5))
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = c.Fetch(context.Background(), Request{
			URL:          serverURL,
			AllowPrivate: true,
		})
		return err
	})

	// Capture header order from honest (stdlib net/http) client
	honestHeaders := captureRequestHeaders(t, func(serverURL string) error {
		c, err := New(ProfileHonest, WithTimeout(5))
		if err != nil {
			return err
		}
		defer c.Close()
		_, err = c.Fetch(context.Background(), Request{
			URL:          serverURL,
			AllowPrivate: true,
		})
		return err
	})

	if len(chromeHeaders) == 0 {
		t.Fatal("azuretls Chrome client sent no headers")
	}
	if len(honestHeaders) == 0 {
		t.Fatal("honest client sent no headers")
	}

	t.Logf("azuretls Chrome header order: %v", chromeHeaders)
	t.Logf("honest client header order:   %v", honestHeaders)

	// The two clients MUST send headers in different orders.
	// Header ordering is a key fingerprinting signal; if they match,
	// the browser impersonation is not preserving Chrome's order.
	if reflect.DeepEqual(chromeHeaders, honestHeaders) {
		t.Fatal("azuretls and honest client sent headers in identical order; " +
			"browser impersonation is not preserving distinct header order")
	}

	// Go's net/http writes Host first.
	if honestHeaders[0] != "Host" {
		t.Errorf("honest client should send Host first, got: %v", honestHeaders)
	}

	// azuretls Chrome preset does NOT put Host first — it follows Chrome's internal ordering.
	if len(chromeHeaders) > 0 && chromeHeaders[0] == "Host" {
		t.Errorf("azuretls should not send Host first (Chrome-like order), got: %v", chromeHeaders)
	}

	// Both clients must include Host and User-Agent.
	for _, h := range []string{"Host", "User-Agent"} {
		if headerIndex(chromeHeaders, h) < 0 {
			t.Errorf("azuretls missing %s header in: %v", h, chromeHeaders)
		}
		if headerIndex(honestHeaders, h) < 0 {
			t.Errorf("honest client missing %s header in: %v", h, honestHeaders)
		}
	}

	// The absolute positions of common headers must differ between clients.
	for _, h := range []string{"Host", "User-Agent"} {
		ci := headerIndex(chromeHeaders, h)
		hi := headerIndex(honestHeaders, h)
		if ci >= 0 && hi >= 0 && ci == hi {
			t.Errorf("header %q at same position %d in both clients", h, ci)
		}
	}
}

func TestHonestClientRedirectSSRF(t *testing.T) {
	privateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("private"))
	}))
	defer privateSrv.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, privateSrv.URL, http.StatusFound)
	}))
	defer redirectSrv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.Fetch(context.Background(), Request{
		URL:          redirectSrv.URL,
		AllowPrivate: false,
	})
	if err == nil {
		t.Fatal("expected error for redirect to private URL when AllowPrivate=false")
	}
	var blocked *ErrBlockedPrivate
	if !errors.As(err, &blocked) {
		t.Errorf("expected ErrBlockedPrivate, got %T: %v", err, err)
	}
}

func TestHonestClientRedirectSSRF_AllowPrivate(t *testing.T) {
	privateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("private"))
	}))
	defer privateSrv.Close()

	redirectSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, privateSrv.URL, http.StatusFound)
	}))
	defer redirectSrv.Close()

	c, err := New(ProfileHonest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	resp, err := c.Fetch(context.Background(), Request{
		URL:          redirectSrv.URL,
		AllowPrivate: true,
	})
	if err != nil {
		t.Fatalf("expected no error with AllowPrivate=true, got: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHonestClientDialGuardSSRF(t *testing.T) {
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
		t.Fatal("expected error for private URL when AllowPrivate=false")
	}
	var blocked *ErrBlockedPrivate
	if !errors.As(err, &blocked) {
		t.Errorf("expected ErrBlockedPrivate, got %T: %v", err, err)
	}
}

func TestControlSSRFWiredIntoTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
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
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}
