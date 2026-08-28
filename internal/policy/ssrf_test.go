package policy

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
)

// stubLookup returns a fixed address list for a hostname, simulating a DNS
// resolver without touching the network. It is the DNS-rebinding fixture:
// the test controls exactly what the guard observes at decision time.
func stubLookup(addresses ...string) ssrfLookupFunc {
	return func(ctx context.Context, host string) ([]string, error) {
		return addresses, nil
	}
}

func TestSSRFGuardAllowsPublicTargets(t *testing.T) {
	guard := NewSSRFGuard(false)
	guard.lookup = stubLookup("93.184.216.34") // example.com
	cases := []string{
		"https://example.com/",
		"http://example.com:8080/path?q=1",
		"https://public.example.org:443/x",
	}
	for _, raw := range cases {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if err := guard.AllowsURL(u); err != nil {
			t.Errorf("AllowsURL(%q) = %v, want nil", raw, err)
		}
	}
}

func TestSSRFGuardBlocksPrivateTargets(t *testing.T) {
	cases := []struct {
		name    string
		lookup  ssrfLookupFunc
		raw     string
		wantErr string
	}{
		{name: "literal loopback", lookup: stubLookup("127.0.0.1"), raw: "http://127.0.0.1:8080/", wantErr: "blocked_private"},
		{name: "literal rfc1918 10/8", lookup: stubLookup("10.0.0.5"), raw: "http://10.0.0.5/", wantErr: "blocked_private"},
		{name: "literal rfc1918 172.16/12", lookup: stubLookup("172.16.3.9"), raw: "http://172.16.3.9/", wantErr: "blocked_private"},
		{name: "literal rfc1918 172.31/12", lookup: stubLookup("172.31.255.1"), raw: "http://172.31.255.1/", wantErr: "blocked_private"},
		{name: "literal rfc1918 192.168/16", lookup: stubLookup("192.168.1.42"), raw: "http://192.168.1.42/", wantErr: "blocked_private"},
		{name: "link-local", lookup: stubLookup("169.254.10.10"), raw: "http://169.254.10.10/", wantErr: "blocked_private"},
		{name: "carrier-grade nat", lookup: stubLookup("100.64.0.1"), raw: "http://100.64.0.1/", wantErr: "blocked_private"},
		{name: "ipv6 loopback", lookup: stubLookup("::1"), raw: "http://[::1]/", wantErr: "blocked_private"},
		{name: "ipv6 link-local", lookup: stubLookup("fe80::1"), raw: "http://[fe80::1]/", wantErr: "blocked_private"},
		{name: "ipv6 unique-local", lookup: stubLookup("fd00::1"), raw: "http://[fd00::1]/", wantErr: "blocked_private"},
		{name: "ipv4-unspecified (0.0.0.0)", lookup: stubLookup("0.0.0.0"), raw: "http://0.0.0.0/", wantErr: "blocked_private"},
		{name: "ipv6-unspecified (::)", lookup: stubLookup("::"), raw: "http://[::]/", wantErr: "blocked_private"},
		{name: "ipv4-mapped loopback", lookup: stubLookup("::ffff:127.0.0.1"), raw: "http://[::ffff:127.0.0.1]/", wantErr: "blocked_private"},
		{name: "hostname resolving to 0.0.0.0", lookup: stubLookup("0.0.0.0"), raw: "http://zero.resolves.local/", wantErr: "blocked_private"},
		{name: "hostname resolving to ::", lookup: stubLookup("::"), raw: "http://v6unspec.resolves.local/", wantErr: "blocked_private"},
		{name: "hostname resolving to loopback", lookup: stubLookup("127.0.0.1"), raw: "http://intranet.corp/", wantErr: "blocked_private"},
		// DNS-rebinding fixture: a public-looking name that answers with a
		// private address at decision time must be blocked.
		{name: "rebinding public name answers private", lookup: stubLookup("93.184.216.34", "127.0.0.1"), raw: "http://rebind.example/", wantErr: "blocked_private"},
		{name: "mDNS .local suffix", lookup: stubLookup("192.168.1.7"), raw: "http://printer.local/", wantErr: "blocked_private"},
		{name: "localhost name", lookup: stubLookup("127.0.0.1"), raw: "http://localhost:3000/", wantErr: "blocked_private"},
		{name: "non-http scheme", lookup: stubLookup("127.0.0.1"), raw: "file:///etc/passwd", wantErr: "blocked_private"},
		{name: "no host", lookup: stubLookup("127.0.0.1"), raw: "http:///path", wantErr: "blocked_private"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := NewSSRFGuard(false)
			guard.lookup = tc.lookup
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			err = guard.AllowsURL(u)
			if err == nil {
				t.Fatalf("AllowsURL(%q) = nil, want %q", tc.raw, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("AllowsURL(%q) error = %q, want substring %q", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestSSRFGuardDNSFailureFailsClosed(t *testing.T) {
	guard := NewSSRFGuard(false)
	guard.lookup = func(ctx context.Context, host string) ([]string, error) {
		return nil, context.DeadlineExceeded
	}
	u, err := url.Parse("http://rebind.example/")
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.AllowsURL(u); err == nil {
		t.Fatal("AllowsURL = nil, want DNS failure to fail closed")
	}
}

func TestSSRFGuardAllowPrivateOptIn(t *testing.T) {
	guard := NewSSRFGuard(true)
	guard.lookup = stubLookup("127.0.0.1")
	for _, raw := range []string{"http://127.0.0.1:8080/", "http://10.0.0.5/", "http://localhost:3000/"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := guard.AllowsURL(u); err != nil {
			t.Errorf("AllowsURL(%q) with allowPrivate = %v, want nil", raw, err)
		}
	}
}

func TestSSRFGuardInactiveWhenDisabled(t *testing.T) {
	var guard *SSRFGuard
	if guard.Enabled() {
		t.Fatal("nil guard must be inactive")
	}
}

func TestSSRFGuardIPClassification(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	private := []string{"127.0.0.1", "127.255.255.254", "10.255.255.255", "172.16.0.1", "172.31.255.254", "192.168.0.1", "169.254.0.1", "100.64.0.1", "::1", "fe80::1", "fd00::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "0.0.0.0", "::"}
	for _, raw := range public {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("parse %q", raw)
		}
		if IsPrivateIP(ip) {
			t.Errorf("IsPrivateIP(%s) = true, want false", raw)
		}
	}
	for _, raw := range private {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("parse %q", raw)
		}
		if !IsPrivateIP(ip) {
			t.Errorf("IsPrivateIP(%s) = false, want true", raw)
		}
	}
}
