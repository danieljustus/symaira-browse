use serde_json::Value;
use std::collections::BTreeMap;
use symbrowse_core::{browser_state, flows, journal, oob, profiles, runner, settings, trace};

const FIXTURE: &str = include_str!("../../../testdata/port/workflows/workflows.json");

#[test]
fn pinned_go_fixture_covers_requested_contracts_without_secrets() {
    let root: Value = serde_json::from_str(FIXTURE).unwrap();
    assert_eq!(
        root["oracle_commit"],
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(root["generated_by"], "scripts/rust-port/cmd/workflowgen");
    assert_eq!(root["fixture_families"].as_array().unwrap().len(), 8);
    assert_eq!(root["source_files"].as_object().unwrap().len(), 9);
    assert_eq!(root["source_digest"].as_str().unwrap().len(), 64);
    let contracts = root["contracts"].as_object().unwrap();
    for id in [
        "FLOW-001",
        "FLOW-002",
        "FLOW-003",
        "FLOW-004",
        "SES-001",
        "SES-002",
        "SES-003",
        "SES-004",
        "SES-005",
        "SES-006",
        "STATE-006",
    ] {
        assert!(contracts.contains_key(id), "missing {id}");
    }
    let text = FIXTURE.to_ascii_lowercase();
    assert!(!text.contains("fixture-secret-value"));
    assert!(!text.contains("password123"));
    assert_eq!(root["oob"]["allowed"], false);
    assert_eq!(root["session"]["hard_stop_code"], "handoff_timeout");
    assert_eq!(
        root["auth"]["replay_hard_stop"],
        "credential step requires symvault re-resolution; replay it with auth login"
    );
    assert!(root.get("blockers").is_none() || root["blockers"].as_object().unwrap().is_empty());
}

#[test]
fn flow_fixture_parses_and_dry_run_is_browser_free() {
    let yaml = br#"name: fixture-flow
version: 1
domains: [example.test]
inputs: [email]
steps:
  - open: {url: https://example.test/start}
  - fill: {label: Email, value: '{{email}}'}
  - assert: {visible: Continue}
  - click: {label: Continue}
  - wait: {url: '**/done'}
"#;
    let flow = flows::parse(yaml, "fixture").unwrap();
    let plan = flows::dry_run(&flow);
    assert_eq!(plan.len(), 5);
    assert_eq!(plan[0].action, "open");
    assert_eq!(plan[4].action, "wait");
}

#[test]
fn journal_trace_oob_settings_and_profiles_are_pure_contracts() {
    let entries = vec![
        journal::Entry {
            command: "open".into(),
            args: Some(serde_json::json!({"url":"https://example.test"})),
            risk_class: "navigate".into(),
            ..Default::default()
        },
        journal::Entry {
            command: "fill".into(),
            args: Some(serde_json::json!({"selector":"#password", "value":"••••"})),
            risk_class: "credential".into(),
            ..Default::default()
        },
    ];
    let trace = trace::export(&entries, "fixture", "2026-08-06T12:00:00Z");
    assert_eq!(trace.steps.len(), 1);
    assert_eq!(trace.steps[0].expected_url, "https://example.test");
    let replay = trace::compare_urls(&trace, &[Ok("https://example.test/".into())]);
    assert_eq!((replay.matched, replay.deviated, replay.failed), (1, 0, 0));

    let prompt = oob::Manager::new().create(
        oob::Kind::Approval,
        "Approve",
        "human",
        std::time::Duration::from_millis(1),
        "fixed",
    );
    let command = oob::notification_command(&prompt);
    assert_eq!(command.program, "osascript");
    assert!(!command.args.join(" ").contains("credential"));
    assert!(settings::validate_viewport(1280, 800, 1.0).is_ok());
    assert!(settings::validate_geo(91.0, 0.0).is_err());
    let headers = BTreeMap::from([(String::from("Authorization"), String::from("[REDACTED]"))]);
    assert!(settings::validate_headers(&headers).is_err());
    assert!(profiles::is_profile_name("Default"));
    assert!(!profiles::is_profile_name("Cache"));
}

#[test]
fn generated_runtime_families_are_executable_without_secret_material() {
    let root: Value = serde_json::from_str(FIXTURE).unwrap();
    assert_eq!(root["cookies_storage"]["origin"], "https://example.test");
    assert_eq!(
        root["cookies_storage"]["after_clear"]
            .as_array()
            .unwrap()
            .len(),
        1
    );
    assert_eq!(root["auth"]["reference"], "op://fixture/login");
    assert_eq!(root["auth"]["plaintext_rejected"], true);

    let flow = flows::parse(
        br#"name: fixture-flow
version: 1
domains: [example.test]
steps:
  - open: {url: https://example.test/start}
  - click: {label: Continue}
  - wait: {url: '**/done'}
outputs:
  - {name: final_url, from: url}
"#,
        "fixture",
    )
    .unwrap();
    let mut calls = Vec::new();
    let mut executor = |command: &str, args: Value| {
        calls.push(command.to_owned());
        Ok(match command {
            "get.url" => Value::String("https://example.test/done".into()),
            "find" => serde_json::json!({"ref": "e1"}),
            _ => args,
        })
    };
    let report = runner::run(
        &mut executor,
        runner::RunOptions {
            flow,
            inputs: Default::default(),
            dry_run: false,
        },
    )
    .unwrap();
    assert!(report.success);
    assert_eq!(report.outputs["final_url"], "https://example.test/done");
    assert_eq!(
        calls,
        ["open", "find", "scrollintoview", "click", "wait", "get.url"]
    );
}

#[test]
fn cookie_storage_contract_preserves_origin_scope_and_mutation_order() {
    let mut state = browser_state::OriginState::default();
    let make = |name: &str| browser_state::Cookie {
        name: name.into(),
        value: "secret".into(),
        domain: ".example.test".into(),
        path: "/".into(),
        secure: true,
        http_only: true,
    };
    browser_state::set_cookie(&mut state, make("z")).unwrap();
    browser_state::set_cookie(&mut state, make("a")).unwrap();
    browser_state::storage_mut(&mut state, browser_state::StorageKind::Local)
        .insert("theme".into(), "dark".into());
    assert_eq!(
        browser_state::origin("https://example.test/").unwrap(),
        "https://example.test"
    );
    assert_eq!(browser_state::list_cookies(&state).unwrap()[0].name, "a");
    assert_eq!(
        browser_state::storage(&state, browser_state::StorageKind::Local)["theme"],
        "dark"
    );
    browser_state::clear_cookie(&mut state, "a").unwrap();
    assert_eq!(browser_state::list_cookies(&state).unwrap().len(), 1);
}
