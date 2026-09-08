#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use serde::Deserialize;
use symbrowse_core::state::{State, StateError, decode, decrypt, encrypt};

#[derive(Deserialize)]
struct Manifest {
    schema_version: u32,
    oracle: Oracle,
    key_hex: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    path: String,
    version: u32,
    encrypted: bool,
    expected: State,
}

fn fixture_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/state")
}

fn manifest() -> Manifest {
    serde_json::from_slice(&fs::read(fixture_root().join("manifest.json")).unwrap()).unwrap()
}

#[test]
fn reads_all_go_generated_state_versions() {
    let manifest = manifest();
    assert_eq!(manifest.schema_version, 1);
    assert_eq!(
        manifest.oracle.commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(manifest.oracle.release, "v0.8.0");
    assert_eq!(manifest.cases.len(), 6);
    let key = decode_hex(&manifest.key_hex);
    for case in manifest.cases {
        let raw = fs::read(fixture_root().join(&case.path)).unwrap();
        let actual = decode(&raw, case.encrypted.then_some(key.as_slice())).unwrap();
        assert_eq!(actual, case.expected, "{}", case.name);
    }
}

#[test]
fn encrypted_v3_authenticates_header_and_body() {
    let manifest = manifest();
    let key = decode_hex(&manifest.key_hex);
    let case = manifest
        .cases
        .iter()
        .find(|case| case.name == "encrypted-v3")
        .unwrap();
    let raw = fs::read(fixture_root().join(&case.path)).unwrap();

    let mut header_tampered = raw.clone();
    let saved = b"2026-08-25T14:00:00Z";
    let offset = header_tampered
        .windows(saved.len())
        .position(|window| window == saved)
        .unwrap();
    header_tampered[offset + 9] = b'6';
    assert_eq!(
        decode(&header_tampered, Some(&key)),
        Err(StateError::Decrypt)
    );

    let mut body_tampered = raw;
    *body_tampered.last_mut().unwrap() ^= 1;
    assert_eq!(decode(&body_tampered, Some(&key)), Err(StateError::Decrypt));
}

#[test]
fn encrypted_files_require_the_right_key() {
    let manifest = manifest();
    let key = decode_hex(&manifest.key_hex);
    for case in manifest.cases.into_iter().filter(|case| case.encrypted) {
        let raw = fs::read(fixture_root().join(&case.path)).unwrap();
        if case.version >= 2 {
            assert_eq!(decode(&raw, None), Err(StateError::KeyRequired));
        }
        let mut wrong_key = key.clone();
        wrong_key[0] ^= 1;
        assert_eq!(decode(&raw, Some(&wrong_key)), Err(StateError::Decrypt));
    }
}

#[test]
fn production_encryption_uses_fresh_random_nonces() {
    let key = [0xab; 32];
    let first = encrypt(b"state", b"header", &key).unwrap();
    let second = encrypt(b"state", b"header", &key).unwrap();
    assert_ne!(&first[..12], &second[..12]);
    assert_eq!(decrypt(&first, b"header", &key).unwrap(), b"state");
    assert_eq!(decrypt(&second, b"header", &key).unwrap(), b"state");
}

fn decode_hex(value: &str) -> Vec<u8> {
    assert_eq!(value.len() % 2, 0);
    value
        .as_bytes()
        .as_chunks::<2>()
        .0
        .iter()
        .map(|chunk| {
            let text = std::str::from_utf8(chunk).unwrap();
            u8::from_str_radix(text, 16).unwrap()
        })
        .collect()
}
