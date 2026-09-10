#![deny(unsafe_code)]

use serde::Deserialize;
use serde_json::json;
use symbrowse_core::{
    budget::{estimate, line_range, truncate},
    cache::Cache,
    error::ErrorCode,
    output::{Envelope, ErrorPayload, Format, Warning},
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle: Oracle,
    error_codes: Vec<ErrorCase>,
    outputs: Vec<OutputCase>,
    estimates: Vec<EstimateCase>,
    truncations: Vec<TruncateCase>,
    line_ranges: Vec<LineRangeCase>,
    cache: CacheCase,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct ErrorCase {
    code: String,
    kind: String,
    exit: u8,
}

#[derive(Deserialize)]
struct OutputCase {
    name: String,
    format: String,
    output: String,
}

#[derive(Deserialize)]
struct EstimateCase {
    name: String,
    input: String,
    expected: usize,
}

#[derive(Deserialize)]
struct TruncateCase {
    name: String,
    input: String,
    max_tokens: usize,
    head: String,
    foot: String,
    tokens_returned: usize,
    tokens_total: usize,
    truncated: bool,
}

#[derive(Deserialize)]
struct LineRangeCase {
    name: String,
    input: String,
    start: usize,
    end: usize,
    output: String,
}

#[derive(Deserialize)]
struct CacheCase {
    id: String,
    content: String,
    metadata: String,
    content_mode: u32,
    meta_mode: u32,
}

fn fixture() -> Fixture {
    const CONTENT: &[u8] =
        include_bytes!("../../../testdata/port/core/output-budget-contract.json");
    serde_json::from_slice(CONTENT).expect("decode Go-generated core fixture")
}

#[test]
fn fixture_provenance_is_pinned() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(fixture.oracle.release, "v0.8.0");
}

#[test]
fn error_taxonomy_matches_go() {
    let fixture = fixture();
    assert_eq!(fixture.error_codes.len(), ErrorCode::ALL.len());
    for (expected, actual) in fixture.error_codes.iter().zip(ErrorCode::ALL) {
        assert_eq!(serde_json::to_value(actual).unwrap(), expected.code);
        assert_eq!(
            actual.kind().to_string(),
            expected.kind,
            "{}",
            expected.code
        );
        assert_eq!(actual.exit_code(), expected.exit, "{}", expected.code);
    }
}

#[test]
fn output_bytes_match_go() {
    for expected in fixture().outputs {
        let (envelope, format) = output_input(&expected.name);
        assert_eq!(format_name(format), expected.format, "{}", expected.name);
        assert_eq!(
            envelope.render(format).expect("render output"),
            expected.output,
            "{}",
            expected.name
        );
    }
}

#[test]
fn budget_contract_matches_go() {
    let fixture = fixture();
    for expected in fixture.estimates {
        assert_eq!(
            estimate(&expected.input),
            expected.expected,
            "{}",
            expected.name
        );
    }
    for expected in fixture.truncations {
        let actual = truncate(&expected.input, expected.max_tokens);
        assert_eq!(actual.head, expected.head, "{} head", expected.name);
        assert_eq!(actual.foot, expected.foot, "{} foot", expected.name);
        assert_eq!(
            actual.tokens_returned, expected.tokens_returned,
            "{} returned",
            expected.name
        );
        assert_eq!(
            actual.tokens_total, expected.tokens_total,
            "{} total",
            expected.name
        );
        assert_eq!(
            actual.truncated, expected.truncated,
            "{} truncated",
            expected.name
        );
    }
    for expected in fixture.line_ranges {
        assert_eq!(
            line_range(&expected.input, expected.start, expected.end),
            expected.output,
            "{}",
            expected.name
        );
    }
}

