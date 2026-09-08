#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::PathBuf,
    sync::atomic::{AtomicU64, Ordering},
};

use symbrowse_core::{
    state::{Cookie, OriginState, State},
    state_store::{KeyMaterial, Store, metadata_for},
};
use time::{Duration, macros::datetime};

static NEXT_ROOT: AtomicU64 = AtomicU64::new(1);

#[test]
fn plaintext_store_round_trip_is_atomic_and_private() {
    let root = test_root("plaintext");
    let store = Store::new(&root, Duration::days(30), None).unwrap();
    let mut state = sample_state("demo");
    store
        .save_at(&mut state, datetime!(2026-08-25 14:00 UTC))
        .unwrap();
    assert_eq!(state.schema_version, 3);
    assert_eq!(state.saved_at, "2026-08-25T14:00:00Z");
    assert_eq!(state.expires_at, "2026-09-24T14:00:00Z");
    assert_eq!(state.key_source, "none");
    assert_eq!(store.load("demo").unwrap(), state);
    assert_eq!(store.list().unwrap(), ["demo"]);
    assert_no_temporary_files(&root);
    assert_private_modes(&root, &root.join("demo.json"));
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn plaintext_v3_save_matches_go_production_bytes() {
    let root = test_root("go-bytes");
    let store = Store::new(&root, Duration::days(30), None).unwrap();
    let fixture_root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/state");
    let manifest: serde_json::Value =
        serde_json::from_slice(&fs::read(fixture_root.join("manifest.json")).unwrap()).unwrap();
    let case = manifest["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|case| case["name"] == "plaintext-v3")
        .unwrap();
    let mut state: State = serde_json::from_value(case["expected"].clone()).unwrap();
    store
        .save_at(&mut state, datetime!(2026-08-25 14:00 UTC))
        .unwrap();
    let expected = fs::read(fixture_root.join("plaintext-v3.state")).unwrap();
    assert_eq!(fs::read(root.join("fixture-v3.json")).unwrap(), expected);
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn encrypted_store_round_trip_uses_key_source() {
    let root = test_root("encrypted");
    let store = Store::new(
        &root,
        Duration::days(30),
        Some(KeyMaterial::new([0xab; 32], "environment").unwrap()),
    )
    .unwrap();
    let mut state = sample_state("secret");
    store
        .save_at(&mut state, datetime!(2026-08-25 14:00 UTC))
        .unwrap();
    let raw = fs::read(root.join("secret.json")).unwrap();
    assert!(
        !raw.windows(b"fixture-secret".len())
            .any(|window| window == b"fixture-secret")
    );
    assert_eq!(state.key_source, "environment");
    assert_eq!(store.load("secret").unwrap(), state);
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn plaintext_state_can_be_resaved_with_encryption() {
    let root = test_root("upgrade-encryption");
    let plain = Store::new(&root, Duration::days(30), None).unwrap();
    let mut state = sample_state("upgrade");
    plain
        .save_at(&mut state, datetime!(2026-08-25 14:00 UTC))
        .unwrap();
    let encrypted = Store::new(
        &root,
        Duration::days(30),
        Some(KeyMaterial::new([0xab; 32], "environment").unwrap()),
    )
    .unwrap();
    let mut loaded = encrypted.load("upgrade").unwrap();
    assert_eq!(loaded.key_source, "none");
    encrypted
        .save_at(&mut loaded, datetime!(2026-08-26 14:00 UTC))
        .unwrap();
    assert_eq!(loaded.key_source, "environment");
    assert_eq!(encrypted.load("upgrade").unwrap(), loaded);
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn encrypted_state_cannot_be_silently_downgraded_to_plaintext() {
    let root = test_root("reject-downgrade");
    let encrypted = Store::new(
        &root,
        Duration::days(30),
        Some(KeyMaterial::new([0xab; 32], "environment").unwrap()),
    )
    .unwrap();
    let mut state = sample_state("downgrade");
    encrypted
        .save_at(&mut state, datetime!(2026-08-25 14:00 UTC))
        .unwrap();
    let mut loaded = encrypted.load("downgrade").unwrap();
    let plaintext = Store::new(&root, Duration::days(30), None).unwrap();
    assert!(
        plaintext
            .save_at(&mut loaded, datetime!(2026-08-26 14:00 UTC))
            .is_err()
    );
    assert_eq!(encrypted.load("downgrade").unwrap(), state);
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn metadata_never_contains_cookie_or_storage_values() {
    let state = sample_state("metadata");
    let metadata = metadata_for(&state);
    assert_eq!(metadata.origins.len(), 1);
    assert_eq!(metadata.origins[0].cookie_count, 1);
    assert_eq!(metadata.origins[0].local_storage_keys, 1);
    assert_eq!(metadata.origins[0].session_storage_keys, 1);
    let raw = serde_json::to_string(&metadata).unwrap();
    assert!(!raw.contains("fixture-secret"));
    assert!(!raw.contains("dark"));
    assert!(!raw.contains("step-value"));
    let mut empty = sample_state("empty");
    empty.origins.clear();
    assert!(
        serde_json::to_string(&metadata_for(&empty))
            .unwrap()
            .contains("\"origins\":null")
    );
}

#[test]
fn encrypted_key_material_requires_a_real_source() {
    assert!(KeyMaterial::new([0xab; 32], "").is_err());
    assert!(KeyMaterial::new([0xab; 32], "none").is_err());
}

#[test]
fn expiration_clean_and_remove_are_deterministic() {
    let root = test_root("clean");
    let store = Store::new(&root, Duration::days(1), None).unwrap();
    for (name, now) in [
        ("old", datetime!(2026-08-20 14:00 UTC)),
        ("fresh", datetime!(2026-08-25 14:00 UTC)),
    ] {
        let mut state = sample_state(name);
        store.save_at(&mut state, now).unwrap();
    }
    let now = datetime!(2026-08-25 15:00 UTC);
    assert_eq!(store.expired_at(now).unwrap(), ["old"]);
    assert_eq!(store.clean_at(now).unwrap(), ["old"]);
    assert_eq!(store.list().unwrap(), ["fresh"]);
    store.remove("fresh").unwrap();
    store.remove("fresh").unwrap();
    assert!(store.list().unwrap().is_empty());
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn clean_older_than_matches_saved_time() {
    let root = test_root("older-than");
    let store = Store::new(&root, Duration::days(30), None).unwrap();
    for (name, now) in [
        ("old", datetime!(2026-08-20 14:00 UTC)),
        ("fresh", datetime!(2026-08-25 14:00 UTC)),
    ] {
        let mut state = sample_state(name);
        store.save_at(&mut state, now).unwrap();
    }
    assert_eq!(
        store
            .clean_older_than_at(Duration::days(2), datetime!(2026-08-25 15:00 UTC))
            .unwrap(),
        ["old"]
    );
    assert!(
        store
            .clean_older_than_at(Duration::ZERO, datetime!(2026-08-25 15:00 UTC))
            .is_err()
    );
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn legacy_v2_retention_uses_header_without_a_key() {
    let root = test_root("v2-header");
    let store = Store::new(&root, Duration::days(30), None).unwrap();
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/state/encrypted-v2.state");
    fs::copy(fixture, root.join("legacy.json")).unwrap();
    assert_eq!(
        store.expired_at(datetime!(2026-10-01 00:00 UTC)).unwrap(),
        ["legacy"]
    );
    assert_eq!(
        store
            .clean_older_than_at(Duration::days(10), datetime!(2026-10-01 00:00 UTC))
            .unwrap(),
        ["legacy"]
    );
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn v3_retention_authenticates_encrypted_header() {
    let root = test_root("v3-header");
    let store = Store::new(&root, Duration::days(30), None).unwrap();
    let fixture = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/state/encrypted-v3.state");
    fs::copy(fixture, root.join("protected.json")).unwrap();
    assert!(store.expired_at(datetime!(2026-10-01 00:00 UTC)).is_err());
    fs::remove_dir_all(root).unwrap();
}

#[cfg(unix)]
#[test]
fn refuses_symlink_state_files() {
    use std::os::unix::fs::symlink;

    let root = test_root("symlink");
    let store = store(&root);
    let outside = root.with_extension("outside");
    fs::write(&outside, b"do-not-touch").unwrap();
    symlink(&outside, root.join("escape.json")).unwrap();
    assert!(store.load("escape").is_err());
    assert!(store.remove("escape").is_err());
    assert_eq!(fs::read(&outside).unwrap(), b"do-not-touch");
    fs::remove_file(root.join("escape.json")).unwrap();
    fs::remove_file(outside).unwrap();
    fs::remove_dir_all(root).unwrap();
}

#[cfg(unix)]
fn store(root: &PathBuf) -> Store {
    Store::new(root, Duration::days(30), None).unwrap()
}

fn sample_state(name: &str) -> State {
    State {
        schema_version: 1,
        name: name.to_owned(),
        saved_at: String::new(),
        expires_at: String::new(),
        key_source: String::new(),
        origins: BTreeMap::from([(
            "https://example.test".to_owned(),
            OriginState {
                cookies: vec![Cookie {
                    name: "session".to_owned(),
                    value: "fixture-secret".to_owned(),
                    domain: ".example.test".to_owned(),
                    path: "/".to_owned(),
                    expires: -1.0,
                    size: 21,
                    http_only: true,
                    secure: true,
                    session: true,
                    same_site: "Lax".to_owned(),
                }],
                local_storage: BTreeMap::from([("theme".to_owned(), "dark".to_owned())]),
                session_storage: BTreeMap::from([("step".to_owned(), "step-value".to_owned())]),
            },
        )]),
    }
}

fn test_root(name: &str) -> PathBuf {
    let id = NEXT_ROOT.fetch_add(1, Ordering::Relaxed);
    let root = std::env::temp_dir().join(format!(
        "symbrowse-state-store-{name}-{}-{id}",
        std::process::id()
    ));
    if root.exists() {
        fs::remove_dir_all(&root).unwrap();
    }
    root
}

fn assert_no_temporary_files(root: &PathBuf) {
    for entry in fs::read_dir(root).unwrap() {
        assert!(
            !entry
                .unwrap()
                .file_name()
                .to_string_lossy()
                .contains(".tmp")
        );
    }
}

#[cfg(unix)]
fn assert_private_modes(directory: &PathBuf, file: &PathBuf) {
    use std::os::unix::fs::PermissionsExt;
    assert_eq!(
        fs::metadata(directory).unwrap().permissions().mode() & 0o777,
        0o700
    );
    assert_eq!(
        fs::metadata(file).unwrap().permissions().mode() & 0o777,
        0o600
    );
}

#[cfg(not(unix))]
fn assert_private_modes(_directory: &PathBuf, _file: &PathBuf) {}
