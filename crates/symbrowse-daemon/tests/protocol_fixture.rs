#![deny(unsafe_code)]

use serde::Deserialize;
use symbrowse_daemon::{DaemonError, Frame, Response, Warning, decode_frame};

#[derive(Deserialize)]
struct Fixture {
    frame: String,
    success: String,
    failure: String,
    warning: String,
    bad_frame_error: String,
}

#[test]
fn daemon_wire_schema_matches_go_bytes() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/daemon/protocol.json")).unwrap();
    let frame: Frame = serde_json::from_str(&fixture.frame).unwrap();
    assert_eq!(serde_json::to_string(&frame).unwrap(), fixture.frame);
    let success: Response = serde_json::from_str(&fixture.success).unwrap();
    assert_eq!(serde_json::to_string(&success).unwrap(), fixture.success);
    let failure: Response = serde_json::from_str(&fixture.failure).unwrap();
    assert_eq!(serde_json::to_string(&failure).unwrap(), fixture.failure);
    let warning: Warning = serde_json::from_str(&fixture.warning).unwrap();
    assert_eq!(serde_json::to_string(&warning).unwrap(), fixture.warning);
    let error: DaemonError = failure.error.unwrap();
    assert_eq!(error.code, "operation_failed");
    assert_eq!(
        decode_frame(br#"{"session":"fixture"}"#)
            .unwrap_err()
            .to_string(),
        fixture.bad_frame_error
    );
}
