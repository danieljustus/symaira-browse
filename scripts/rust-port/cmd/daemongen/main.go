package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

type fixture struct {
	Frame    string `json:"frame"`
	Success  string `json:"success"`
	Failure  string `json:"failure"`
	Warning  string `json:"warning"`
	BadFrame string `json:"bad_frame_error"`
}

func main() {
	output := flag.String("output", "testdata/port/daemon/protocol.json", "fixture path")
	check := flag.Bool("check", false, "compare instead of writing")
	flag.Parse()
	maxTokens := 123
	retryable := true
	confirm := false
	frame := daemon.Frame{Cmd: "fetch.url", Args: json.RawMessage(`{"url":"https://example.com"}`), Session: "fixture", RequestID: "request-1", MaxTokens: &maxTokens, RetrievalSurface: "mcp"}
	success := daemon.SuccessResponse(map[string]any{"ok": true}, []daemon.Warning{{Kind: "policy", Severity: "warning", Message: "fixture", Ref: "e1", Excerpt: "evidence"}})
	failure := daemon.Response{Success: false, Error: &daemon.Error{Code: daemon.ErrorOperationFailed, Message: "failed", Hint: "retry", Details: map[string]any{"field": "value"}, Retryable: &retryable, RequiresUserConfirmation: &confirm, ResumeHint: "resume"}}
	warning := daemon.Warning{Kind: "policy", Message: "message"}
	_, badErr := daemon.DecodeFrame([]byte(`{"session":"fixture"}`))
	value := fixture{Frame: marshal(frame), Success: marshal(success), Failure: marshal(failure), Warning: marshal(warning), BadFrame: badErr.Error()}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	encoded = append(encoded, '\n')
	if *check {
		current, err := os.ReadFile(*output)
		if err != nil || string(current) != string(encoded) {
			fmt.Fprintln(os.Stderr, "daemon protocol fixture is stale")
			os.Exit(1)
		}
		fmt.Println("PASS daemon protocol fixture")
		return
	}
	if err := os.MkdirAll("testdata/port/daemon", 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(*output, encoded, 0o644); err != nil {
		panic(err)
	}
}

func marshal(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
