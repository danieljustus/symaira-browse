use serde::Deserialize;
use serde_json::Value;
use std::{fs, path::PathBuf};

const ORACLE_COMMIT: &str = "652453d1595fc302bd69c328e7da8a21dbee28b9";

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    suite: String,
    capabilities: Value,
    chrome_args: Vec<String>,
    frames: Vec<Value>,
    policy: Value,
    upload: Value,
    download: Value,
    errors: Value,
    unsupported: Vec<String>,
    cleanup: Value,
}

fn fixture_path(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/engine")
        .join(name)
}

fn read_fixture() -> Fixture {
    let bytes = fs::read(fixture_path("chrome-full.json")).expect("chrome fixture");
    serde_json::from_slice(&bytes).expect("valid chrome fixture JSON")
}

#[test]
fn chrome_fixture_is_pinned_and_covers_eng_005_to_007() {
    let fixture = read_fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.suite, "chrome-full");
    assert_eq!(fixture.capabilities["kind"], "chrome");
    assert_eq!(fixture.frames.len(), 2);
    assert_eq!(fixture.frames[1]["parent_id"], "root");
    assert_eq!(fixture.policy["allowlisted"], true);
    assert_eq!(fixture.policy["denied"], false);
    assert_eq!(
        fixture.upload["outside"],
        "rejected outside allowed directory"
    );
    assert_eq!(fixture.download["events_enabled"], true);
    assert!(
        fixture.errors["invalid_screenshot"]
            .as_str()
            .expect("screenshot error")
            .contains("png or jpeg")
    );
    assert_eq!(fixture.unsupported, ["har-export", "axe-core-audit"]);
    assert_eq!(fixture.cleanup["owned_process_killed"], true);
    assert_eq!(fixture.cleanup["owned_process_reaped"], true);
    assert_eq!(fixture.cleanup["private_profile_removed"], true);
    assert!(
        fixture
            .chrome_args
            .iter()
            .any(|arg| arg == "--headless=new")
    );

    let manifest: Value = serde_json::from_slice(
        &fs::read(fixture_path("chrome-full-manifest.json")).expect("chrome manifest"),
    )
    .expect("valid chrome manifest JSON");
    assert_eq!(manifest["oracle_commit"], ORACLE_COMMIT);
    assert!(
        manifest["fixture_sha256"]
            .as_str()
            .is_some_and(|value| value.len() == 64)
    );
}