#[test]
fn cache_files_from_go_are_readable() {
    use std::{fs, time::Duration};

    let expected = fixture().cache;
    let root =
        std::env::temp_dir().join(format!("symbrowse-go-cache-fixture-{}", std::process::id()));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).expect("create cache fixture root");
    let content_path = root.join(format!("{}.json", expected.id));
    let meta_path = root.join(format!("{}.meta.json", expected.id));
    fs::write(&content_path, expected.content.as_bytes()).expect("write cache content fixture");
    fs::write(&meta_path, expected.metadata.as_bytes()).expect("write cache metadata fixture");
    let cache = Cache::new(&root, Duration::from_secs(3600));
    assert_eq!(
        cache.load(&expected.id).unwrap(),
        expected.content.as_bytes()
    );
    let entries = cache.list().unwrap();
    assert_eq!(entries.len(), 1);
    assert_eq!(entries[0].id, expected.id);
    assert_eq!(entries[0].bytes, expected.content.len() as u64);
    assert_eq!(expected.content_mode, 0o600);
    assert_eq!(expected.meta_mode, 0o600);
    fs::remove_dir_all(root).expect("remove cache fixture root");
}

fn output_input(name: &str) -> (Envelope, Format) {
    match name {
        "json-success" => (
            Envelope::ok(
                json!({"result": "ok", "count": 2}),
                vec![Warning {
                    kind: "prompt_injection".to_owned(),
                    severity: "high".to_owned(),
                    message: "untrusted instruction".to_owned(),
                    r#ref: "@e7".to_owned(),
                    excerpt: "ignore previous".to_owned(),
                }],
            ),
            Format::Json,
        ),
        "json-error" => {
            let mut envelope =
                Envelope::failure(ErrorCode::StaleRef, "element @e7 no longer exists");
            envelope.error.as_mut().unwrap().hint = "run snapshot --diff".to_owned();
            (envelope, Format::Json)
        }
        "json-hard-stop" => (
            Envelope {
                success: false,
                data: json!(null),
                warnings: Vec::new(),
                error: Some(ErrorPayload {
                    code: ErrorCode::SessionUserControl,
                    message: "session is controlled by a human".to_owned(),
                    hint: String::new(),
                    details: None,
                    retryable: Some(false),
                    requires_user_confirmation: Some(true),
                    resume_hint: "request explicit confirmation".to_owned(),
                }),
            },
            Format::Json,
        ),
        "text-nil-success" => (Envelope::ok(json!(null), Vec::new()), Format::Text),
        "text-string" => (Envelope::ok(json!("hello"), Vec::new()), Format::Text),
        "text-map" => (
            Envelope::ok(json!({"b": 2, "a": 1}), Vec::new()),
            Format::Text,
        ),
        "text-error" => (
            Envelope::failure(ErrorCode::Internal, "something failed"),
            Format::Text,
        ),
        "text-truncation" => (
            Envelope::ok(
                json!({
                    "truncated": true,
                    "tokens_returned": 90,
                    "tokens_total": 18400,
                    "cache_id": "out_000000000000",
                    "hint": "symbrowse cache get out_000000000000 --range 40-120",
                    "head": "HEAD",
                    "foot": "FOOT"
                }),
                Vec::new(),
            ),
            Format::Text,
        ),
        "yaml-success" => (
            Envelope::ok(json!({"url": "https://example.com", "n": 42}), Vec::new()),
            Format::Yaml,
        ),
        "yaml-warning" => (
            Envelope::ok(
                json!("ok"),
                vec![Warning {
                    kind: "prompt_injection".to_owned(),
                    severity: "high".to_owned(),
                    message: "untrusted instruction".to_owned(),
                    r#ref: "@e7".to_owned(),
                    excerpt: "ignore previous".to_owned(),
                }],
            ),
            Format::Yaml,
        ),
        "yaml-error" => {
            let mut envelope = Envelope::failure(ErrorCode::StaleRef, "element missing");
            envelope.error.as_mut().unwrap().hint = "take a snapshot".to_owned();
            (envelope, Format::Yaml)
        }
        _ => panic!("unknown output fixture {name}"),
    }
}

const fn format_name(format: Format) -> &'static str {
    match format {
        Format::Text => "text",
        Format::Json => "json",
        Format::Yaml => "yaml",
    }
}
