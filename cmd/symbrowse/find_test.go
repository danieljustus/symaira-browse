package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

func TestWriteFindResponse(t *testing.T) {
	cases := []struct {
		name     string
		response daemon.Response
		jsonFlag bool
		want     string
		wantErr  string
	}{
		{
			name: "text action prints the value",
			response: daemon.Response{Success: true, Data: map[string]any{
				"action": "text", "value": "hello", "ref": "e7",
			}},
			want: "hello\n",
		},
		{
			name: "non-text action prints the element ref",
			response: daemon.Response{Success: true, Data: map[string]any{
				"action": "click", "ref": "e7",
			}},
			want: "@e7\n",
		},
		{
			name:     "failed response becomes an error",
			response: daemon.Response{Success: false, Error: daemon.NewError("not_found", `find label "x" matched no elements`)},
			wantErr:  "matched no elements",
		},
		{
			name:     "undecodable data becomes an error",
			response: daemon.Response{Success: true, Data: "not an object"},
			wantErr:  "decode find result",
		},
		{
			name:     "unmarshalable data becomes an error",
			response: daemon.Response{Success: true, Data: map[string]any{"action": "text", "value": make(chan int)}},
			wantErr:  "unsupported type",
		},
		{
			name: "json mode writes the success envelope",
			response: daemon.Response{Success: true, Data: map[string]any{
				"action": "text", "value": "hello", "ref": "e7",
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
			err := writeFindResponse(command, tc.response)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("writeFindResponse returned an error: %v", err)
			}
			if tc.jsonFlag {
				var envelope struct {
					Success bool           `json:"success"`
					Data    map[string]any `json:"data"`
				}
				if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil {
					t.Fatalf("output = %q: %v", buffer.String(), err)
				}
				if !envelope.Success || envelope.Data["action"] != "text" || envelope.Data["value"] != "hello" {
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
