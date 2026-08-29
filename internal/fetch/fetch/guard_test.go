package fetch

import (
	"net"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

func TestIsPrivate(t *testing.T) {
	tests := []struct {
		name     string
		ip       net.IP
		expected bool
	}{
		// Loopback
		{"IPv4 loopback", net.ParseIP("127.0.0.1"), true},
		{"IPv4 loopback edge", net.ParseIP("127.255.255.255"), true},
		{"IPv6 loopback", net.ParseIP("::1"), true},

		// Link-local
		{"IPv4 link-local", net.ParseIP("169.254.1.1"), true},
		{"IPv6 link-local", net.ParseIP("fe80::1"), true},

		// RFC-1918
		{"10.x.x.x", net.ParseIP("10.0.0.1"), true},
		{"172.16.x.x", net.ParseIP("172.16.0.1"), true},
		{"192.168.x.x", net.ParseIP("192.168.1.1"), true},

		// IPv6 private (ULA)
		{"IPv6 ULA", net.ParseIP("fd00::1"), true},

		// IPv4-mapped IPv6 (the fix for #78)
		{"IPv4-mapped loopback", net.ParseIP("::ffff:127.0.0.1"), true},
		{"IPv4-mapped 10.x", net.ParseIP("::ffff:10.0.0.1"), true},
		{"IPv4-mapped 192.168.x", net.ParseIP("::ffff:192.168.1.1"), true},
		{"IPv4-mapped 172.16.x", net.ParseIP("::ffff:172.16.0.1"), true},
		{"IPv4-mapped public", net.ParseIP("::ffff:8.8.8.8"), false},

		// CGNAT
		{"CGNAT", net.ParseIP("100.64.0.1"), true},

		// Public IPs
		{"public IPv4", net.ParseIP("8.8.8.8"), false},
		{"public IPv4 alt", net.ParseIP("1.1.1.1"), false},
		{"public IPv6", net.ParseIP("2001:4860:4860::8888"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.IsPrivateIP(tt.ip)
			if got != tt.expected {
				t.Errorf("policy.IsPrivateIP(%v) = %v, want %v", tt.ip, got, tt.expected)
			}
		})
	}
}

func TestIsPrivate_IPv4MappedIPv6Bypass(t *testing.T) {
	bypassIPs := []string{
		"::ffff:127.0.0.1",
		"::ffff:10.0.0.1",
		"::ffff:172.16.0.1",
		"::ffff:192.168.1.1",
		"::ffff:100.64.0.1",
	}
	for _, ipStr := range bypassIPs {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			t.Fatalf("failed to parse %q", ipStr)
		}
		if !policy.IsPrivateIP(ip) {
			t.Errorf("policy.IsPrivateIP(%s) = false, want true (IPv4-mapped IPv6 bypass detected)", ipStr)
		}
	}
}

func TestErrBlockedPrivate_Error(t *testing.T) {
	e := &ErrBlockedPrivate{URL: "http://127.0.0.1:8080/admin"}
	msg := e.Error()
	if msg == "" {
		t.Error("expected non-empty error message")
	}
	if !strings.Contains(msg, "127.0.0.1") {
		t.Errorf("error message should contain URL, got: %q", msg)
	}
	if !strings.Contains(msg, "blocked_private") {
		t.Errorf("error message should contain 'blocked_private', got: %q", msg)
	}
}

func TestControlSSRF_NoPortInAddress(t *testing.T) {
	err := ControlSSRF("tcp", "8.8.8.8", nil)
	if err != nil {
		t.Errorf("expected no error for public IP without port, got: %v", err)
	}
}

func TestControlSSRF_PrivateNoPort(t *testing.T) {
	err := ControlSSRF("tcp", "127.0.0.1", nil)
	if err == nil {
		t.Error("expected error for private IP without port")
	}
}

func TestControlSSRF_InvalidIP(t *testing.T) {
	err := ControlSSRF("tcp", "not-an-ip:80", nil)
	if err != nil {
		t.Errorf("expected no error for non-parseable IP, got: %v", err)
	}
}

func TestCheckSSRF_InvalidURL(t *testing.T) {
	err := CheckSSRF("://bad-url")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestCheckSSRF_EmptyHost(t *testing.T) {
	err := CheckSSRF("http:///path")
	if err == nil {
		t.Error("expected error for empty host")
	}
}

func TestCheckSSRF_NonHTTPScheme(t *testing.T) {
	err := CheckSSRF("ftp://example.com/file")
	if err == nil {
		t.Error("expected error for non-HTTP scheme")
	}
}

func TestUnspecifiedAddressesBlockedByBothGuards(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		address string
	}{
		{name: "IPv4 unspecified", rawURL: "http://0.0.0.0:8080/", address: "0.0.0.0:8080"},
		{name: "IPv4 unspecified range", rawURL: "http://0.0.0.1:8080/", address: "0.0.0.1:8080"},
		{name: "IPv6 unspecified", rawURL: "http://[::]:8080/", address: "[::]:8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckSSRF(tt.rawURL); err == nil {
				t.Errorf("CheckSSRF(%q) = nil, want blocked_private", tt.rawURL)
			} else if !strings.Contains(err.Error(), "blocked_private") {
				t.Errorf("CheckSSRF(%q) = %q, want blocked_private", tt.rawURL, err)
			}

			if err := ControlSSRF("tcp", tt.address, nil); err == nil {
				t.Errorf("ControlSSRF(%q) = nil, want blocked_private", tt.address)
			} else if !strings.Contains(err.Error(), "blocked_private") {
				t.Errorf("ControlSSRF(%q) = %q, want blocked_private", tt.address, err)
			}
		})
	}
}

func TestSSRFGuardsAgreeOnAddressCorpus(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		blocked bool
	}{
		{name: "IPv4 unspecified", ip: "0.0.0.0", blocked: true},
		{name: "IPv4 unspecified range", ip: "0.0.0.1", blocked: true},
		{name: "IPv4 loopback", ip: "127.0.0.1", blocked: true},
		{name: "IPv4 private", ip: "10.0.0.1", blocked: true},
		{name: "IPv4 link-local", ip: "169.254.1.1", blocked: true},
		{name: "IPv4 CGNAT", ip: "100.64.0.1", blocked: true},
		{name: "IPv6 unspecified", ip: "::", blocked: true},
		{name: "IPv6 loopback", ip: "::1", blocked: true},
		{name: "IPv6 link-local", ip: "fe80::1", blocked: true},
		{name: "IPv6 unique-local", ip: "fd00::1", blocked: true},
		{name: "IPv4-mapped loopback", ip: "::ffff:127.0.0.1", blocked: true},
		{name: "public IPv4", ip: "8.8.8.8", blocked: false},
		{name: "public IPv6", ip: "2001:4860:4860::8888", blocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			address := net.JoinHostPort(tt.ip, "8080")
			rawURL := "http://" + address + "/"
			checkErr := CheckSSRF(rawURL)
			controlErr := ControlSSRF("tcp", address, nil)
			checkBlocked := checkErr != nil
			controlBlocked := controlErr != nil
			if checkBlocked != controlBlocked {
				t.Fatalf("address %q: CheckSSRF blocked=%v (err=%v), ControlSSRF blocked=%v (err=%v), want identical verdicts", tt.ip, checkBlocked, checkErr, controlBlocked, controlErr)
			}
			if checkBlocked != tt.blocked {
				t.Errorf("address %q: both guards blocked=%v, want %v (CheckSSRF err=%v, ControlSSRF err=%v)", tt.ip, checkBlocked, tt.blocked, checkErr, controlErr)
			}
		})
	}
}
