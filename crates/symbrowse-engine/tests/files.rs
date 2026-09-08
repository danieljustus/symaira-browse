use serde::Deserialize;
use std::{fs, path::PathBuf};
use symbrowse_engine::files::{
    DownloadBehavior, DownloadRegistry, UploadError, download_behavior, guard_upload_path,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    upload: Vec<UploadCase>,
    download: DownloadFixture,
}

#[derive(Deserialize)]
struct UploadCase {
    name: String,
    accepted: bool,
    error: Option<String>,
    supported: bool,
}

#[derive(Deserialize)]
struct DownloadFixture {
    deny: DownloadBehavior,
    allow: DownloadBehavior,
    traversal: DownloadBehavior,
    event: DownloadEvent,
    collision: CollisionCase,
}

#[derive(Deserialize)]
struct DownloadEvent {
    guid: String,
    url: String,
    filename: String,
    state: String,
    received_bytes: i64,
    total_bytes: i64,
    sha256: String,
    timestamp: String,
}

#[derive(Deserialize)]
struct CollisionCase {
    suggested_filename: String,
    existing_payload: String,
    guid_payload: String,
    checksum_source: String,
    collision_handling: String,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/engine/file-guards.json"
    ))
    .expect("valid Go file-guard fixture")
}

#[test]
fn upload_guard_matches_go_generated_cases() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    let root = tempfile_root("upload");
    let allowed = root.join("allowed");
    fs::create_dir_all(&allowed).unwrap();
    fs::write(allowed.join("report.pdf"), b"pdf").unwrap();
    let outside = tempfile_root("outside");
    fs::create_dir_all(&outside).unwrap();
    fs::write(outside.join("secret.txt"), b"secret").unwrap();

    for case in fixture.upload {
        if !case.supported {
            continue;
        }
        let path = match case.name.as_str() {
            "valid_relative" => allowed.join("report.pdf"),
            "traversal" => PathBuf::from("allowed/sub/../../etc/passwd"),
            "outside" => outside.join("secret.txt"),
            "missing" => allowed.join("missing.txt"),
            "no_allowed_directories" => allowed.join("report.pdf"),
            "directory" => allowed.clone(),
            "empty" => PathBuf::new(),
            "symlink_escape" => {
                let link = allowed.join("escape-link");
                if make_symlink(&outside.join("secret.txt"), &link).is_err() {
                    continue;
                }
                link
            }
            other => panic!("unknown fixture case {other}"),
        };
        let path_string = path.to_string_lossy().into_owned();
        let allowed_dirs = if case.name == "no_allowed_directories" {
            Vec::new()
        } else {
            vec![allowed.to_string_lossy().into_owned()]
        };
        let allowed_refs: Vec<&str> = allowed_dirs.iter().map(String::as_str).collect();
        let result = guard_upload_path(&path_string, &allowed_refs);
        assert_eq!(result.is_ok(), case.accepted, "case {}", case.name);
        if let Some(expected_error) = case.error {
            let error = result.expect_err(&case.name);
            assert_error_family(&error, &expected_error);
        }
    }
}

#[test]
fn download_behavior_and_integrity_match_go_generated_fixture() {
    let fixture = fixture();
    assert_eq!(download_behavior(" ").unwrap(), fixture.download.deny);

    let root = tempfile_root("downloads");
    let traversal =
        download_behavior(&root.join("downloads/../normalized").to_string_lossy()).unwrap();
    assert_eq!(traversal.behavior, fixture.download.traversal.behavior);
    assert!(traversal.download_path.ends_with("normalized"));

    let directory = root.join("downloads");
    let behavior = download_behavior(&directory.to_string_lossy()).unwrap();
    assert_eq!(behavior.behavior, fixture.download.allow.behavior);
    assert!(behavior.download_path.ends_with("downloads"));
    assert_eq!(
        behavior.events_enabled,
        fixture.download.allow.events_enabled
    );

    fs::write(
        directory.join("g1"),
        fixture.download.collision.guid_payload.as_bytes(),
    )
    .unwrap();
    fs::write(
        directory.join(&fixture.download.collision.suggested_filename),
        fixture.download.collision.existing_payload.as_bytes(),
    )
    .unwrap();
    let mut registry = DownloadRegistry::new();
    registry
        .set_download_behavior("s", &directory.to_string_lossy())
        .unwrap();
    registry.record_download_will_begin(
        "s",
        &fixture.download.event.guid,
        &fixture.download.event.url,
        &fixture.download.event.filename,
        &fixture.download.event.timestamp,
    );
    registry.record_download_progress(
        "s",
        &fixture.download.event.guid,
        &fixture.download.event.state,
        fixture.download.event.received_bytes,
        fixture.download.event.total_bytes,
    );
    let event = &registry.events("s")[0];
    assert_eq!(event.sha256, fixture.download.event.sha256);
    assert_eq!(
        event.filename,
        fixture.download.collision.suggested_filename
    );
    assert_eq!(fixture.download.collision.collision_handling, "none");
    assert_eq!(
        fixture.download.collision.checksum_source,
        "<ROOT>/downloads/g1"
    );
}

fn assert_error_family(error: &UploadError, expected: &str) {
    if expected.contains("traversal") {
        assert!(matches!(error, UploadError::Traversal(_)));
    } else if expected.contains("does not exist") {
        assert!(matches!(error, UploadError::Missing(_)));
    } else if expected.contains("no allowed") {
        assert!(matches!(error, UploadError::NoAllowedDirectories));
    } else if expected.contains("not a regular") {
        assert!(matches!(error, UploadError::NotRegular(_)));
    } else if expected.contains("outside allowed") {
        assert!(matches!(error, UploadError::Outside(_)));
    } else if expected.contains("path is empty") {
        assert!(matches!(error, UploadError::EmptyPath));
    } else {
        panic!("unclassified oracle error: {expected}");
    }
}

#[cfg(unix)]
fn make_symlink(source: &std::path::Path, link: &std::path::Path) -> std::io::Result<()> {
    std::os::unix::fs::symlink(source, link)
}

#[cfg(windows)]
fn make_symlink(source: &std::path::Path, link: &std::path::Path) -> std::io::Result<()> {
    std::os::windows::fs::symlink_file(source, link)
}

fn tempfile_root(name: &str) -> PathBuf {
    let root = std::env::temp_dir().join(format!("symbrowse-engine-{name}-{}", std::process::id()));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).unwrap();
    root
}
