package policy

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// BlockedPrivateError is returned when a request targets a private, loopback,
// link-local, or otherwise non-public address and the SSRF guard is active.
type BlockedPrivateError struct {
	URL string
}

func (e *BlockedPrivateError) Error() string {
	return fmt.Sprintf("blocked_private: %s targets a private or loopback address", e.URL)
}

// ssrfResolver mirrors symfetch's resolver: the configured DNS resolver may
// legitimately live on a private or loopback address (local VPN, router
// resolver), so SSRF protection applies to resolved request targets, not to
// the system resolver used to look them up.
var ssrfResolver = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	},
}

// ssrfLookupFunc resolves a hostname to IP addresses. It is a field so tests
// can inject a deterministic resolver (DNS-rebinding fixtures) without
// touching the network.
type ssrfLookupFunc func(ctx context.Context, host string) ([]string, error)

// SSRFGuard blocks requests to private network ranges. It is deny-by-default
// while enabled: RFC1918, loopback, link-local, .local mDNS names, IPv6
// unique-local, and unspecified addresses (0.0.0.0/8, ::/128) are rejected.
// Matching symfetch's semantics, the hostname is resolved at decision time
// and every resolved address is checked, so a rebinding hostname that
// answers with a private address is blocked before the browser connects.
type SSRFGuard struct {
	enabled      bool
	allowPrivate bool
	lookup       ssrfLookupFunc
}

// NewSSRFGuard builds a guard. allowPrivate relaxes the policy so that
// private targets are permitted (the --allow-private opt-in).
func NewSSRFGuard(allowPrivate bool) *SSRFGuard {
	return &SSRFGuard{
		enabled:      true,
		allowPrivate: allowPrivate,
		lookup: func(ctx context.Context, host string) ([]string, error) {
			return ssrfResolver.LookupHost(ctx, host)
		},
	}
}

// Enabled reports whether the guard is active. A nil guard is inactive.
func (g *SSRFGuard) Enabled() bool {
	return g != nil && g.enabled
}

// AllowsURL reports whether a request to u may proceed. Unparsable targets
// and non-http(s) schemes are denied: the guard must fail closed.
func (g *SSRFGuard) AllowsURL(u *url.URL) error {
	if !g.Enabled() || g.allowPrivate {
		return nil
	}
	if u == nil {
		return &BlockedPrivateError{URL: ""}
	}
	raw := u.String()
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return &BlockedPrivateError{URL: raw}
	}
	host := u.Hostname()
	if host == "" {
		return &BlockedPrivateError{URL: raw}
	}
	return g.AllowsHost(host, raw)
}

// AllowsHost checks one hostname. The raw form is used for error reporting.
func (g *SSRFGuard) AllowsHost(hostname, raw string) error {
	if !g.Enabled() || g.allowPrivate {
		return nil
	}
	host := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if host == "" {
		return &BlockedPrivateError{URL: raw}
	}
	// .local is the mDNS namespace: it resolves through link-local
	// multicast and almost always lands on private addresses. It is
	// blocked by suffix so a resolution failure cannot be used to slip
	// past the guard. (Deviation from symfetch, documented in docs/ssrf.md.)
	if strings.HasSuffix(host, ".local") || host == "localhost" {
		return &BlockedPrivateError{URL: raw}
	}
	// Resolve at decision time and validate every address. DNS failure
	// fails closed: a rebinding host or NXDOMAIN bypass must not proceed.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := g.lookup(ctx, host)
	if err != nil {
		return fmt.Errorf("DNS resolution failed for %s: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return &BlockedPrivateError{URL: raw}
		}
	}
	return nil
}

// ipv4MappedNet covers the ::ffff:0:0/96 range used to detect IPv4-mapped
// IPv6 addresses. It is checked separately because the /96 prefix matches all
// IPv4 addresses when applied to 4-byte IPs.
var ipv4MappedNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("::ffff:0:0/96")
	return n
}()

// privateRanges are the networks blocked by the guard: RFC1918 private
// space, loopback, link-local, carrier-grade NAT, IPv6 unique-local,
// and the unspecified address ranges (0.0.0.0/8 and ::/128).
// The set is identical to symfetch's guard so both tools behave alike.
var privateRanges = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"::/128",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
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

// isPrivateIP reports whether ip falls into any blocked network.
func isPrivateIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range privateRanges {
		if n.Contains(ip) {
			return true
		}
	}
	if len(ip) == net.IPv6len && ipv4MappedNet.Contains(ip) {
		return isPrivateIP(ip[12:16])
	}
	return false
}
