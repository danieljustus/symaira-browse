package safaribidi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// DriverPath is the stable location of safaridriver on macOS. symbrowse ships
// no browser and no driver; it uses what the system provides.
const DriverPath = "/usr/bin/safaridriver"

// sessionRequest is the W3C session-creation payload.
//
// Two capabilities matter and neither is guessable from the specification.
// Measured on 2026-09-03 against Safari 27.0 (26A5425a), macOS 27.0:
//
//   - "webSocketUrl": true alone is NOT enough. Safari accepts it, echoes it
//     back as the boolean it was given, sets "safari:experimentalWebSocketUrl"
//     to false, and opens no socket at all. The session is a plain WebDriver
//     session that merely looks BiDi-capable.
//   - "safari:experimentalWebSocketUrl": true is what actually opens the
//     socket. Safari then returns "webSocketUrl" as a real ws:// URL.
//
// Both are sent, because the standard capability is what a future
// non-experimental Safari will honour and the vendor one is what today's does.
type sessionRequest struct {
	Capabilities struct {
		AlwaysMatch map[string]any `json:"alwaysMatch"`
	} `json:"capabilities"`
}

type sessionResponse struct {
	Value struct {
		SessionID    string          `json:"sessionId"`
		Capabilities json.RawMessage `json:"capabilities"`
		Error        string          `json:"error"`
		Message      string          `json:"message"`
	} `json:"value"`
}

// driverSession is a live safaridriver process plus the WebDriver session it
// hosts. Close tears down both, in that order.
type driverSession struct {
	cmd          *exec.Cmd
	httpPort     int
	sessionID    string
	webSocketURL string
	capabilities json.RawMessage
	stderr       *bytes.Buffer
}

// startDriver spawns safaridriver and creates a BiDi-enabled session.
//
// The --bidi port is required for the driver to enable BiDi at all, but it is
// NOT the port the socket ends up on: Safari picks its own and reports it in
// the session's webSocketUrl. The URL is therefore always read from the
// response and never reconstructed from the flag.
func startDriver(ctx context.Context, options Options) (*driverSession, error) {
	driver := options.DriverPath
	if driver == "" {
		driver = DriverPath
	}
	httpPort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate safaridriver port: %w", err)
	}
	bidiPort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate safaridriver bidi port: %w", err)
	}

	args := []string{"-p", fmt.Sprint(httpPort), "--bidi", fmt.Sprint(bidiPort)}
	if options.Diagnose {
		args = append(args, "--diagnose")
	}
	cmd := exec.Command(driver, args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", driver, err)
	}
	session := &driverSession{cmd: cmd, httpPort: httpPort, stderr: stderr}

	if err := waitForDriver(ctx, httpPort, options.driverReadyTimeout()); err != nil {
		session.kill()
		return nil, err
	}
	if err := session.createSession(ctx, options); err != nil {
		session.kill()
		return nil, err
	}
	return session, nil
}

func (d *driverSession) createSession(ctx context.Context, options Options) error {
	request := sessionRequest{}
	request.Capabilities.AlwaysMatch = map[string]any{
		"browserName":                     "safari",
		"webSocketUrl":                    true,
		"safari:experimentalWebSocketUrl": true,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode session request: %w", err)
	}

	timeout := options.sessionTimeout()
	postCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(postCtx, http.MethodPost, d.baseURL()+"/session", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build session request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("create safaridriver session: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	var decoded sessionResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode session response: %w", err)
	}
	if decoded.Value.Error != "" {
		return sessionCreationError(decoded.Value.Error, decoded.Value.Message)
	}
	if decoded.Value.SessionID == "" {
		return fmt.Errorf("safaridriver returned no session id (HTTP %d)", response.StatusCode)
	}

	var caps struct {
		WebSocketURL json.RawMessage `json:"webSocketUrl"`
	}
	_ = json.Unmarshal(decoded.Value.Capabilities, &caps)
	var socketURL string
	// webSocketUrl is a string when the socket is real and a bare `true` when
	// Safari acknowledged the capability without opening one. Only the string
	// form is usable, so the boolean form is reported as the distinct failure
	// it is rather than being coerced into a URL.
	if err := json.Unmarshal(caps.WebSocketURL, &socketURL); err != nil || socketURL == "" {
		return fmt.Errorf("safaridriver created a session without a BiDi socket (webSocketUrl=%s); this Safari does not support safari:experimentalWebSocketUrl", strings.TrimSpace(string(caps.WebSocketURL)))
	}

	d.sessionID = decoded.Value.SessionID
	d.webSocketURL = socketURL
	d.capabilities = decoded.Value.Capabilities
	return nil
}

// sessionCreationError translates Safari's single opaque timeout message into
// the two distinct causes behind it.
//
// Safari reports both with byte-identical text. The discriminator is not in
// the WebDriver response at all — it is only in the unified log, where the
// second case logs
//
//	[com.apple.Safari:Automation] Rejecting session (…): Safari was not
//	launched for automation.
//
// Relaying Apple's text alone would leave a user with a 30-second timeout and
// no cause, so the likely causes are named here in the order they bite.
func sessionCreationError(code, message string) error {
	if !strings.Contains(message, "Request creation of a new automation session") {
		return fmt.Errorf("safaridriver session not created (%s): %s", code, message)
	}
	return fmt.Errorf(`safaridriver could not create a session (%s). Safari reports this same timeout for two different causes:
  1. Safari is already running for normal browsing. safaridriver then attaches to that instance instead of launching its own, and Safari rejects it ("Safari was not launched for automation"). Quit Safari completely (Cmd-Q) and retry.
  2. Remote automation is not permitted. Run "sudo safaridriver --enable" once, and enable Safari > Settings > Developer > "Allow remote automation".
Apple's own message was: %s`, code, message)
}

func (d *driverSession) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", d.httpPort)
}

// Close deletes the WebDriver session and stops the driver process. Deleting
// the session first lets Safari shut down its automation window cleanly.
func (d *driverSession) Close(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if d.sessionID != "" {
		deleteCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		request, err := http.NewRequestWithContext(deleteCtx, http.MethodDelete, d.baseURL()+"/session/"+d.sessionID, nil)
		if err == nil {
			if response, err := http.DefaultClient.Do(request); err == nil {
				_ = response.Body.Close()
			}
		}
		cancel()
		d.sessionID = ""
	}
	d.kill()
	return nil
}

func (d *driverSession) kill() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Kill()
	_, _ = d.cmd.Process.Wait()
	d.cmd = nil
}

// waitForDriver polls /status until safaridriver's HTTP server accepts
// requests. safaridriver writes nothing to stdout on success, so readiness
// cannot be detected by watching its output.
func waitForDriver(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/status", port)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("safaridriver did not become ready on port %d within %s", port, timeout)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
		if err == nil {
			response, err := http.DefaultClient.Do(request)
			if err == nil {
				_ = response.Body.Close()
				cancel()
				return nil
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// freePort asks the kernel for an unused localhost port. safaridriver takes a
// port number rather than reporting one it chose, so the caller must pick.
func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
