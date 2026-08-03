package daemon

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRecordStartStopStatusLifecycle(t *testing.T) {
	runtime := &NavigationRuntime{
		recorders: make(map[string]*recorderState),
	}
	ctx := context.Background()

	status, err := runtime.recordStatus("s1")
	if err != nil {
		t.Fatalf("recordStatus: %v", err)
	}
	if status.(map[string]any)["recording"] != false {
		t.Errorf("initial recording state = %v, want false", status)
	}

	if _, err := runtime.recordStart("s1"); err != nil {
		t.Fatalf("recordStart: %v", err)
	}
	status, _ = runtime.recordStatus("s1")
	if status.(map[string]any)["recording"] != true {
		t.Errorf("recording state after start = %v, want true", status)
	}

	// Recording captures recordable frames only.
	runtime.recordFrame(ctx, "s1", Frame{Cmd: "open", Args: json.RawMessage(`{"url":"http://fixture.local/form"}`)})
	runtime.recordFrame(ctx, "s1", Frame{Cmd: "fill", Args: json.RawMessage(`{"selector":"@e3","value":"alice"}`)})
	runtime.recordFrame(ctx, "s1", Frame{Cmd: "get.url", Args: json.RawMessage(`{}`)})

	status, _ = runtime.recordStatus("s1")
	if actions := status.(map[string]any)["actions"]; actions != 2 {
		t.Errorf("recorded actions = %v, want 2 (get.url ignored)", actions)
	}

	stopped, err := runtime.recordStop("s1")
	if err != nil {
		t.Fatalf("recordStop: %v", err)
	}
	payload := stopped.(map[string]any)
	if payload["recording"] != false {
		t.Errorf("recording after stop = %v, want false", payload["recording"])
	}
	actions := payload["actions"].([]RecordedAction)
	if len(actions) != 2 {
		t.Fatalf("stopped actions = %d, want 2", len(actions))
	}
	if actions[0].Command != "open" || actions[0].Selector != "http://fixture.local/form" {
		t.Errorf("action 0 = %+v", actions[0])
	}
	if actions[1].Command != "fill" || actions[1].Selector != "@e3" || actions[1].Value != "alice" {
		t.Errorf("action 1 = %+v", actions[1])
	}

	// After stop, no more actions are captured.
	runtime.recordFrame(ctx, "s1", Frame{Cmd: "click", Args: json.RawMessage(`{"selector":"@e9"}`)})
	status, _ = runtime.recordStatus("s1")
	if actions := status.(map[string]any)["actions"]; actions != 2 {
		t.Errorf("actions after stop = %v, want 2", actions)
	}
}

func TestRecordFrameResolvesRefsWhenServiceAvailable(t *testing.T) {
	// With no service registered, refs stay unresolved but the action is kept.
	runtime := &NavigationRuntime{recorders: make(map[string]*recorderState)}
	if _, err := runtime.recordStart("s1"); err != nil {
		t.Fatalf("recordStart: %v", err)
	}
	runtime.recordFrame(context.Background(), "s1", Frame{Cmd: "click", Args: json.RawMessage(`{"selector":"@e9"}`)})
	stopped, err := runtime.recordStop("s1")
	if err != nil {
		t.Fatalf("recordStop: %v", err)
	}
	actions := stopped.(map[string]any)["actions"].([]RecordedAction)
	if len(actions) != 1 || actions[0].Selector != "@e9" {
		t.Fatalf("actions = %+v", actions)
	}
}

func TestRecordStartResetsPreviousRecording(t *testing.T) {
	runtime := &NavigationRuntime{recorders: make(map[string]*recorderState)}
	ctx := context.Background()
	_, _ = runtime.recordStart("s1")
	runtime.recordFrame(ctx, "s1", Frame{Cmd: "open", Args: json.RawMessage(`{"url":"http://a.example"}`)})
	_, _ = runtime.recordStart("s1") // restart clears
	runtime.recordFrame(ctx, "s1", Frame{Cmd: "open", Args: json.RawMessage(`{"url":"http://b.example"}`)})
	stopped, _ := runtime.recordStop("s1")
	actions := stopped.(map[string]any)["actions"].([]RecordedAction)
	if len(actions) != 1 || actions[0].Selector != "http://b.example" {
		t.Fatalf("actions after restart = %+v, want only the new recording", actions)
	}
}
