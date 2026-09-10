package safaribidi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type rustSafariBidiFixture struct {
	SchemaVersion  int            `json:"schema_version"`
	EngineKind     string         `json:"engine_kind"`
	LaunchMode     string         `json:"launch_mode"`
	SessionRequest map[string]any `json:"session_request"`
	Capabilities   interface{}    `json:"capabilities"`
	Policy         map[string]any `json:"policy"`
	Protocol       map[string]any `json:"protocol"`
	Lifecycle      map[string]any `json:"lifecycle"`
	Unsupported    []string       `json:"unsupported"`
}

// TestGenerateRustPortSafariBidiFixture freezes BiDi lifecycle and capability
// evidence without starting safaridriver or opening a WebSocket.
func TestGenerateRustPortSafariBidiFixture(t *testing.T) {
	output := os.Getenv("RUST_PORT_SAFARI_BIDI_OUT")
	if output == "" {
		t.Skip("set RUST_PORT_SAFARI_BIDI_OUT to generate the Safari BiDi fixture")
	}
	loopbackOK := requireLoopback("ws://127.0.0.1:1234/session/fixture") == nil
	foreignRejected := requireLoopback("ws://192.0.2.1:1234/session/fixture") != nil
	fixture := rustSafariBidiFixture{
		SchemaVersion: 1,
		EngineKind:    "safari-bidi",
		LaunchMode:    "launch",
		SessionRequest: map[string]any{
			"capabilities": map[string]any{"alwaysMatch": map[string]any{
				"browserName":                     "safari",
				"webSocketUrl":                    true,
				"safari:experimentalWebSocketUrl": true,
			}},
		},
		Capabilities: New().Capabilities(),
		Policy: map[string]any{
			"direct_navigation_checked":              true,
			"redirects_and_subresources_intercepted": false,
			"denials_before_transport":               true,
		},
		Protocol: map[string]any{
			"commands":                       []string{"browsingContext.getTree", "browsingContext.navigate", "script.evaluate"},
			"typed_protocol_errors":          true,
			"boolean_websocket_url_rejected": true,
			"loopback_endpoint_accepted":     loopbackOK,
			"foreign_socket_rejected":        foreignRejected,
		},
		Lifecycle: map[string]any{
			"session_delete_before_driver_stop": true,
			"transport_closed":                  true,
			"owned_process_killed_and_reaped":   true,
			"close_is_idempotent":               true,
		},
		Unsupported: []string{"screenshot", "input", "network interception", "storage", "runtime events"},
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
