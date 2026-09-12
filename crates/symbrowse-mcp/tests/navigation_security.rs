#![deny(unsafe_code)]
#![cfg(unix)]
#[path = "../../symbrowse-daemon/tests/support/navigation_security.rs"]
mod support;
use serde_json::{Value, json};
use std::{io::Cursor, sync::atomic::Ordering};
use support::*;
use symbrowse_mcp::{ServeOptions, serve_stdio};

#[test]
fn mcp_admission_and_later_warning_frames_are_clean() {
    let harness = Harness::new("mcp");
    // Exercise the raw daemon path first, then the real MCP socket proxy.
    assert_eq!(harness.request("open", SAFE_URL)["success"], true);
    let options = ServeOptions {
        session: "boundary".into(),
        profiles: "all".into(),
        endpoint: Some(harness.socket.to_string_lossy().into_owned()),
        ..Default::default()
    };
    let call = |id: u32, name: &str, arguments: Value| {
        json!({"jsonrpc":"2.0", "id":id, "method":"tools/call", "params":{"name":name, "arguments":arguments}}).to_string() + "\n"
    };
    let input = call(1, "open", json!({"url":CREDENTIAL_URL}))
        + &call(2, "goto", json!({"url":BLOCKED_URL}));
    let mut output = Vec::new();
    serve_stdio(Cursor::new(input), &mut output, options.clone()).unwrap();
    let text = String::from_utf8(output).unwrap();
    assert_scrubbed(&text);
    let denied: Vec<Value> = text
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(denied.len(), 2);
    for (index, frame) in denied.iter().enumerate() {
        assert_eq!(frame["jsonrpc"], "2.0");
        assert_eq!(frame["id"], index + 1);
        assert_eq!(frame["result"]["isError"], true, "{frame}");
        assert!(frame.to_string().contains("domain allowlist"), "{frame}");
    }
    assert_eq!(harness.calls.load(Ordering::SeqCst), 1);
    harness.seed_engine_history();
    let mut output = Vec::new();
    serve_stdio(
        Cursor::new(call(3, "get", json!({"kind":"title"}))),
        &mut output,
        options,
    )
    .unwrap();
    let text = String::from_utf8(output).unwrap();
    assert_scrubbed(&text);
    assert_eq!(
        text.lines().count(),
        1,
        "stdout must contain only one JSON-RPC frame"
    );
    let frame: Value = serde_json::from_str(text.trim()).unwrap();
    assert_eq!(frame["jsonrpc"], "2.0");
    assert_eq!(frame["id"], 3);
    assert_ne!(frame["result"]["isError"], true, "{frame}");
    let body: Value =
        serde_json::from_str(frame["result"]["content"][0]["text"].as_str().unwrap()).unwrap();
    assert_eq!(body["data"]["url"], SAFE_URL);
    assert_eq!(
        body["warnings"][0]["message"],
        "domain allowlist blocked 2 request(s)"
    );
    assert_eq!(
        body["warnings"][1]["message"],
        "blocked Document https://[REDACTED]@blocked.example/path?token=[REDACTED]&password=[REDACTED]&view=public (2 requests)"
    );
    assert_eq!(body["warnings"][2]["kind"], "network_policy.limitation");
}
