package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// recordedActionData builds the response payload shape the daemon emits for
// flow.record stop.
func recordedActionData(actions []any) map[string]any {
	return map[string]any{"actions": actions}
}

func TestWriteFlowDraft(t *testing.T) {
	openAction := map[string]any{
		"index": 0, "command": "open", "selector": "https://example.com",
	}
	cases := []struct {
		name     string
		response daemon.Response
		jsonFlag bool
		want     []string // substrings expected in human output
		wantErr  string
	}{
		{
			name:     "renders the recorded actions as yaml",
			response: daemon.Response{Success: true, Data: recordedActionData([]any{openAction})},
			want:     []string{"name: recorded-flow", "https://example.com"},
		},
		{
			name:     "no actions is an error",
			response: daemon.Response{Success: true, Data: recordedActionData([]any{})},
			wantErr:  "no actions recorded",
		},
		{
			name: "unrecognised actions yield no steps",
			response: daemon.Response{Success: true, Data: recordedActionData([]any{map[string]any{
				"index": 0, "command": "bogus", "value": "x",
			}})},
			wantErr: "recording contains no flow steps",
		},
		{
			name:     "json mode writes the success envelope",
			response: daemon.Response{Success: true, Data: recordedActionData([]any{openAction})},
			jsonFlag: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, buffer := newOutputCommand(t)
			if tc.jsonFlag {
				if err := command.PersistentFlags().Set("json", "true"); err != nil {
					t.Fatal(err)
				}
			}
			err := writeFlowDraft(command, tc.response)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("writeFlowDraft returned an error: %v", err)
			}
			if tc.jsonFlag {
				var envelope struct {
					Success bool           `json:"success"`
					Data    map[string]any `json:"data"`
				}
				if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil {
					t.Fatalf("output = %q: %v", buffer.String(), err)
				}
				if !envelope.Success {
					t.Fatalf("envelope = %#v: expected success", envelope)
				}
				draft, ok := envelope.Data["draft"].(string)
				if !ok || !strings.Contains(draft, "https://example.com") {
					t.Fatalf("envelope data draft = %q", envelope.Data["draft"])
				}
				return
			}
			for _, substring := range tc.want {
				if !strings.Contains(buffer.String(), substring) {
					t.Fatalf("output = %q, want containing %q", buffer.String(), substring)
				}
			}
		})
	}
}
