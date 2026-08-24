package main

import (
	"strings"
	"testing"
)

func TestPrintErrorEntries(t *testing.T) {
	cases := []struct {
		name       string
		data       any
		jsonOutput bool
		want       string
		wantErr    string
	}{
		{
			name: "text with stack frames",
			data: map[string]any{
				"entries": []any{
					map[string]any{
						"text":       "TypeError: x is undefined",
						"stacktrace": []any{"file.js:1:5", "file.js:2:9"},
					},
				},
			},
			want: "TypeError: x is undefined\n    at file.js:1:5\n    at file.js:2:9\n",
		},
		{
			name: "text without stack frames",
			data: map[string]any{
				"entries": []any{map[string]any{"text": "SyntaxError: bad token"}},
			},
			want: "SyntaxError: bad token\n",
		},
		{
			name: "no entries",
			data: map[string]any{"entries": []any{}},
			want: "",
		},
		{
			name: "data is not a map",
			data: "error payload",
			want: "",
		},
		{
			name:       "json mode prints the unified envelope",
			data:       map[string]any{"entries": []any{map[string]any{"text": "TypeError: x", "stacktrace": []any{"file.js:1"}}}},
			jsonOutput: true,
			want:       `{"success":true,"data":{"entries":[{"stacktrace":["file.js:1"],"text":"TypeError: x"}]}}` + "\n",
		},
		{
			name:    "write failure is returned",
			data:    map[string]any{"entries": []any{map[string]any{"text": "boom"}}},
			wantErr: "write failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, buffer := newOutputCommand(t)
			if tc.jsonOutput {
				_ = command.Flags().Set("json", "true")
			}
			if tc.wantErr != "" {
				command.SetOut(failingWriter{})
			}
			err := printErrorEntries(command, tc.data, jsonOutputFlag(command))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("printErrorEntries returned an error: %v", err)
			}
			if got := buffer.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}
