package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/config"
	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// redirectSocketDir points daemon.SocketPath at a throwaway directory so
// tests can plant a fake daemon socket without touching real runtime dirs.
// On darwin the socket lives under $HOME/Library/Caches/symbrowse/run, and
// unix socket paths are capped at 104 bytes, so HOME points at a short
// per-test directory under the system temp dir instead of a long
// t.TempDir() path.
func redirectSocketDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		dir, err := os.MkdirTemp("", "sb-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		t.Setenv("HOME", dir)
		return
	}
	if runtime.GOOS == "windows" {
		dir, err := os.MkdirTemp("", "sb-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		t.Setenv("XDG_RUNTIME_DIR", dir)
		return
	}
	// socketBaseDir honors XDG_RUNTIME_DIR on every non-darwin platform.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

// uniqueSession returns a short session name that cannot collide with a
// running daemon or with other tests. The name must stay short so the
// darwin socket path stays under the 104-byte sun_path limit; each test
// additionally gets its own HOME, so cross-test name reuse is harmless.
func uniqueSession() string {
	var buf [2]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("c%x", buf[:])
}

// fakeDaemon listens on a unix socket at path and answers every frame with
// the JSON produced by respond. A nil reply closes the connection without a
// response. A stale socket file from an earlier crashed run is removed first
// (mirroring the production daemon's stale-socket handling).
func fakeDaemon(t *testing.T, path string, respond func(frame []byte) []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(path)
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil || respond == nil {
					return
				}
				if reply := respond(line); reply != nil {
					_, _ = conn.Write(append(reply, '\n'))
				}
			}(conn)
		}
	}()
}

func TestStateRequestInvalidSession(t *testing.T) {
	if _, err := daemonRequest(context.Background(), "bad session!", "cookies.list", nil); err == nil {
		t.Fatal("expected an invalid session name to fail the daemon request")
	}
}

func TestDaemonLifecycleRequestInvalidSession(t *testing.T) {
	if _, err := daemonLifecycleRequest(context.Background(), "bad session!", "daemon.status", false); err == nil {
		t.Fatal("expected an invalid session name to fail daemonLifecycleRequest")
	}
}

func TestDaemonLifecycleRequestNoDaemon(t *testing.T) {
	redirectSocketDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := daemonLifecycleRequest(ctx, uniqueSession(), "daemon.status", false); err == nil {
		t.Fatal("expected a missing daemon socket to fail the request")
	}
}

func TestDaemonLifecycleCommandErrors(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	for _, sub := range []string{"status", "stop"} {
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"daemon", sub, "--session", session})
		if err := command.Execute(); err == nil {
			t.Fatalf("daemon %s: expected a missing-daemon failure", sub)
		}
	}
}

// TestStateCommandErrorFrames executes every cookies/storage subcommand
// against a fake daemon that answers with a JSON-RPC error frame; each
// command must surface the daemon error instead of succeeding.
func TestStateCommandErrorFrames(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, path, func([]byte) []byte {
		return []byte(`{"success":false,"error":{"code":"peer_denied","message":"denied by policy"}}`)
	})
	cases := []struct {
		name string
		args []string
	}{
		{"cookies list", []string{"cookies", "list"}},
		{"cookies set", []string{"cookies", "set", "k", "v"}},
		{"cookies clear", []string{"cookies", "clear", "k"}},
		{"storage get", []string{"storage", "get", "local"}},
		{"storage set", []string{"storage", "set", "local", "k", "v"}},
		{"storage clear", []string{"storage", "clear", "local"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := newRootCommand()
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append(tc.args, "--session", session))
			err := command.Execute()
			if err == nil {
				t.Fatal("expected the daemon error frame to fail the command")
			}
			if !strings.Contains(err.Error(), "denied by policy") {
				t.Fatalf("error = %v, want the daemon error message", err)
			}
		})
	}
}

