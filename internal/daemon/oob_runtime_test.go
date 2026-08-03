package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/journal"
	"github.com/danieljustus/symaira-browse/internal/oob"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// fakeOverlayServiceEngine implements the engine plus overlay capability.
type fakeOverlayServiceEngine struct {
	fakeCookieEngine
	decision string
}

func (f *fakeOverlayServiceEngine) InstallOverlay(context.Context, engine.Page, engine.OverlayRequest) error {
	return nil
}
func (f *fakeOverlayServiceEngine) RemoveOverlay(context.Context, engine.Page) error { return nil }
func (f *fakeOverlayServiceEngine) OverlayResult(context.Context, engine.Page) (string, error) {
	return f.decision, nil
}

// testNotifier returns a notifier that never executes osascript.
func testNotifier() *oob.Notifier {
	return &oob.Notifier{
		RunCommand: func(name string, args ...string) ([]byte, error) { return nil, nil },
		Stderr:     func(string) {},
	}
}

func newOOBTestRuntime(t *testing.T, p *policy.Policy) (*NavigationRuntime, *OOBRuntime) {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeOverlayServiceEngine{}
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
	oobRuntime := NewOOBRuntime(oob.NewManager(), testNotifier(), runtime, p, policy.ModeMCP)
	return runtime, oobRuntime
}

// TestHandoffTimeoutIsStructured verifies B-44/B-45: a timed-out handoff
// returns a structured timeout result, never a hang.
func TestHandoffTimeoutIsStructured(t *testing.T) {
	_, oobRuntime := newOOBTestRuntime(t, &policy.Policy{})
	started := time.Now()
	payload, err := oobRuntime.StartHandoff(context.Background(), "default", "2FA needed", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("handoff hung for %s", elapsed)
	}
	if payload["status"] != string(oob.StatusTimeout) {
		t.Fatalf("status = %v", payload["status"])
	}
	if payload["prompt_id"] == "" {
		t.Fatal("prompt id missing")
	}
}

// TestApprovalTimeoutDenies verifies the B-46 contract: timeout => deny,
// never silent allow.
func TestApprovalTimeoutDenies(t *testing.T) {
	_, oobRuntime := newOOBTestRuntime(t, &policy.Policy{})
	allowed, prompt, err := oobRuntime.RequestApproval(context.Background(), "default", "eval", "https://x.com", policy.ClassEval, nil, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("timeout must deny")
	}
	if prompt.Status != oob.StatusTimeout {
		t.Fatalf("status = %s", prompt.Status)
	}
}

// TestApprovalCompletionAllows verifies the allow path.
func TestApprovalCompletionAllows(t *testing.T) {
	manager := oob.NewManager()
	oobRuntime := NewOOBRuntime(manager, testNotifier(), nil, &policy.Policy{}, policy.ModeMCP)
	go func() {
		time.Sleep(50 * time.Millisecond)
		active, err := manager.Active()
		if err == nil {
			_, _ = manager.Complete(active.ID, nil)
		}
	}()
	allowed, prompt, err := oobRuntime.RequestApproval(context.Background(), "default", "eval", "https://x.com", policy.ClassEval, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || prompt.Status != oob.StatusCompleted {
		t.Fatalf("allowed=%v prompt=%#v", allowed, prompt)
	}
}

// TestDecideAndConfirmGate verifies the policy gate routing: allow passes,
// deny refuses, confirm asks (and times out to deny in MCP mode).
func TestDecideAndConfirmGate(t *testing.T) {
	// deny via policy file rule
	p := &policy.Policy{Rules: []policy.Rule{{Class: policy.ClassNetworkMock, Domain: "x.com", Decision: policy.Deny}}}
	_, oobRuntime := newOOBTestRuntime(t, p)
	allowed, decision, _, err := oobRuntime.DecideAndConfirm(context.Background(), "default", "network.route", "https://x.com", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || decision != policy.Deny {
		t.Fatalf("allowed=%v decision=%s", allowed, decision)
	}
	// confirm: timeout => deny
	allowed, decision, _, err = oobRuntime.DecideAndConfirm(context.Background(), "default", "eval", "https://x.com", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || decision != policy.Confirm {
		t.Fatalf("allowed=%v decision=%s", allowed, decision)
	}
	// read: allow without prompting
	allowed, decision, _, err = oobRuntime.DecideAndConfirm(context.Background(), "default", "snapshot", "https://x.com", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || decision != policy.Allow {
		t.Fatalf("allowed=%v decision=%s", allowed, decision)
	}
}

// TestOOBStatusFrame reports no active prompt when nothing is pending.
func TestOOBStatusFrame(t *testing.T) {
	manager := oob.NewManager()
	oobRuntime := &OOBRuntime{manager: manager}
	data, _, err := oobRuntime.Handle(context.Background(), Frame{Cmd: "oob.status", Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	if !containsJSON(raw, `"active":false`) {
		t.Fatalf("status = %s", raw)
	}
}

func containsJSON(raw []byte, needle string) bool {
	return len(raw) > 0 && string(raw) != "" && jsonContains(string(raw), needle)
}

func jsonContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Watch does not touch the session: a watch loop only issues journal.show
// frames, which are read-only. This test pins that journal.show requires no
// browser launch (the runtime has no executable configured).
func TestWatchReadOnlyDoesNotTouchSession(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1})
	_, _ = registry.Ensure("default")
	// No executable configured: any browser-touching frame would fail.
	runtime := NewNavigationRuntime(registry, "", NavigationRuntimeOptions{})
	j, err := journal.New(journal.Options{Dir: t.TempDir(), Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	journalRuntime := NewJournalRuntime(j, runtime)
	data, _, err := journalRuntime.HandleJournal(context.Background(), Frame{Cmd: "journal.show", Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	if !jsonContains(string(raw), `"entries"`) {
		t.Fatalf("payload = %s", raw)
	}
}
