package session

import (
	"errors"
	"testing"
	"time"
)

func TestOwnershipLifecycleFixture(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var transitions []Transition
	manager := NewManager(Options{
		Now: func() time.Time { return now },
		Journal: func(transition Transition) error {
			transitions = append(transitions, transition)
			return nil
		},
	})

	snapshot, err := manager.Create("session-1", "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ControlState != ControlAgent || !snapshot.Active {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	if err := manager.CheckAgentAccess("session-1", "agent-a"); err != nil {
		t.Fatalf("agent access before handoff: %v", err)
	}

	snapshot, err = manager.Handoff("session-1", "agent-a", "2FA required")
	if err != nil || snapshot.ControlState != ControlAgentDelegated {
		t.Fatalf("handoff = %#v, %v", snapshot, err)
	}
	if err := manager.CheckAgentAccess("session-1", "agent-a"); err == nil {
		t.Fatal("agent access during handoff unexpectedly allowed")
	} else {
		assertHardStop(t, err, CodeSessionUserControl, true, "request explicit confirmation")
	}

	snapshot, err = manager.Claim("session-1", "human-a")
	if err != nil || snapshot.ControlState != ControlUser || snapshot.ControlID != "human-a" {
		t.Fatalf("claim = %#v, %v", snapshot, err)
	}
	beforeTakeover, err := manager.Snapshot("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Takeover("session-1", "agent-b", false); err == nil {
		t.Fatal("takeover without confirmation unexpectedly succeeded")
	} else {
		assertHardStop(t, err, CodeSessionUserControl, true, "request explicit confirmation")
	}
	afterDeniedTakeover, err := manager.Reconnect("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if afterDeniedTakeover.ControlState != beforeTakeover.ControlState || afterDeniedTakeover.ControlID != beforeTakeover.ControlID {
		t.Fatalf("denied takeover mutated ownership: before=%#v after=%#v", beforeTakeover, afterDeniedTakeover)
	}

	snapshot, err = manager.Takeover("session-1", "agent-b", true)
	if err != nil || snapshot.ControlState != ControlAgent || snapshot.ControlID != "agent-b" {
		t.Fatalf("confirmed takeover = %#v, %v", snapshot, err)
	}
	snapshot, err = manager.Complete("session-1", "agent-b", true)
	if err != nil || snapshot.Active || snapshot.Completion == nil || !snapshot.Completion.Keep {
		t.Fatalf("complete = %#v, %v", snapshot, err)
	}
	if err := manager.CheckAgentAccess("session-1", "agent-b"); err == nil {
		t.Fatal("completed session unexpectedly allowed access")
	} else {
		assertHardStop(t, err, CodeSessionInactive, false, "reopen the session")
	}

	if len(transitions) != 5 {
		t.Fatalf("transition count = %d, want 5 (%#v)", len(transitions), transitions)
	}
	wantActions := []string{"create", "handoff", "claim", "takeover", "complete"}
	for i, want := range wantActions {
		if transitions[i].Action != want {
			t.Fatalf("transition[%d].Action = %q, want %q", i, transitions[i].Action, want)
		}
	}

	// Restore/reconnect preserves the stable identity and terminal decision.
	restored := NewManager(NewManagerOptionsForTest(now))
	if err := restored.Restore(*snapshot); err != nil {
		t.Fatal(err)
	}
	reconnected, err := restored.Reconnect("session-1")
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.ID != snapshot.ID || reconnected.ControlID != snapshot.ControlID || reconnected.Completion.Keep != snapshot.Completion.Keep {
		t.Fatalf("reconnected snapshot = %#v, want %#v", reconnected, snapshot)
	}
}

func TestHandoffTimeoutIsDeniedAndJournaled(t *testing.T) {
	var transitions []Transition
	manager := NewManager(Options{
		Journal: func(transition Transition) error {
			transitions = append(transitions, transition)
			return nil
		},
	})
	if _, err := manager.Create("session-timeout", "agent-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Handoff("session-timeout", "agent-a", "CAPTCHA"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Timeout("session-timeout")
	if snapshot == nil || snapshot.Active {
		t.Fatalf("timeout snapshot = %#v", snapshot)
	}
	if err == nil {
		t.Fatal("timeout unexpectedly succeeded")
	}
	assertHardStop(t, err, CodeHandoffTimeout, true, "start a new handoff")
	if len(transitions) != 3 || transitions[2].Action != "timeout" || transitions[2].Confirmed {
		t.Fatalf("timeout transitions = %#v", transitions)
	}
}

func TestRestoreRejectsInvalidSnapshot(t *testing.T) {
	manager := NewManager(Options{})
	if err := manager.Restore(Session{ID: "session", ControlID: "agent", ControlState: "unknown", Active: true, UpdatedAt: time.Now()}); err == nil {
		t.Fatal("invalid control state accepted")
	}
}

func assertHardStop(t *testing.T, err error, code string, confirmation bool, hint string) {
	t.Helper()
	var hardStop *HardStopError
	if !errors.As(err, &hardStop) {
		t.Fatalf("error type = %T, want *HardStopError", err)
	}
	if hardStop.Code != code || hardStop.RetryableError() || hardStop.RequiresConfirmation() != confirmation || hardStop.ResumeGuidance() == "" || !contains(hardStop.ResumeGuidance(), hint) {
		t.Fatalf("hard stop = %#v", hardStop)
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}

func NewManagerOptionsForTest(now time.Time) Options {
	return Options{Now: func() time.Time { return now }}
}
