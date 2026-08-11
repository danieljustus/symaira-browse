package main

import (
	"strings"
	"testing"
)

func TestPrintConsoleEntries(t *testing.T) {
	cases := []struct {
		name       string
		data       any
		jsonOutput bool
		want       string
		wantErr    string
	}{
		{
			name: "entries rendered as type-prefixed lines",
			data: map[string]any{
				"entries": []any{
					map[string]any{"type": "log", "text": "hello"},
					map[string]any{"type": "error", "text": "boom"},
				},
			},
			want: "[log] hello\n[error] boom\n",
		},
		{
			name: "entry without a type",
			data: map[string]any{
				"entries": []any{map[string]any{"text": "no type"}},
			},
			want: "[] no type\n",
		},
		{
			name: "no entries",
			data: map[string]any{"entries": []any{}},
			want: "",
		},
		{
			name: "data is not a map",
			data: "console payload",
			want: "",
		},
		{
			name:       "json mode prints the raw payload",
			data:       map[string]any{"entries": []any{map[string]any{"type": "log", "text": "hello"}}},
			jsonOutput: true,
			want:       "{\n  \"entries\": [\n    {\n      \"text\": \"hello\",\n      \"type\": \"log\"\n    }\n  ]\n}\n",
		},
		{
			name:    "write failure is returned",
			data:    map[string]any{"entries": []any{map[string]any{"type": "log", "text": "hello"}}},
			wantErr: "write failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, buffer := newOutputCommand(t)
			if tc.wantErr != "" {
				command.SetOut(failingWriter{})
			}
			err := printConsoleEntries(command, tc.data, tc.jsonOutput)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("printConsoleEntries returned an error: %v", err)
			}
			if got := buffer.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}
