package safaribidi

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"
)

// maxConnectAttempts bounds the session/socket retry loop. Safari picks the
// BiDi port itself and does not verify it is free, so a collision is possible
// on every attempt but is not sticky: measured on 2026-09-03, three
// consecutive sessions were offered ports 8087, 8081 and 8094.
const maxConnectAttempts = 4

// session is a verified BiDi connection: a safaridriver process, a WebDriver
// session, and a socket that has answered session.status as BiDi.
type session struct {
	driver *driverSession
	conn   *connection
}

// connect starts safaridriver, creates a session, and returns a socket that
// has been proven to be Safari's.
//
// The proof is not optional. Safari selects the BiDi port on its own and binds
// it without checking whether the port is already taken; it then reports that
// port in webSocketUrl regardless. Measured on 2026-09-03, Safari handed out
// ws://127.0.0.1:8081/… while an unrelated SSH tunnel owned 8081, so the URL
// pointed at a foreign service. Connecting to a stranger's socket and issuing
// browser commands against it is not an acceptable failure mode, so every
// connection is verified with session.status before any other command is sent,
// and a socket that does not answer as BiDi is discarded with its session.
func connect(ctx context.Context, options Options) (*session, error) {
	var lastErr error
	for attempt := 0; attempt < maxConnectAttempts; attempt++ {
		driver, err := startDriver(ctx, options)
		if err != nil {
			// Session creation failures are configuration problems, not port
			// collisions; retrying cannot fix them and would multiply Safari's
			// 30-second timeout.
			return nil, err
		}
		conn, err := verifiedDial(ctx, driver.webSocketURL, options)
		if err == nil {
			return &session{driver: driver, conn: conn}, nil
		}
		lastErr = err
		_ = driver.Close(ctx)
	}
	return nil, fmt.Errorf("safari-bidi: no usable BiDi socket after %d attempts; Safari assigns its own port and does not check that it is free, so a local service may be holding it: %w", maxConnectAttempts, lastErr)
}

func verifiedDial(ctx context.Context, endpoint string, options Options) (*connection, error) {
	if err := requireLoopback(endpoint); err != nil {
		return nil, err
	}
	conn, err := dial(ctx, endpoint, options.driverReadyTimeout())
	if err != nil {
		return nil, err
	}
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var status struct {
		Ready bool `json:"ready"`
	}
	if err := conn.Execute(statusCtx, "session.status", map[string]any{}, &status); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("socket %s did not answer session.status as WebDriver BiDi; it is not Safari: %w", endpoint, err)
	}
	return conn, nil
}

// requireLoopback rejects a webSocketUrl that does not point at this machine.
// The URL is Safari's to choose, but the engine's trust boundary is local.
func requireLoopback(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse bidi socket url %q: %w", endpoint, err)
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("refusing non-loopback bidi socket %q", endpoint)
	}
	return nil
}

func (s *session) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return s.driver.Close(ctx)
}
