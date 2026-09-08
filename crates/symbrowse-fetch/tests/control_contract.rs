use std::time::Duration;

use serde::Deserialize;
use symbrowse_fetch::{BackoffConfig, Profile, is_transient_status, parse_retry_after_millis};

#[derive(Deserialize)]
struct Fixture {
    oracle_commit: String,
    generated_by: String,
    source_digest: String,
    controls: Controls,
}

#[derive(Deserialize)]
struct Controls {
    profiles: Vec<ProfileCase>,
    retry: Retry,
    robots: Vec<RobotsCase>,
    batch_counts: Vec<usize>,
}

#[derive(Deserialize)]
struct ProfileCase {
    input: String,
    profile: String,
    warning: Option<String>,
}

#[derive(Deserialize)]
struct Retry {
    transient: Vec<StatusCase>,
    backoff: Vec<DelayCase>,
    retry_after: Vec<DelayCase>,
}

#[derive(Deserialize)]
struct StatusCase {
    status: u16,
    value: bool,
}

#[derive(Deserialize)]
struct DelayCase {
    input: Option<String>,
    attempt: Option<usize>,
    jitter: Option<u8>,
    millis: i64,
}

#[derive(Deserialize)]
struct RobotsCase {
    text: String,
    agent: String,
    path: String,
    allowed: bool,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!("../../../port/fixtures/fetch/control.json")).unwrap()
}

#[test]
fn pinned_go_control_fixture_has_provenance_and_full_boundaries() {
    let fixture = fixture();
    assert_eq!(
        fixture.oracle_commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(
        fixture.generated_by,
        "scripts/rust-port/cmd/fetchcontrolgen"
    );
    assert_eq!(fixture.source_digest.len(), 64);
    assert_eq!(fixture.controls.batch_counts, vec![0, 1, 20, 21]);
    assert_eq!(fixture.controls.profiles.len(), 11);
    assert_eq!(fixture.controls.retry.transient.len(), 13);
    assert_eq!(fixture.controls.retry.backoff.len(), 12);
    assert_eq!(fixture.controls.robots.len(), 15);
}

#[test]
fn batch_boundaries_match_pinned_go_counts() {
    let fixture = fixture();
    for count in fixture.controls.batch_counts {
        let valid = symbrowse_fetch::batch::validate_size(count).is_ok();
        assert_eq!(valid, matches!(count, 1..=20), "count={count}");
    }
}
#[test]
fn profile_selection_matches_pinned_go_cases() {
    let fixture = fixture();
    for case in fixture.controls.profiles {
        let actual = Profile::parse(&case.input);
        assert_eq!(
            format!("{actual:?}").to_ascii_lowercase(),
            case.profile,
            "{}",
            case.input
        );
        assert_eq!(
            Profile::parse_warning(&case.input),
            case.warning,
            "{}",
            case.input
        );
    }
}

#[test]
fn retry_classification_and_deterministic_backoff_match_fixture() {
    let fixture = fixture();
    for case in fixture.controls.retry.transient {
        assert_eq!(
            is_transient_status(case.status),
            case.value,
            "{}",
            case.status
        );
    }
    let config = BackoffConfig::default();
    for case in fixture.controls.retry.backoff {
        let attempt = case.attempt.unwrap_or_default();
        let jitter = case.jitter.unwrap_or(100);
        let expected = Duration::from_millis(case.millis.max(0) as u64);
        assert_eq!(
            config.deterministic_delay(attempt, jitter),
            expected,
            "attempt={attempt} jitter={jitter}"
        );
    }
    for case in fixture.controls.retry.retry_after {
        let input = case.input.unwrap_or_default();
        assert_eq!(parse_retry_after_millis(&input), case.millis, "{input}");
    }
}

#[test]
fn robots_control_cases_match_pinned_go_rules() {
    let fixture = fixture();
    for case in fixture.controls.robots {
        let rules = symbrowse_fetch::robots::Robots::parse(&case.text);
        assert_eq!(
            rules.allows(&case.agent, &case.path),
            case.allowed,
            "{} {}",
            case.agent,
            case.path
        );
    }
}
