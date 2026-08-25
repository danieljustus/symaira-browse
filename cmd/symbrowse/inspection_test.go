package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

func TestWriteInspectionResponse(t *testing.T) {
	cases := []struct {
		name       string
		response   daemon.Response
		stateCheck bool
		jsonFlag   bool
		want       string
		wantErr    string
	}{
		{
			name: "string value",
			response: daemon.Response{Success: true, Data: map[string]any{
				"kind": "text", "value": "hello",
			}},
			want: "hello\n",
		},
		{
			name: "numeric value",
			response: daemon.Response{Success: true, Data: map[string]any{
				"kind": "count", "value": 42,
			}},
			want: "42\n",
		},
		{
			name: "state check passes",
			response: daemon.Response{Success: true, Data: map[string]any{
				"kind": "visible", "value": true,
			}},
			stateCheck: true,
			want:       "true\n",
		},
		{
			name: "state check fails on false",
			response: daemon.Response{Success: true, Data: map[string]any{
				"kind": "visible", "value": false,
			}},
			stateCheck: true,
			wantErr:    "state visible is false",
		},
		{
			name: "state check rejects non-boolean",
			response: daemon.Response{Success: true, Data: map[string]any{
				"kind": "visible", "value": "yes",
			}},
			stateCheck: true,
			wantErr:    "state inspection returned a non-boolean value",
		},
		{
			name:     "failed response becomes an error",
			response: daemon.Response{Success: false, Error: daemon.NewError("not_found", "element @e7 no longer exists")},
			wantErr:  "element @e7 no longer exists",
		},
		{
			name:     "undecodable data becomes an error",
			response: daemon.Response{Success: true, Data: "not an object"},
			wantErr:  "decode inspection result",
		},
		{
			name:     "unmarshalable data becomes an error",
			response: daemon.Response{Success: true, Data: map[string]any{"kind": "text", "value": make(chan int)}},
			wantErr:  "unsupported type",
		},
		{
			name: "json mode writes the success envelope",
			response: daemon.Response{Success: true, Data: map[string]any{
				"kind": "text", "value": "hello",
			}},
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
			err := writeInspectionResponse(command, tc.response, tc.stateCheck)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("writeInspectionResponse returned an error: %v", err)
			}
			if tc.jsonFlag {
				var envelope struct {
					Success bool           `json:"success"`
					Data    map[string]any `json:"data"`
				}
				if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil {
					t.Fatalf("output = %q: %v", buffer.String(), err)
				}
				if !envelope.Success || envelope.Data["kind"] != "text" || envelope.Data["value"] != "hello" {
					t.Fatalf("envelope = %#v", envelope)
				}
				return
			}
			if got := buffer.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}
