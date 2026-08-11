package main

import (
	"testing"
)

func TestJournalEntriesFromResponse(t *testing.T) {
	fullEntry := map[string]any{
		"timestamp":  "2026-08-11T10:00:00Z",
		"command":    "open",
		"risk_class": "low",
		"decider":    "auto",
		"result":     "approved",
		"reason":     "policy allowlist",
	}
	cases := []struct {
		name string
		data any
		want []journalEntryPayload
	}{
		{
			name: "parses all fields",
			data: map[string]any{"entries": []any{fullEntry}},
			want: []journalEntryPayload{{
				Timestamp: "2026-08-11T10:00:00Z",
				Command:   "open",
				RiskClass: "low",
				Decider:   "auto",
				Result:    "approved",
				Reason:    "policy allowlist",
			}},
		},
		{
			name: "optional reason is omitted",
			data: map[string]any{"entries": []any{map[string]any{
				"timestamp":  "2026-08-11T10:00:01Z",
				"command":    "fill",
				"risk_class": "medium",
				"decider":    "human",
				"result":     "denied",
			}}},
			want: []journalEntryPayload{{
				Timestamp: "2026-08-11T10:00:01Z",
				Command:   "fill",
				RiskClass: "medium",
				Decider:   "human",
				Result:    "denied",
			}},
		},
		{
			name: "multiple entries keep order",
			data: map[string]any{"entries": []any{
				map[string]any{"timestamp": "t1", "command": "open"},
				map[string]any{"timestamp": "t2", "command": "click"},
			}},
			want: []journalEntryPayload{
				{Timestamp: "t1", Command: "open"},
				{Timestamp: "t2", Command: "click"},
			},
		},
		{
			name: "missing entries key",
			data: map[string]any{"something": "else"},
			want: nil,
		},
		{
			name: "non-json data",
			data: func() {},
			want: nil,
		},
		{
			name: "nil data",
			data: nil,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := journalEntriesFromResponse(tc.data)
			if len(got) != len(tc.want) {
				t.Fatalf("entries = %#v, want %#v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("entry %d = %#v, want %#v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
