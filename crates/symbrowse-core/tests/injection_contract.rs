#![deny(unsafe_code)]

use std::{collections::BTreeMap, fs, path::PathBuf};

use serde::Deserialize;
use symbrowse_core::injection::{self, ScanOptions};

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle: Oracle,
    default_scan: ScanCase,
    custom_scan: ScanCase,
    corpus: ScanCase,
    boundaries: Vec<BoundaryCase>,
}

#[derive(Debug, Deserialize)]
struct Oracle {
    commit: String,
    release: String,
    source_files: BTreeMap<String, String>,
    embedded_patterns_sha256: String,
    embedded_patterns: String,
}

#[derive(Debug, Deserialize)]
struct ScanCase {
    html: String,
    patterns: Option<String>,
    warnings: Vec<injection::ScanWarning>,
    content_length: usize,
}

#[derive(Debug, Deserialize)]
struct BoundaryCase {
    name: String,
    origin: String,
    content: String,
    wrapped_template: String,
    parsed_content: String,
    parsed_origin: String,
    spoof_content: Option<String>,
    spoof_wrapped_template: Option<String>,
    spoof_parsed: Option<String>,
}

fn fixture() -> Fixture {
    const CONTENT: &[u8] =
        include_bytes!("../../../testdata/port/injection/injection-contract.json");
    serde_json::from_slice(CONTENT).expect("decode Go-generated injection fixture")
}

#[test]
fn fixture_has_pinned_go_and_embedded_pattern_provenance() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(fixture.oracle.release, "v0.8.0");
    assert_eq!(fixture.oracle.source_files.len(), 3);
    for (path, expected) in [
        (
            "internal/injection/scan.go",
            "3708d8a8420e0b95f42d4764e4a5124723ab463dcf7f1669fef2f4e77308da32",
        ),
        (
            "internal/injection/boundary.go",
            "ae029f5af218310967b5466129fb252575bb08e98afa70a6122f7e630e5946d9",
        ),
        (
            "internal/injection/patterns.txt",
            "a06b5da389640f558c44cf590e27db2848d0a5f125d4f48153553437147e047c",
        ),
    ] {
        assert_eq!(
            fixture.oracle.source_files.get(path).map(String::as_str),
            Some(expected),
            "{path}"
        );
    }
    // Git may check the tracked asset out with CRLF on Windows; compare its
    // logical pattern content independently of checkout line endings.
    let embedded_patterns = injection::EMBEDDED_PATTERNS.replace("\r\n", "\n");
    assert_eq!(
        fixture.oracle.embedded_patterns.len(),
        embedded_patterns.len()
    );
    assert_eq!(fixture.oracle.embedded_patterns, embedded_patterns);
    assert_eq!(
        fixture.oracle.embedded_patterns_sha256,
        "a06b5da389640f558c44cf590e27db2848d0a5f125d4f48153553437147e047c"
    );
}

#[test]
fn default_and_custom_scans_match_go_warning_fixtures() {
    let fixture = fixture();
    assert_scan_case(&fixture.default_scan, &ScanOptions::default());

    let path = temp_path("patterns");
    fs::write(&path, fixture.custom_scan.patterns.as_deref().unwrap())
        .expect("write custom patterns");
    assert_scan_case(
        &fixture.custom_scan,
        &ScanOptions {
            patterns_file: Some(path.clone()),
        },
    );
    let _ = fs::remove_file(path);
}

#[test]
fn hundred_kib_corpus_matches_go_warning_fixture() {
    let fixture = fixture();
    assert!(fixture.corpus.content_length >= 100 * 1024);
    assert_eq!(fixture.corpus.html.len(), fixture.corpus.content_length);
    assert_scan_case(&fixture.corpus, &ScanOptions::default());
}

fn assert_scan_case(case: &ScanCase, options: &ScanOptions) {
    let warnings = injection::scan(&case.html, options).expect("scan fixture");
    assert_eq!(warnings, case.warnings);
}

#[test]
fn boundary_fixture_preserves_content_and_rejects_spoof_markers() {
    let fixture = fixture();
    assert_eq!(fixture.boundaries.len(), 2);
    for case in &fixture.boundaries {
        let boundary = injection_boundary::Boundary::new(&case.origin).expect("new boundary");
        if case.spoof_content.is_none() {
            assert_eq!(
                normalize_nonce(&boundary.wrap_text(&case.content), &boundary.nonce),
                case.wrapped_template,
                "{}",
                case.name
            );
            let (content, parsed) =
                injection_boundary::parse_text(&boundary.wrap_text(&case.content), &boundary.nonce)
                    .expect("parse boundary");
            assert_eq!(content, case.parsed_content, "{} content", case.name);
            assert_eq!(parsed.origin, case.parsed_origin, "{} origin", case.name);
        } else {
            assert_eq!(
                normalize_nonce(&boundary.wrap_text("content"), &boundary.nonce),
                case.wrapped_template,
                "{} base template",
                case.name
            );
        }

        if let (Some(spoof_content), Some(spoof_template), Some(spoof_parsed)) = (
            &case.spoof_content,
            &case.spoof_wrapped_template,
            &case.spoof_parsed,
        ) {
            let wrapped = boundary.wrap_text(spoof_content);
            assert_eq!(normalize_nonce(&wrapped, &boundary.nonce), *spoof_template);
            let (parsed, _) = injection_boundary::parse_text(&wrapped, &boundary.nonce)
                .expect("parse spoof boundary");
            assert_eq!(parsed, *spoof_parsed);
            assert_eq!(parsed, *spoof_content);
        }
    }
}

fn normalize_nonce(value: &str, nonce: &str) -> String {
    value.replace(nonce, "{nonce}")
}

fn temp_path(name: &str) -> PathBuf {
    std::env::temp_dir().join(format!(
        "symbrowse-injection-{name}-{}-{}.txt",
        std::process::id(),
        std::thread::current().name().unwrap_or("test")
    ))
}

mod injection_boundary {
    pub use symbrowse_core::injection_boundary::{Boundary, parse_text};
}
