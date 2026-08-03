package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/oob"
	"github.com/danieljustus/symaira-browse/internal/policy"
	"github.com/danieljustus/symaira-browse/internal/state"
)

// ---------------------------------------------------------------------------
// JournalRuntime (issue B-41): action journaling wrapper.
// ---------------------------------------------------------------------------

func newJournalTestRuntime(t *testing.T, j *journal.Journal) *JournalRuntime {
	t.Helper()
	nav, _ := newCookieRuntime(t)
	return NewJournalRuntime(j, nav)
}

func TestJournalHandleWithDeciderAppendsEntry(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.New(journal.Options{Dir: dir, Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r := newJournalTestRuntime(t, j)
	raw, _ := json.Marshal(map[string]any{})
	data, warnings, err := r.HandleWithDecider(context.Background(), Frame{Cmd: "snapshot", Session: "default", Args: raw}, "guard")
	if err != nil {
		t.Fatal(err)
	}
	if data == nil {
		t.Fatal("expected snapshot payload")
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	entries, err := j.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Command != "snapshot" || entries[0].Result != "ok" || entries[0].Decider != "guard" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[0].RiskClass == "" {
		t.Fatal("risk class missing")
	}
}

func TestJournalHandleWithDeciderRecordsErrors(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.New(journal.Options{Dir: dir, Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r := newJournalTestRuntime(t, j)
	raw, _ := json.Marshal(map[string]any{})
	_, _, err = r.HandleWithDecider(context.Background(), Frame{Cmd: "definitely.not.a.command", Session: "default", Args: raw}, "policy")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
	entries, readErr := j.Read()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if !strings.HasPrefix(entries[0].Result, "error:") {
		t.Fatalf("result = %q, want error prefix", entries[0].Result)
	}
}

func TestJournalHandlePassesThroughWithoutJournal(t *testing.T) {
	r := newJournalTestRuntime(t, nil)
	raw, _ := json.Marshal(map[string]any{})
	_, _, err := r.Handle(context.Background(), Frame{Cmd: "snapshot", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournalHandleOOBJournalsHumanDecider(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.New(journal.Options{Dir: dir, Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r := newJournalTestRuntime(t, j)
	raw, _ := json.Marshal(map[string]any{})
	handler := func(ctx context.Context, frame Frame) (any, []Warning, error) {
		return map[string]any{"granted": true}, nil, nil
	}
	data, _, err := r.HandleOOB(context.Background(), Frame{Cmd: "handoff", Session: "default", Args: raw}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if data.(map[string]any)["granted"] != true {
		t.Fatalf("data = %v", data)
	}
	entries, _ := j.Read()
	if len(entries) != 1 || entries[0].Decider != "human" {
		t.Fatalf("entries = %+v, want one human-decided entry", entries)
	}
}

func TestJournalHandleOOBPassesThroughWithoutJournal(t *testing.T) {
	r := newJournalTestRuntime(t, nil)
	called := false
	raw, _ := json.Marshal(map[string]any{})
	_, _, err := r.HandleOOB(context.Background(), Frame{Cmd: "handoff", Session: "default", Args: raw}, func(ctx context.Context, frame Frame) (any, []Warning, error) {
		called = true
		return nil, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("handler not called")
	}
}

func TestJournalHandleJournalTailAndShow(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.New(journal.Options{Dir: dir, Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r := newJournalTestRuntime(t, j)
	raw, _ := json.Marshal(map[string]any{})
	_, _, _ = r.HandleWithDecider(context.Background(), Frame{Cmd: "snapshot", Session: "default", Args: raw}, "policy")

	tailRaw, _ := json.Marshal(map[string]any{"lines": 10})
	tail, _, err := r.HandleJournal(context.Background(), Frame{Cmd: "journal.tail", Session: "default", Args: tailRaw})
	if err != nil {
		t.Fatal(err)
	}
	payload := tail.(map[string]any)
	if payload["session"] != "default" || len(payload["entries"].([]journal.Entry)) != 1 {
		t.Fatalf("tail payload = %+v", payload)
	}

	show, _, err := r.HandleJournal(context.Background(), Frame{Cmd: "journal.show", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	showPayload := show.(map[string]any)
	if len(showPayload["entries"].([]journal.Entry)) != 1 {
		t.Fatalf("show payload = %+v", showPayload)
	}
}

func TestJournalHandleJournalDisabled(t *testing.T) {
	r := newJournalTestRuntime(t, nil)
	raw, _ := json.Marshal(map[string]any{})
	if _, _, err := r.HandleJournal(context.Background(), Frame{Cmd: "journal.tail", Session: "default", Args: raw}); err == nil {
		t.Fatal("expected error when journal disabled")
	}
}

func TestJournalHandleJournalUnknownCommand(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.New(journal.Options{Dir: dir, Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	r := newJournalTestRuntime(t, j)
	raw, _ := json.Marshal(map[string]any{})
	if _, _, err := r.HandleJournal(context.Background(), Frame{Cmd: "journal.bogus", Session: "default", Args: raw}); err == nil {
		t.Fatal("expected error for unknown journal command")
	}
}

func TestJournalCurrentURLWithoutBrowserIsEmpty(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.New(journal.Options{Dir: dir, Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	nav := &NavigationRuntime{
		tabs:      make(map[string][]*sessionTab),
		activeTab: make(map[string]int),
	}
	r := NewJournalRuntime(j, nav)
	if url := r.currentURL(context.Background(), "default"); url != "" {
		t.Fatalf("url = %q, want empty", url)
	}
}

// ---------------------------------------------------------------------------
// StateRuntime (issue B-35/B-36): named state persistence bridge.
// ---------------------------------------------------------------------------

func newStateTestRuntime(t *testing.T) (*StateRuntime, *NavigationRuntime) {
	t.Helper()
	store, err := state.NewStore(state.StoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	nav, _ := newCookieRuntime(t)
	return NewStateRuntime(store, nav), nav
}

func TestStateSaveRejectsInvalidName(t *testing.T) {
	r, _ := newStateTestRuntime(t)
	_, err := r.Save(context.Background(), "default", "../evil")
	if err == nil {
		t.Fatal("expected name validation error")
	}
}

func TestStateLoadRejectsInvalidNameAndMissingState(t *testing.T) {
	r, _ := newStateTestRuntime(t)
	if _, _, err := r.Load(context.Background(), "default", "../evil"); err == nil {
		t.Fatal("expected name validation error")
	}
	if _, _, err := r.Load(context.Background(), "default", "missing"); err == nil {
		t.Fatal("expected missing-state error")
	}
}

func TestStateHandleListShowClearClean(t *testing.T) {
	r, _ := newStateTestRuntime(t)
	raw, _ := json.Marshal(map[string]any{})

	list, _, err := r.Handle(context.Background(), Frame{Cmd: "state.list", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	if list.(map[string]any)["schema_version"] != state.SchemaVersion {
		t.Fatalf("list payload = %+v", list)
	}

	showRaw, _ := json.Marshal(map[string]any{"name": "missing"})
	if _, _, err := r.Handle(context.Background(), Frame{Cmd: "state.show", Session: "default", Args: showRaw}); err == nil {
		t.Fatal("expected error for missing state")
	}

	clearRaw, _ := json.Marshal(map[string]any{"name": "missing"})
	cleared, _, err := r.Handle(context.Background(), Frame{Cmd: "state.clear", Session: "default", Args: clearRaw})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.(map[string]any)["cleared"] != "missing" {
		t.Fatalf("cleared payload = %+v", cleared)
	}

	clean, _, err := r.Handle(context.Background(), Frame{Cmd: "state.clean", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := clean.(map[string]any)["removed"]; !ok {
		t.Fatalf("clean payload = %+v", clean)
	}
}

func TestStateHandleUnknownCommand(t *testing.T) {
	r, _ := newStateTestRuntime(t)
	raw, _ := json.Marshal(map[string]any{})
	if _, _, err := r.Handle(context.Background(), Frame{Cmd: "state.bogus", Session: "default", Args: raw}); err == nil {
		t.Fatal("expected unknown-command error")
	}
}

func TestStateReportExpiredWithEmptyStore(t *testing.T) {
	r, _ := newStateTestRuntime(t)
	r.ReportExpired() // must not panic
}

func TestAutosaveConfigValidate(t *testing.T) {
	valid := &AutosaveConfig{Policy: "", Interval: time.Minute}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if valid.Policy != AutosaveAuto {
		t.Fatalf("policy = %q, want auto default", valid.Policy)
	}
	if err := (&AutosaveConfig{Policy: "bogus"}).Validate(); err == nil {
		t.Fatal("expected invalid policy error")
	}
	if err := (&AutosaveConfig{Policy: AutosaveAuto, Interval: -1}).Validate(); err == nil {
		t.Fatal("expected negative interval error")
	}
}

// ---------------------------------------------------------------------------
// AuthRuntime (issue B-41): vault-backed login frame.
// ---------------------------------------------------------------------------

func TestAuthHandleLoginFrame(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAuthEngine{}
	runtime := &NavigationRuntime{
		registry:   registry,
		executable: "/fake/chrome",
		tabs:       make(map[string][]*sessionTab),
		activeTab:  make(map[string]int),
		engines:    make(map[string]engine.Engine),
	}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	runtime.tabs["default"] = []*sessionTab{{Label: "t1", Service: service, Page: engine.Page{ID: "page"}}}
	runtime.activeTab["default"] = 0
	runtime.engines["default"] = fake

	vault := &VaultResolver{
		LookPath: func(string) (string, error) { return "/bin/symvault", nil },
		Run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(`{"username":"ada","password":"hunter2-secret"}`), nil
		},
	}
	auth := NewAuthRuntime(runtime, vault)
	raw, _ := json.Marshal(map[string]any{"entry": "myapp"})
	result, _, err := auth.Handle(context.Background(), Frame{Cmd: "auth.login", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	payload := result.(LoginResult)
	if payload.Status != "logged_in" {
		t.Fatalf("payload = %+v", payload)
	}
	// The secret must never leak into the response.
	blob, _ := json.Marshal(payload)
	if strings.Contains(string(blob), "hunter2-secret") {
		t.Fatal("password leaked into auth.login response")
	}
}

func TestAuthHandleMissingEntryAndUnknownCommand(t *testing.T) {
	auth := NewAuthRuntime(&NavigationRuntime{}, &VaultResolver{})
	raw, _ := json.Marshal(map[string]any{})
	if _, _, err := auth.Handle(context.Background(), Frame{Cmd: "auth.login", Session: "default", Args: raw}); err == nil {
		t.Fatal("expected missing-entry error")
	}
	if _, _, err := auth.Handle(context.Background(), Frame{Cmd: "auth.bogus", Session: "default", Args: raw}); err == nil {
		t.Fatal("expected unknown-command error")
	}
}

// ---------------------------------------------------------------------------
// OOBRuntime approval gate (issue B-44/B-45/B-46).
// ---------------------------------------------------------------------------

func newOOBGateRuntime(t *testing.T, p *policy.Policy) *OOBRuntime {
	t.Helper()
	nav, _ := newCookieRuntime(t)
	return NewOOBRuntime(oob.NewManager(), testNotifier(), nav, p, policy.ModeTTY)
}

func TestDecideAndConfirmPolicyAllow(t *testing.T) {
	r := newOOBGateRuntime(t, &policy.Policy{})
	allowed, decision, decider, err := r.DecideAndConfirm(context.Background(), "default", "snapshot", "https://example.com", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || decision != policy.Allow || decider != "policy" {
		t.Fatalf("allowed=%v decision=%q decider=%q", allowed, decision, decider)
	}
}

func TestDecideAndConfirmPolicyDeny(t *testing.T) {
	// eval is a Confirm-class command in TTY mode: without a human
	// completing the approval within the timeout the gate must deny
	// (timeout => deny, issue B-44).
	r := newOOBGateRuntime(t, &policy.Policy{})
	allowed, decision, _, err := r.DecideAndConfirm(context.Background(), "default", "eval", "https://example.com", 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatalf("eval must be denied when the approval times out, decision=%q", decision)
	}
}

func TestDecideAndConfirmUnknownCommand(t *testing.T) {
	r := newOOBGateRuntime(t, &policy.Policy{})
	if _, _, _, err := r.DecideAndConfirm(context.Background(), "default", "totally.unknown", "https://example.com", time.Second); err == nil {
		t.Fatal("expected classification error")
	}
}

func TestDecideAndConfirmGuardDecider(t *testing.T) {
	r := newOOBGateRuntime(t, &policy.Policy{})
	r.SetDecider(func(ctx context.Context, command, url string, mode policy.Mode, warnings []string) (policy.Decision, string, string, error) {
		return policy.Allow, "guard", "test guard allowed", nil
	})
	allowed, decision, decider, err := r.DecideAndConfirm(context.Background(), "default", "open", "https://example.com", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || decision != policy.Allow || decider != "guard" {
		t.Fatalf("allowed=%v decision=%q decider=%q", allowed, decision, decider)
	}
}

func TestOOBHandleStatusCompleteCancel(t *testing.T) {
	r := newOOBGateRuntime(t, &policy.Policy{})
	raw, _ := json.Marshal(map[string]any{})

	status, _, err := r.Handle(context.Background(), Frame{Cmd: "oob.status", Session: "default", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	if status.(map[string]any)["active"] != false {
		t.Fatalf("status payload = %+v", status)
	}

	// Complete and cancel on a missing prompt must error, not panic.
	completeRaw, _ := json.Marshal(map[string]any{"id": "nope"})
	if _, _, err := r.Handle(context.Background(), Frame{Cmd: "oob.complete", Session: "default", Args: completeRaw}); err == nil {
		t.Fatal("expected error completing missing prompt")
	}
	cancelRaw, _ := json.Marshal(map[string]any{"id": "nope"})
	if _, _, err := r.Handle(context.Background(), Frame{Cmd: "oob.cancel", Session: "default", Args: cancelRaw}); err == nil {
		t.Fatal("expected error cancelling missing prompt")
	}
	if _, _, err := r.Handle(context.Background(), Frame{Cmd: "oob.bogus", Session: "default", Args: raw}); err == nil {
		t.Fatal("expected unknown-command error")
	}
}

// ---------------------------------------------------------------------------
// Protocol helpers.
// ---------------------------------------------------------------------------

func TestErrorKindStructuredAndFallback(t *testing.T) {
	if kind := errorKind(&Error{Code: "network_request_not_found", Message: "x"}); kind != "network_request_not_found" {
		t.Fatalf("kind = %q", kind)
	}
	if kind := errorKind(context.DeadlineExceeded); kind != "failed" {
		t.Fatalf("kind = %q, want failed", kind)
	}
}

func TestHostOfURLParsing(t *testing.T) {
	cases := map[string]string{
		"https://example.com/path":   "example.com",
		"https://example.com:8443/x": "example.com",
		"http://sub.example.org":     "sub.example.org",
		"no-scheme":                  "no-scheme",
	}
	for raw, want := range cases {
		if got := hostOfURL(raw); got != want {
			t.Errorf("hostOfURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
