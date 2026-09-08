#![cfg(target_os = "macos")]

use serde_json::Value;
use std::{fs, path::PathBuf};

const ORACLE_COMMIT: &str = "652453d1595fc302bd69c328e7da8a21dbee28b9";

fn fixture() -> Value {
    let path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/engine/safari.json");
    serde_json::from_slice(&fs::read(path).expect("Safari fixture")).expect("valid Safari fixture")
}

#[test]
fn safari_fixture_freezes_attach_and_bidi_boundaries() {
    let value = fixture();
    assert_eq!(value["schema_version"], 1);
    assert_eq!(value["suite"], "safari");
    assert_eq!(value["attach"]["engine_kind"], "safari-attach");
    assert_eq!(value["attach"]["launch_mode"], "attach");
    assert_eq!(
        value["attach"]["policy"]["invalid_target_before_runner"],
        true
    );
    assert_eq!(value["attach"]["lifecycle"]["close_quits_safari"], false);
    assert_eq!(value["bidi"]["engine_kind"], "safari-bidi");
    assert_eq!(value["bidi"]["launch_mode"], "launch");
    assert_eq!(
        value["bidi"]["protocol"]["boolean_websocket_url_rejected"],
        true
    );
    assert_eq!(value["bidi"]["protocol"]["foreign_socket_rejected"], true);
    assert_eq!(
        value["bidi"]["lifecycle"]["session_delete_before_driver_stop"],
        true
    );
}

#[test]
fn safari_manifest_pins_the_go_oracle() {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/engine/safari-manifest.json");
    let value: Value = serde_json::from_slice(&fs::read(path).expect("Safari manifest"))
        .expect("valid Safari manifest");
    assert_eq!(value["oracle_commit"], ORACLE_COMMIT);
    assert_eq!(value["native_gate"]["status"], "manual-gate");
}
