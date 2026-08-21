package fetch

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedPrivate is returned when a request targets a private/loopback
// address and AllowPrivate is false.
type ErrBlockedPrivate struct {
	URL string
}

func (e *ErrBlockedPrivate) Error() string {
	return fmt.Sprintf("blocked_private: %s targets a private or loopback address", e.URL)
}

var ssrfResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		// The configured DNS resolver can legitimately be on a private or
		// loopback address (for example, a local VPN or router resolver).
		// SSRF protection applies to resolved request targets below, not to
		// the system resolver used to look them up.
		return (&net.Dialer{}).DialContext(ctx, network, address)
	},
}

// CheckSSRF returns an error if the URL targets a blocked private/loopback
// address. It is used by the robots package to apply the same SSRF protection
// to robots.txt fetches.
func CheckSSRF(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return &ErrBlockedPrivate{URL: rawURL}
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("invalid URL: no host")
	}

	// Perform DNS resolution and validate every resolved IP at connection
	// time using a custom Control function. This prevents DNS-rebinding
	// where a second lookup during the TCP handshake returns a private IP.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ips, err := ssrfResolver.LookupHost(ctx, host)
	if err != nil {
		// Fail closed: DNS resolution failure could indicate a rebinding
		// attack or NXDOMAIN bypass. The connect-time controlSSRF provides
		// a second layer, but failing here prevents any further processing.
		return fmt.Errorf("DNS resolution failed for %s: %w", host, err)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivate(ip) {
			return &ErrBlockedPrivate{URL: rawURL}
		}
	}
	return nil
}

// ControlSSRF is a net.Dialer.Control function that rejects connections to
// private/loopback addresses at TCP connect time, preventing DNS-rebinding.
// It is used by both the honest and azuretls clients to enforce SSRF
// protection at the TCP layer, independent of any earlier DNS check.
func ControlSSRF(network, address string, c syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip != nil && isPrivate(ip) {
		return &ErrBlockedPrivate{URL: address}
	}
	return nil
}

// ipv4MappedNet covers the ::ffff:0:0/96 range used to detect
// IPv4-mapped IPv6 addresses. It is checked separately because the
// /96 prefix matches all IPv4 addresses when applied to 4-byte IPs.
var ipv4MappedNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("::ffff:0:0/96")
	return n
}()

var privateRanges = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"::1/128",
		"169.254.0.0/16",
		"fe80::/10",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",
		"100.64.0.0/10",
	}
	var nets []*net.IPNet
	for _, cidr := range cidrs {
		_, n, _ := net.ParseCIDR(cidr)
		if n != nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

func isPrivate(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range privateRanges {
		if n.Contains(ip) {
			return true
		}
	}
	if len(ip) == net.IPv6len && ipv4MappedNet.Contains(ip) {
		return isPrivate(ip[12:16])
	}
	return false
}
