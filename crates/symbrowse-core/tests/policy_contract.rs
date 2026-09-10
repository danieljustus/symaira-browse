#![deny(unsafe_code)]

use std::{collections::BTreeMap, fs, path::PathBuf};

use serde::Deserialize;
use symbrowse_core::policy::{
    Allowlist, Decision, Mode, Policy, RiskClass, SsrfGuard, classify, defaults,
};

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle: Oracle,
    allowlists: Vec<AllowlistCase>,
    ssrf: Vec<SsrfCase>,
    risk: RiskFixture,
}

#[derive(Debug, Deserialize)]
struct Oracle {
    commit: String,
    release: String,
    source_files: BTreeMap<String, String>,
}

#[derive(Debug, Deserialize)]
struct AllowlistCase {
    name: String,
    patterns: Option<Vec<String>>,
    valid: bool,
    #[serde(default)]
    hosts: Vec<HostCase>,
    #[serde(default)]
    urls: Vec<UrlCase>,
}

#[derive(Debug, Deserialize)]
struct HostCase {
    host: String,
    allow: bool,
}

#[derive(Debug, Deserialize)]
struct UrlCase {
    url: String,
    allow: bool,
}

#[derive(Debug, Deserialize)]
struct SsrfCase {
    name: String,
    url: String,
    addresses: Option<Vec<String>>,
    allow: bool,
    error: Option<String>,
}

#[derive(Debug, Deserialize)]
struct RiskFixture {
    defaults: Vec<DefaultCase>,
    rules: Vec<RuleCase>,
    explain: String,
}

#[derive(Debug, Deserialize)]
struct DefaultCase {
    class: String,
    mode: String,
    want: String,
}

#[derive(Debug, Deserialize)]
struct RuleCase {
    class: String,
    host: String,
    mode: String,
    want: String,
    origin: String,
}

fn fixture() -> Fixture {
    const CONTENT: &[u8] = include_bytes!("../../../testdata/port/policy/policy-contract.json");
    serde_json::from_slice(CONTENT).expect("decode Go-generated policy fixture")
}

#[test]
fn fixture_has_pinned_go_provenance() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(fixture.oracle.release, "v0.8.0");
    assert_eq!(fixture.oracle.source_files.len(), 3);
    for path in [
        "internal/policy/allowlist.go",
        "internal/policy/ssrf.go",
        "internal/policy/policy.go",
    ] {
        assert_eq!(
            fixture.oracle.source_files.get(path).map(String::len),
            Some(64)
        );
    }
}

#[test]
fn allowlist_matches_go_generated_cases() {
    for case in fixture().allowlists {
        let result = case
            .patterns
            .as_deref()
            .unwrap_or_default()
            .pipe(Allowlist::parse);
        assert_eq!(result.is_ok(), case.valid, "{}", case.name);
        if let Ok(list) = result {
            for host in case.hosts {
                assert_eq!(
                    list.allows_host(&host.host),
                    host.allow,
                    "{} host",
                    case.name
                );
            }
            for url in case.urls {
                assert_eq!(list.allows_url(&url.url), url.allow, "{} url", case.name);
            }
        }
    }
}

#[test]
fn ssrf_matches_go_generated_cases_without_network() {
    for case in fixture().ssrf {
        let name = case.name.clone();
        let addresses = case.addresses.clone();
        let guard = SsrfGuard::with_lookup(false, move |_| {
            if name == "dns-failure" {
                Err("fixture DNS failure".to_owned())
            } else {
                Ok(addresses.clone().unwrap_or_default())
            }
        });
        let result = guard.allows_url(&case.url);
        assert_eq!(result.is_ok(), case.allow, "{}", case.name);
        if let (Err(error), Some(expected)) = (result, case.error) {
            assert!(
                error.to_string().starts_with(&expected),
                "{}: {error}",
                case.name
            );
        }
    }
}

#[test]
fn ssrf_allow_private_is_explicit_opt_out() {
    let guard = SsrfGuard::with_lookup(true, |_| Ok(vec!["127.0.0.1".to_owned()]));
    assert!(guard.allows_url("http://localhost:3000/").is_ok());
}

#[test]
fn risk_defaults_and_rules_match_go_generated_cases() {
    let fixture = fixture();
    for case in fixture.risk.defaults {
        let class = parse_class(&case.class);
        let mode = parse_mode(&case.mode);
        assert_eq!(
            defaults(class, mode).to_string(),
            case.want,
            "default {}/{}",
            case.class,
            case.mode
        );
    }

    let policy = Policy {
        rules: vec![
            rule("submit", "bank.example.com", "deny"),
            rule("credential", "login.example.com", "allow"),
        ],
        source: String::new(),
    };
    for case in fixture.risk.rules {
        let (actual, origin) =
            policy.decide(parse_class(&case.class), &case.host, parse_mode(&case.mode));
        assert_eq!(actual.to_string(), case.want, "decision {}", case.host);
        assert_eq!(origin, case.origin, "origin {}", case.host);
    }
    assert_eq!(
        policy
            .explain("auth.login", "https://login.example.com/app", Mode::Mcp)
            .unwrap(),
        fixture.risk.explain
    );
}

#[test]
fn every_go_classified_command_is_known() {
    for command in symbrowse_core::policy::sorted_commands() {
        assert!(
            classify(command).is_ok(),
            "missing classification for {command}"
        );
    }
}

fn parse_class(value: &str) -> RiskClass {
    RiskClass::ALL
        .into_iter()
        .find(|class| class.as_str() == value)
        .expect("fixture risk class")
}

fn parse_mode(value: &str) -> Mode {
    match value {
        "mcp" => Mode::Mcp,
        "tty" => Mode::Tty,
        other => panic!("fixture mode {other}"),
    }
}

fn rule(class: &str, domain: &str, decision: &str) -> symbrowse_core::policy::Rule {
    symbrowse_core::policy::Rule {
        class: class.to_owned(),
        domain: domain.to_owned(),
        decision: decision.to_owned(),
    }
}

trait Pipe: Sized {
    fn pipe<T>(self, function: impl FnOnce(Self) -> T) -> T {
        function(self)
    }
}
impl<T> Pipe for T {}

#[allow(dead_code)]
fn fixture_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/policy")
}

#[allow(dead_code)]
fn fixture_exists() -> bool {
    fs::metadata(fixture_root().join("policy-contract.json")).is_ok()
}

#[allow(dead_code)]
fn _decision_name(decision: Decision) -> &'static str {
    decision.as_str()
}
