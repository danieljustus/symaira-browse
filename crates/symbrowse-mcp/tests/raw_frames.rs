#![deny(unsafe_code)]

use std::io::Cursor;

use serde_json::Value;
use symbrowse_mcp::{ServeOptions, ToolError, ToolProxy, registry, serve_stdio, serve_with_proxy};

struct GoToolErrorFixtureProxy;

impl ToolProxy for GoToolErrorFixtureProxy {
    fn call(
        &mut self,
        _tool: &registry::ToolSpec,
        _arguments: &Value,
    ) -> Result<Value, Box<ToolError>> {
        Err(Box::new(ToolError {
            code: "peer_denied".to_owned(),
            message: "blocked_private: http://127.0.0.1:1 targets a private or loopback address".to_owned(),
            hint: None,
            retryable: Some(false),
            requires_user_confirmation: Some(false),
            resume_hint: Some("the target is a private or loopback address; start the daemon with --allow-private to permit it".to_owned()),
            details: None,
        }))
    }
}

fn run_fixture(name: &str, args: &[&str]) {
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
    let input_path = format!("../../testdata/port/mcp/{name}.in");
    let output_path = format!("../../testdata/port/mcp/{name}.out");
    let input = std::fs::read(root.join(input_path)).expect("read MCP input fixture");
    let expected = std::fs::read(root.join(output_path)).expect("read MCP output fixture");
    let mut profiles = "core";
    for window in args.windows(2) {
        if window[0] == "--tools" {
            profiles = window[1];
        }
    }
    let options = ServeOptions {
        version: "v0.8.0".to_owned(),
        session: "default".to_owned(),
        profiles: profiles.to_owned(),
        executable: String::new(),
        allow_private: false,
        engine: None,
        daemon_log_path: None,
    };
    let mut actual = Vec::new();
    if name == "tool_error" {
        serve_with_proxy(
            Cursor::new(input),
            &mut actual,
            options,
            &mut GoToolErrorFixtureProxy,
        )
        .expect("serve MCP fixture");
    } else {
        serve_stdio(Cursor::new(input), &mut actual, options).expect("serve MCP fixture");
    }
    assert_eq!(actual, expected, "fixture={name}");
}

#[test]
fn initialize_is_byte_exact_and_clean() {
    run_fixture("initialize", &["mcp"]);
    run_fixture("framed_initialize", &["mcp"]);
    run_fixture("initialized", &["mcp"]);
}

#[test]
fn tools_list_profiles_are_byte_exact() {
    run_fixture("tools_core", &["mcp"]);
    run_fixture("tools_nav", &["mcp", "--tools", "nav"]);
    run_fixture("tools_all", &["mcp", "--tools", "all"]);
}

#[test]
fn malformed_unknown_and_typed_argument_errors_match() {
    run_fixture("malformed", &["mcp"]);
    run_fixture("unknown_method", &["mcp"]);
    run_fixture("unknown_tool", &["mcp"]);
    run_fixture("missing_argument", &["mcp"]);
    run_fixture("tool_error", &["mcp"]);
}

#[test]
fn notifications_and_eof_are_silent() {
    run_fixture("notifications", &["mcp"]);
    run_fixture("eof", &["mcp"]);
}
