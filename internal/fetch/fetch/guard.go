package fetch

import (
	"net"
	"syscall"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// ErrBlockedPrivate is a compatibility alias for the canonical policy error.
// Keeping the alias lets callers use errors.As with either package's name.
type ErrBlockedPrivate = policy.BlockedPrivateError

// CheckSSRF delegates to the canonical policy package.
func CheckSSRF(rawURL string) error {
	return policy.CheckSSRF(rawURL)
}

// CheckSSRFWithLookup delegates to the canonical policy package with an
// injectable hostname lookup for deterministic callers and tests.
func CheckSSRFWithLookup(rawURL string, lookup policy.LookupFunc) error {
	return policy.CheckSSRFWithLookup(rawURL, lookup)
}

// ControlSSRF is a net.Dialer.Control function that rejects connections to
// private/loopback addresses at TCP connect time, preventing DNS-rebinding.
// It remains the connect-time hook used by the static fetch transports.
func ControlSSRF(network, address string, c syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip != nil && policy.IsPrivateIP(ip) {
		return &ErrBlockedPrivate{URL: address}
	}
	return nil
}