// TestStateCommandConnectionErrors executes every cookies/storage subcommand
// with an unreachable daemon socket; each command must fail with the
// transport error. The context is already canceled so the client's autostart
// hook gives up without spawning a process.
func TestStateCommandConnectionErrors(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	cases := []struct {
		name string
		args []string
	}{
		{"cookies list", []string{"cookies", "list"}},
		{"cookies set", []string{"cookies", "set", "k", "v"}},
		{"cookies clear", []string{"cookies", "clear", "k"}},
		{"storage get", []string{"storage", "get", "local"}},
		{"storage set", []string{"storage", "set", "local", "k", "v"}},
		{"storage clear", []string{"storage", "clear", "local"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			command := newRootCommand()
			command.SetContext(ctx)
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append(tc.args, "--session", session))
			if err := command.Execute(); err == nil {
				t.Fatal("expected the unreachable daemon to fail the command")
			}
		})
	}
}

// TestStateCommandSuccessFrames drives the happy paths of the state commands
// against a fake daemon so the masking, formatting and output statements are
// covered without a real browser.
func TestStateCommandSuccessFrames(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	reply := func([]byte) []byte { return []byte(`{"success":true,"data":{}}`) }
	fakeDaemon(t, path, func(frame []byte) []byte {
		mu.Lock()
		fn := reply
		mu.Unlock()
		return fn(frame)
	})
	setReply := func(fn func([]byte) []byte) {
		mu.Lock()
		reply = fn
		mu.Unlock()
	}

	t.Run("cookies list masks and prints rows", func(t *testing.T) {
		setReply(func([]byte) []byte {
			return []byte(`{"success":true,"data":{"origin":"https://example.com","cookies":[{"name":"session","value":"abc123","domain":".example.com","path":"/","secure":true,"http_only":true}]}}`)
		})
		var output bytes.Buffer
		command := newRootCommand()
		command.SetOut(&output)
		command.SetArgs([]string{"cookies", "list", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "session	••••	.example.com	/	secure,httpOnly") {
			t.Fatalf("output = %q", output.String())
		}
	})

	t.Run("cookies list json masks values", func(t *testing.T) {
		setReply(func([]byte) []byte {
			return []byte(`{"success":true,"data":{"origin":"https://example.com","cookies":[{"name":"session","value":"abc123"}]}}`)
		})
		var output bytes.Buffer
		command := newRootCommand()
		command.SetOut(&output)
		command.SetArgs([]string{"cookies", "list", "--json", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		// The --json branch emits the masked payload directly (no envelope).
		if !strings.Contains(output.String(), "••••") || !strings.Contains(output.String(), `"origin"`) {
			t.Fatalf("output = %q", output.String())
		}
	})

	okCases := []struct {
		name string
		args []string
		want string
	}{
		{"cookies set", []string{"cookies", "set", "k", "v"}, "ok"},
		{"cookies clear", []string{"cookies", "clear", "k"}, "ok"},
		{"storage set", []string{"storage", "set", "local", "k", "v"}, "ok"},
		{"storage clear", []string{"storage", "clear", "local"}, "ok"},
	}
	for _, tc := range okCases {
		t.Run(tc.name, func(t *testing.T) {
			setReply(func([]byte) []byte { return []byte(`{"success":true,"data":{}}`) })
			var output bytes.Buffer
			command := newRootCommand()
			command.SetOut(&output)
			command.SetArgs(append(tc.args, "--session", session))
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), tc.want) {
				t.Fatalf("output = %q, want %q", output.String(), tc.want)
			}
		})
	}

	t.Run("storage get prints items", func(t *testing.T) {
		setReply(func([]byte) []byte {
			return []byte(`{"success":true,"data":{"origin":"https://example.com","kind":"local","items":{"theme":"dark"}}}`)
		})
		var output bytes.Buffer
		command := newRootCommand()
		command.SetOut(&output)
		command.SetArgs([]string{"storage", "get", "local", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "theme	dark") {
			t.Fatalf("output = %q", output.String())
		}
	})

	t.Run("storage get missing key fails", func(t *testing.T) {
		setReply(func([]byte) []byte {
			return []byte(`{"success":true,"data":{"origin":"https://example.com","kind":"local","items":{"theme":"dark"}}}`)
		})
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetArgs([]string{"storage", "get", "local", "missing", "--session", session})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), `key "missing" not found`) {
			t.Fatalf("err = %v, want missing-key failure", err)
		}
	})

	t.Run("storage get json prints the payload", func(t *testing.T) {
		setReply(func([]byte) []byte {
			return []byte(`{"success":true,"data":{"origin":"https://example.com","kind":"local","items":{"theme":"dark"}}}`)
		})
		var output bytes.Buffer
		command := newRootCommand()
		command.SetOut(&output)
		command.SetArgs([]string{"storage", "get", "local", "--json", "--session", session})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "https://example.com") {
			t.Fatalf("output = %q", output.String())
		}
	})
}

