package oob

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPromptLifecycle(t *testing.T) {
	manager := NewManager()
	prompt := manager.Create(KindApproval, "Approve submit", "submit to bank.example.com", 5*time.Second)
	if prompt.Status != StatusPending || prompt.ID == "" {
		t.Fatalf("prompt = %#v", prompt)
	}
	done, err := manager.Complete(prompt.ID, map[string]any{"decision": "allow"})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusCompleted || done.Result["decision"] != "allow" {
		t.Fatalf("done = %#v", done)
	}
	// Double-finish must fail.
	if _, err := manager.Cancel(prompt.ID, "x"); err == nil {
		t.Fatal("double finish accepted")
	}
}

func TestWaitTimeoutDenies(t *testing.T) {
	manager := NewManager()
	prompt := manager.Create(KindApproval, "Approve", "reason", 100*time.Millisecond)
	allowed, result, err := manager.ResolveWait(context.Background(), prompt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("timeout must never allow")
	}
	if result.Status != StatusTimeout {
		t.Fatalf("status = %s", result.Status)
	}
}

func TestWaitCompletionAllows(t *testing.T) {
	manager := NewManager()
	prompt := manager.Create(KindHandoff, "Handoff", "2FA needed", time.Minute)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = manager.Complete(prompt.ID, nil)
	}()
	allowed, result, err := manager.ResolveWait(context.Background(), prompt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed || result.Status != StatusCompleted {
		t.Fatalf("allowed=%v result=%#v", allowed, result)
	}
}

func TestWaitCancelDenies(t *testing.T) {
	manager := NewManager()
	prompt := manager.Create(KindHandoff, "Handoff", "x", time.Minute)
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = manager.Cancel(prompt.ID, "user aborted")
	}()
	allowed, result, err := manager.ResolveWait(context.Background(), prompt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || result.Status != StatusCancelled {
		t.Fatalf("allowed=%v result=%#v", allowed, result)
	}
}

func TestActiveReturnsNewestPending(t *testing.T) {
	manager := NewManager()
	first := manager.Create(KindWatch, "Watch", "", 0)
	second := manager.Create(KindApproval, "Approve", "", 0)
	active, err := manager.Active()
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != second.ID {
		t.Fatalf("active = %s, want %s", active.ID, second.ID)
	}
	_, _ = manager.Complete(second.ID, nil)
	active, err = manager.Active()
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID {
		t.Fatalf("active = %s, want %s", active.ID, first.ID)
	}
}

func TestWaitContextCancel(t *testing.T) {
	manager := NewManager()
	prompt := manager.Create(KindHandoff, "Handoff", "", time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := manager.Wait(ctx, prompt.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestNotifierFallbackWithoutMacOS(t *testing.T) {
	var stderr string
	notifier := &Notifier{
		RunCommand: func(name string, args ...string) ([]byte, error) { return nil, errors.New("no osascript") },
		Stderr:     func(message string) { stderr = message },
	}
	prompt := &Prompt{ID: "oob-1", Kind: KindHandoff, Title: "Handoff"}
	if runtime.GOOS != "darwin" {
		// On non-macOS the notifier falls back to stderr and must succeed.
		if err := notifier.Notify(prompt); err != nil {
			t.Fatalf("fallback notify failed: %v", err)
		}
		if !strings.Contains(stderr, "oob-1") {
			t.Fatalf("stderr = %q", stderr)
		}
	} else {
		// On macOS the injected failing command surfaces as an error that
		// must never break the blocking wait.
		if err := notifier.Notify(prompt); err == nil {
			t.Fatal("expected notification failure")
		}
	}
}
