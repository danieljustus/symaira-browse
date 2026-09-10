#![deny(unsafe_code)]

use std::{
    collections::{BTreeMap, HashMap},
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use serde::Deserialize;
use symbrowse_core::config::{Field, FlagOverrides, LoadContext, load, show_fields};

static NEXT_ROOT: AtomicU64 = AtomicU64::new(1);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle: Oracle,
    cases: Vec<Case>,
    invalid: Vec<InvalidCase>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    fields: BTreeMap<String, Field>,
}

#[derive(Deserialize)]
struct InvalidCase {
    name: String,
    error: String,
}

fn fixture() -> Fixture {
    const CONTENT: &[u8] = include_bytes!("../../../testdata/port/core/config-contract.json");
    serde_json::from_slice(CONTENT).expect("decode Go-generated config fixture")
}

#[test]
fn config_precedence_matches_go() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(fixture.oracle.release, "v0.8.0");
    for expected in fixture.cases {
        let root = test_root(&expected.name);
        let context = match expected.name.as_str() {
            "defaults" => context(&root),
            "precedence" => precedence_context(&root),
            other => panic!("unknown config case {other}"),
        };
        let result = load(&context).expect("load Rust configuration");
        assert_eq!(
            normalize(show_fields(&result), &root),
            expected.fields,
            "{}",
            expected.name
        );
        fs::remove_dir_all(root).expect("remove config fixture root");
    }
}

#[test]
fn config_validation_errors_match_go() {
    for expected in fixture().invalid {
        let root = test_root(&expected.name);
        let mut context = precedence_context(&root);
        match expected.name.as_str() {
            "engine" => {
                context
                    .env
                    .insert("SYMBROWSE_ENGINE".to_owned(), "wat".to_owned());
            }
            "autosave" => {
                context
                    .env
                    .insert("SYMBROWSE_ENGINE".to_owned(), "chrome".to_owned());
                context
                    .env
                    .insert("SYMBROWSE_AUTOSAVE".to_owned(), "sometimes".to_owned());
            }
            "timeout" => {
                context
                    .env
                    .insert("SYMBROWSE_ENGINE".to_owned(), "chrome".to_owned());
                context
                    .env
                    .insert("SYMBROWSE_AUTOSAVE".to_owned(), "auto".to_owned());
                context
                    .env
                    .insert("SYMBROWSE_READ_TIMEOUT".to_owned(), "0".to_owned());
            }
            other => panic!("unknown invalid config case {other}"),
        }
        let actual = load(&context).expect_err("invalid configuration must fail");
        assert_eq!(actual.to_string(), expected.error, "{}", expected.name);
        fs::remove_dir_all(root).expect("remove config fixture root");
    }
}

#[test]
fn config_show_omits_encryption_key_material() {
    const MARKER: &str = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
    let root = test_root("redaction");
    let mut context = context(&root);
    context
        .env
        .insert("SYMBROWSE_ENCRYPTION_KEY".to_owned(), MARKER.to_owned());
    let result = load(&context).expect("load configuration with encryption key environment");
    let json = serde_json::to_string(&show_fields(&result)).expect("serialize show fields");
    assert!(
        !json.contains(MARKER),
        "config show exposed encryption key material"
    );
    fs::remove_dir_all(root).expect("remove config fixture root");
}

fn context(root: &Path) -> LoadContext {
    let home = root.join("home");
    let cwd = root.join("workspace");
    for directory in [&home, &cwd] {
        fs::create_dir_all(directory).expect("create fixture directory");
    }
    LoadContext {
        home,
        cwd,
        xdg_config_home: Some(root.join("xdg-config")),
        xdg_cache_home: Some(root.join("xdg-cache")),
        xdg_state_home: Some(root.join("xdg-state")),
        env: HashMap::new(),
        flags: FlagOverrides::default(),
    }
}

fn precedence_context(root: &Path) -> LoadContext {
    let mut context = context(root);
    let global = context
        .xdg_config_home
        .as_ref()
        .unwrap()
        .join("symbrowse/config.toml");
    fs::create_dir_all(global.parent().unwrap()).expect("create global config directory");
    fs::write(
        global,
        "log_level = \"info\"\nstate_dir = \"global-state\"\nread_timeout = 77\nallowed_domains = [\"global.example\"]\n",
    )
    .expect("write global config");
    fs::write(
        context.cwd.join(".symbrowse.toml"),
        "log_level = \"debug\"\nstate_dir = \"project-state\"\noperation_timeout = 44\nengine = \"static\"\n",
    )
    .expect("write project config");
    context.env.extend([
        ("SYMBROWSE_LOG_LEVEL".to_owned(), "error".to_owned()),
        ("SYMBROWSE_READ_TIMEOUT".to_owned(), "90".to_owned()),
        (
            "SYMBROWSE_ALLOWED_DOMAINS".to_owned(),
            "env.example, *.env.example".to_owned(),
        ),
        ("SYMBROWSE_HEADLESS".to_owned(), "true".to_owned()),
    ]);
    context.flags = FlagOverrides {
        log_level: Some("trace".to_owned()),
        state_dir: Some("flag-state".to_owned()),
        cache_dir: Some("flag-cache".to_owned()),
        ..FlagOverrides::default()
    };
    context
}

fn normalize(mut fields: BTreeMap<String, Field>, root: &Path) -> BTreeMap<String, Field> {
    let raw = root.to_string_lossy();
    let canonical = root
        .canonicalize()
        .unwrap_or_else(|_| PathBuf::from(root))
        .to_string_lossy()
        .into_owned();
    for field in fields.values_mut() {
        field.value = field.value.replace(&canonical, "<ROOT>");
        field.value = field.value.replace(raw.as_ref(), "<ROOT>");
        field.value = field.value.replace('\\', "/");
    }
    fields
}

fn test_root(name: &str) -> PathBuf {
    let id = NEXT_ROOT.fetch_add(1, Ordering::Relaxed);
    let root = std::env::temp_dir().join(format!(
        "symbrowse-rust-config-{name}-{}-{id}",
        std::process::id()
    ));
    if root.exists() {
        fs::remove_dir_all(&root).expect("clear stale fixture root");
    }
    fs::create_dir_all(&root).expect("create fixture root");
    root
}
