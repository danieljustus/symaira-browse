#![deny(unsafe_code)]
#![cfg(unix)]
#[path = "support/navigation_security.rs"]
mod support;
use serde_json::json;
use std::sync::atomic::Ordering;
use support::*;

#[test]
fn daemon_apostrophe_and_generic_secret_boundaries_are_scrubbed() {
    let harness = Harness::new("daemon-apostrophe");
    for command in ["open", "goto", "tab.new"] {
        let denied = harness.request(command, APOSTROPHE_URL);
        assert_eq!(denied["success"], false);
        assert_scrubbed(&denied.to_string());
        assert!(
            denied["error"]["message"]
                .as_str()
                .unwrap()
                .contains(APOSTROPHE_REDACTED)
        );
    }
    assert_eq!(harness.calls.load(Ordering::SeqCst), 0);
    harness.seed_boundary_warnings();
    let response = harness.request("get.title", "");
    assert_eq!(response["success"], true);
    assert_eq!(response["warnings"][0]["message"], APOSTROPHE_REDACTED);
    assert_eq!(response["warnings"][0]["ref"], "password=[REDACTED]");
    assert_eq!(
        response["warnings"][0]["excerpt"],
        "reader's note: password=[REDACTED]"
    );
}

#[test]
fn daemon_non_http_navigation_has_zero_handler_dispatches() {
    for (index, domains) in [vec![], vec!["example.com".into()]].into_iter().enumerate() {
        let harness = Harness::with_domains(&format!("daemon-scheme-{index}"), domains);
        for command in ["open", "goto", "tab.new"] {
            for url in ["ws://example.com/", "wss://example.com/"] {
                let response = harness.request(command, url);
                assert_eq!(response["success"], false);
                assert!(
                    response["error"]["message"]
                        .as_str()
                        .unwrap()
                        .contains("http/https URL required")
                );
                assert_eq!(harness.calls.load(Ordering::SeqCst), 0);
            }
        }
    }
}

#[test]
fn daemon_admission_sequence_matches_go_and_redacts_later_warnings() {
    let harness = Harness::new("daemon");
    let mut steps = Vec::new();
    for (command, url) in [("open", SAFE_URL), ("goto", BLOCKED_URL), ("get.title", "")] {
        let response = harness.request(command, url);
        let mut step = json!({"command":command, "success":response["success"], "engine_calls":harness.calls.load(Ordering::SeqCst), "warnings":response.get("warnings").cloned().unwrap_or_default()});
        if response["success"] == false {
            step["error"] = response["error"]["message"].clone();
        }
        steps.push(step);
    }
    assert_eq!(json!(steps), fixture()["safe_sequence"]);
    for command in ["open", "goto"] {
        let denied = harness.request(command, CREDENTIAL_URL);
        assert_eq!(denied["success"], false);
        assert_eq!(denied["error"]["code"], "operation_failed");
        assert_scrubbed(&denied.to_string());
        assert!(denied.to_string().contains("view=public"));
    }
    assert_eq!(harness.calls.load(Ordering::SeqCst), 1);
    // Independent subrequest history may still contain denied credentials.
    harness.seed_engine_history();
    let response = harness.request("get.title", "");
    assert_eq!(response["success"], true);
    assert_scrubbed(&response.to_string());
    assert_eq!(response["data"]["url"], SAFE_URL);
    assert_eq!(
        response["warnings"][0]["message"],
        "domain allowlist blocked 2 request(s)"
    );
    assert_eq!(
        response["warnings"][1]["message"],
        "blocked Document https://[REDACTED]@blocked.example/path?token=[REDACTED]&password=[REDACTED]&view=public (2 requests)"
    );
    assert_eq!(response["warnings"][2]["kind"], "network_policy.limitation");
    assert_eq!(response["warnings"].as_array().unwrap().len(), 3);
}

#[test]
fn runtime_rejects_navigation_before_browser_initialization() {
    for engine in ["chrome", "firefox", "safari-attach", "safari-bidi"] {
        for command in ["open", "goto"] {
            let mut spec = symbrowse_daemon::SessionSpec::for_session("boundary");
            spec.engine = engine.into();
            spec.allowed_domains = vec!["example.com".into()];
            let response = symbrowse_daemon::dispatch_once(
                spec,
                symbrowse_daemon::Frame {
                    cmd: command.into(),
                    args: Some(json!({"url":CREDENTIAL_URL})),
                    ..Default::default()
                },
            );
            assert!(!response.success);
            let error = response.error.unwrap();
            assert_eq!(error.code, "operation_failed");
            assert!(
                error.message.starts_with("navigation URL policy: target "),
                "{engine}: {error}"
            );
            assert_scrubbed(&error.message);
        }
    }
}
