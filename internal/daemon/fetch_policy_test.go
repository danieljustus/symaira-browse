package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

func fetchPolicyError(t *testing.T, runtime *FetchRuntime, command string, args map[string]any) *Error {
	t.Helper()
	_, _, err := runtime.Handle(context.Background(), Frame{Cmd: command, Args: marshalArgsForTest(args)})
	if err == nil {
		t.Fatal("expected fetch policy error, got success")
	}
	var daemonErr *Error
	if !errors.As(err, &daemonErr) {
		t.Fatalf("error is %T, want *daemon.Error: %v", err, err)
	}
	return daemonErr
}

func TestFetchRuntimeAllowlistDeniesOffListURL(t *testing.T) {
	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowedDomains: []string{"allowed.example"},
		AllowPrivate:   true,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	server := fetchTestServer(t)
	defer server.Close()

	daemonErr := fetchPolicyError(t, runtime, "fetch.url", map[string]any{"url": server.URL})
	if daemonErr.Code != ErrorPeerDenied {
		t.Fatalf("code = %q, want %q", daemonErr.Code, ErrorPeerDenied)
	}
	if daemonErr.Retryable == nil || *daemonErr.Retryable {
		t.Fatal("an off-list URL must not be reported retryable")
	}
}

func TestFetchRuntimeAllowlistDeniesBlockedRedirect(t *testing.T) {
	var blockedTarget string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, blockedTarget, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("should not be fetched"))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	blockedTarget = "http://localhost:" + parsed.Port() + "/blocked"

	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowedDomains: []string{"127.0.0.1"},
		AllowPrivate:   true,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	daemonErr := fetchPolicyError(t, runtime, "fetch.url", map[string]any{"url": server.URL + "/start"})
	if daemonErr.Code != ErrorPeerDenied {
		t.Fatalf("code = %q, want %q", daemonErr.Code, ErrorPeerDenied)
	}
	if !strings.Contains(daemonErr.Message, "fetch") {
		t.Fatalf("redirect error lost fetch context: %q", daemonErr.Message)
	}
}

func TestFetchRuntimeBatchChecksEachURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body><article><h1>Allowed</h1><p>" + strings.Repeat("content ", 100) + "</p></article></body></html>"))
	}))
	defer server.Close()

	runtime, err := NewFetchRuntime(FetchRuntimeOptions{
		AllowedDomains: []string{"127.0.0.1"},
		AllowPrivate:   true,
	})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = runtime.Close() }()

	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd: "fetch.batch",
		Args: marshalArgsForTest(map[string]any{
			"urls": []string{server.URL + "/allowed", "https://outside.example/blocked"},
		}),
	})
	if err != nil {
		t.Fatalf("fetch.batch: %v", err)
	}
	entries, ok := data.([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("batch result = %#v, want two entries", data)
	}
	first, _ := entries[0].(map[string]any)
	if first["ok"] != true {
		t.Fatalf("allowed entry = %#v, want success", first)
	}
	second, _ := entries[1].(map[string]any)
	if second["ok"] != false || second["code"] != ErrorPeerDenied {
		t.Fatalf("blocked entry = %#v, want peer_denied failure", second)
	}
}

func TestFetchCommandsHaveReadRiskClass(t *testing.T) {
	for _, command := range []string{"fetch.url", "fetch.batch", "wayback.snapshots"} {
		class, err := policy.Classify(command)
		if err != nil {
			t.Fatalf("Classify(%q): %v", command, err)
		}
		if class != policy.ClassRead {
			t.Errorf("Classify(%q) = %q, want %q", command, class, policy.ClassRead)
		}
	}
}

func TestFetchRuntimeJournalUsesFetchHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body><article><h1>Journaled fetch</h1><p>" + strings.Repeat("content ", 100) + "</p></article></body></html>"))
	}))
	defer server.Close()

	fetchRuntime, err := NewFetchRuntime(FetchRuntimeOptions{AllowPrivate: true})
	if err != nil {
		t.Fatalf("NewFetchRuntime: %v", err)
	}
	defer func() { _ = fetchRuntime.Close() }()

	j, err := journal.New(journal.Options{Dir: t.TempDir(), Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	nav := NewNavigationRuntime(NewSessionRegistry(SessionRegistryOptions{}), "", NavigationRuntimeOptions{})
	journalRuntime := NewJournalRuntime(j, nav)

	args, _ := json.Marshal(map[string]any{"url": server.URL + "/journal"})
	if _, _, err := journalRuntime.HandleFuncWithDecider(context.Background(), Frame{
		Cmd:     "fetch.url",
		Session: "default",
		Args:    args,
	}, "policy", fetchRuntime.Handle); err != nil {
		t.Fatalf("journaled fetch: %v", err)
	}

	entries, err := j.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Command != "fetch.url" || entry.RiskClass != string(policy.ClassRead) || entry.Decider != "policy" || entry.Result != "ok" {
		t.Fatalf("journal entry = %+v", entry)
	}
	if entry.URLBefore != server.URL+"/journal" || entry.URLAfter != server.URL+"/journal" {
		t.Fatalf("journal URLs = before %q after %q, want fetch target", entry.URLBefore, entry.URLAfter)
	}
}