func TestStateCommandsRejectUnknownFlagsAndArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"cookies", "list", "--no-such-flag"}, "unknown flag"},
		{"extra positional", []string{"cookies", "list", "extra"}, "unknown command"},
		{"missing cookie args", []string{"cookies", "set", "only-one"}, "accepts 2 arg(s)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := newRootCommand()
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(tc.args)
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestResponseErrorMapping(t *testing.T) {
	if err := responseError(daemon.Response{}); err == nil || err.Error() != "daemon request failed" {
		t.Fatalf("responseError(nil error) = %v", err)
	}
	err := responseError(daemon.Response{Error: &daemon.Error{Code: daemon.ErrorPeerDenied, Message: "nope", Hint: "try again"}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("responseError(peer denied) = %v", err)
	}
	// Unknown daemon codes map to the internal error code instead of panicking.
	if err := responseError(daemon.Response{Error: &daemon.Error{Code: "made_up_code", Message: "x"}}); err == nil {
		t.Fatal("expected an error for an unknown daemon code")
	}
}

func TestWriteEnvelopeFromResponseFailure(t *testing.T) {
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	err := writeEnvelopeFromResponse(command, daemon.Response{Success: false, Error: &daemon.Error{Code: daemon.ErrorPeerDenied, Message: "denied"}})
	if err == nil {
		t.Fatal("expected a failed response to produce an error")
	}
	// A successful response with warnings converts warnings and writes the envelope.
	err = writeEnvelopeFromResponse(command, daemon.Response{Success: true, Data: map[string]any{"ok": true}, Warnings: []daemon.Warning{{Kind: "policy", Message: "warn"}}})
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("expected envelope output")
	}
}

func TestImportCurlCookiesErrors(t *testing.T) {
	command := newRootCommand()
	command.SetOut(&bytes.Buffer{})
	if err := importCurlCookies(command, "default", filepath.Join(t.TempDir(), "missing.txt")); err == nil || !strings.Contains(err.Error(), "open curl cookie jar") {
		t.Fatalf("missing jar err = %v", err)
	}
	// A line longer than the scanner buffer fails the read.
	huge := filepath.Join(t.TempDir(), "huge.txt")
	if err := os.WriteFile(huge, []byte(strings.Repeat("x", 2<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := importCurlCookies(command, "default", huge); err == nil || !strings.Contains(err.Error(), "read curl cookie jar") {
		t.Fatalf("huge line err = %v", err)
	}
}

func TestImportCurlCookiesViaDaemon(t *testing.T) {
	redirectSocketDir(t)
	session := uniqueSession()
	path, err := daemon.SocketPath(session)
	if err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, path, func([]byte) []byte {
		return []byte(`{"success":true,"data":{}}`)
	})
	jar := filepath.Join(t.TempDir(), "cookies.txt")
	content := "# Netscape HTTP Cookie File\n.example.com\tTRUE\t/\tTRUE\t1767225600\tsession\tabc123\ninvalid line\n"
	if err := os.WriteFile(jar, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newRootCommand()
	command.SetOut(&output)
	if err := importCurlCookies(command, session, jar); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "imported 1 cookie(s), skipped 2") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestMaskCookiePayloadFallbackAndMasking(t *testing.T) {
	// Payloads that cannot unmarshal into the cookie schema pass through
	// unchanged (a bare JSON string fails struct unmarshalling).
	data := "not an object"
	if got := maskCookiePayload(data, "all"); got != data {
		t.Fatalf("maskCookiePayload(fallback) = %#v", got)
	}
	payload := maskCookiePayload(map[string]any{
		"origin": "https://example.com",
		"cookies": []any{
			map[string]any{"name": "session", "value": "supersecretvalue"},
			map[string]any{"name": "theme", "value": "dark"},
		},
	}, "theme")
	typed, ok := payload.(cookieListPayload)
	if !ok {
		t.Fatalf("payload = %#v", typed)
	}
	if typed.Cookies[0].Value != maskSecret("supersecretvalue") {
		t.Fatalf("session cookie not masked: %#v", typed.Cookies[0])
	}
	if typed.Cookies[1].Value != "dark" {
		t.Fatalf("revealed cookie got masked: %#v", typed.Cookies[1])
	}
}

func TestPayloadParsersResilient(t *testing.T) {
	if got := cookieListFromResponse("garbage"); len(got.Cookies) != 0 {
		t.Fatalf("cookieListFromResponse(garbage) = %#v", got)
	}
	if got := storageListFromResponse("garbage"); got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("storageListFromResponse(garbage) = %#v, want an empty map", got)
	}
	got := storageListFromResponse(map[string]any{"origin": "https://example.com", "kind": "local", "items": map[string]any{"k": "v"}})
	if got.Origin != "https://example.com" || got.Items["k"] != "v" {
		t.Fatalf("storageListFromResponse(valid) = %#v", got)
	}
}

func TestRunDaemonErrorBranches(t *testing.T) {
	t.Run("profile resolution fails", func(t *testing.T) {
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"daemon", "--profile", filepath.Join(t.TempDir(), "missing")})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "resolve profile") {
			t.Fatalf("err = %v, want profile resolution failure", err)
		}
	})
	t.Run("invalid session name", func(t *testing.T) {
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"daemon", "--session", "bad name!"})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "invalid session") {
			t.Fatalf("err = %v, want invalid session failure", err)
		}
	})
	t.Run("invalid idle timeout env", func(t *testing.T) {
		t.Setenv("SYMBROWSE_IDLE_TIMEOUT", "bogus")
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"daemon"})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "invalid SYMBROWSE_IDLE_TIMEOUT") {
			t.Fatalf("err = %v, want idle timeout failure", err)
		}
	})
	t.Run("invalid operation timeout env", func(t *testing.T) {
		t.Setenv("SYMBROWSE_OPERATION_TIMEOUT", "bogus")
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"daemon"})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "invalid SYMBROWSE_OPERATION_TIMEOUT") {
			t.Fatalf("err = %v, want operation timeout failure", err)
		}
	})
	t.Run("state store fails when the state directory cannot be created", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		// Block the XDG state parent with a regular file so MkdirAll fails
		// deterministically on every platform (a plain missing HOME only
		// fails on some platforms, where the daemon then serves forever).
		if err := os.MkdirAll(filepath.Join(home, ".local"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".local", "state"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"daemon"})
		if err := command.Execute(); err == nil {
			t.Fatal("expected the state store to fail when the state directory cannot be created")
		}
	})
	t.Run("autosave policy rejected", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		global := filepath.Join(home, ".config", "symbrowse", "config.toml")
		if err := os.MkdirAll(filepath.Dir(global), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(global, []byte(fmt.Sprintf("state_dir = %q\n", filepath.Join(home, "state"))), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SYMBROWSE_AUTOSAVE", "bogus")
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"daemon", "--restore", "some-key"})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "autosave") {
			t.Fatalf("err = %v, want autosave policy failure", err)
		}
	})
}

func TestJournalForBrokenStateDir(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := journalFor(&config.Config{StateDir: blocked}, "s"); got != nil {
		t.Fatalf("journalFor = %v, want nil when the journal dir cannot be created", got)
	}
}
