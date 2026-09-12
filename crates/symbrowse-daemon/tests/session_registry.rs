#![deny(unsafe_code)]
#![allow(clippy::result_large_err)]

//! DMN-007 assertions derived from oracle 652453d1595fc302bd69c328e7da8a21dbee28b9:
//! internal/daemon/{session_registry.go,session_registry_test.go,registry_protocol_test.go,path.go,server.go}.
//! These are source-derived regression tests, not generated differential goldens.

use std::{
    fs,
    path::PathBuf,
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
};
use symbrowse_daemon::{SessionError, SessionRegistry, SessionRegistryOptions};
use time::{OffsetDateTime, macros::datetime};

static NEXT: AtomicUsize = AtomicUsize::new(0);
const START: OffsetDateTime = datetime!(2026-08-03 14:00:00.123456789 +02:00);
const LATER: OffsetDateTime = datetime!(2026-08-03 12:05:00.120 UTC);

struct Root(PathBuf);
impl Root {
    fn new() -> Self {
        Self(std::env::temp_dir().join(format!(
            "sb-dmn007-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        )))
    }

    fn options(&self) -> SessionRegistryOptions {
        SessionRegistryOptions {
            user_data_root: self.0.join("profiles"),
            pid: 4242,
            scope: String::new(),
            origin_path: String::new(),
        }
    }
}
impl Drop for Root {
    fn drop(&mut self) {
        if self.0.exists() {
            fs::remove_dir_all(&self.0).unwrap();
        }
    }
}

fn clocked(
    root: &Root,
) -> (
    SessionRegistry,
    Arc<Mutex<OffsetDateTime>>,
    Arc<AtomicUsize>,
) {
    let now = Arc::new(Mutex::new(START));
    let calls = Arc::new(AtomicUsize::new(0));
    let clock = now.clone();
    let counter = calls.clone();
    let registry = SessionRegistry::with_clock(root.options(), move || {
        counter.fetch_add(1, Ordering::SeqCst);
        *clock.lock().unwrap()
    });
    (registry, now, calls)
}

#[test]
fn names_follow_go_regex_without_registry_defaulting() {
    let root = Root::new();
    let (registry, _, calls) = clocked(&root);
    // path.go: ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$; Ensure validates before I/O.
    for name in [
        "",
        ".",
        "..",
        "-a",
        "_a",
        "../escape",
        "a/b",
        "a\\b",
        "a b",
        "a\n",
        "é",
        &"a".repeat(65),
    ] {
        assert_eq!(
            registry.ensure(name).unwrap_err(),
            SessionError::InvalidName(name.into())
        );
    }
    assert!(!root.0.exists());
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    for name in ["a", "Z", "0", "default", "A.z_9-", &"a".repeat(64)] {
        assert_eq!(registry.ensure(name).unwrap().name, name);
    }
}

#[test]
fn ensure_and_touch_use_one_injected_instant_without_resetting_start() {
    let root = Root::new();
    let (registry, now, calls) = clocked(&root);
    assert!(!root.0.exists(), "construction must be lazy");
    let first = registry.ensure("alpha").unwrap();
    assert_eq!(first.started_at, "2026-08-03T12:00:00.123456789Z");
    assert_eq!(first.last_activity, first.started_at);
    *now.lock().unwrap() = LATER;
    registry.set_active_tabs("alpha", 2).unwrap();
    registry.set_ref("alpha", "save", "@e1").unwrap();
    let again = registry.ensure("alpha").unwrap();
    assert_eq!(again.started_at, first.started_at);
    assert_eq!(again.last_activity, first.last_activity);
    assert_eq!(again.active_tabs, 2);
    assert_eq!(again.ref_count, 1);
    registry.get("alpha").unwrap();
    registry.list_data();
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    registry.touch("alpha").unwrap();
    let touched = registry.get("alpha").unwrap();
    assert_eq!(touched.started_at, first.started_at);
    assert_eq!(touched.last_activity, "2026-08-03T12:05:00.12Z");
    assert_eq!(calls.load(Ordering::SeqCst), 2);
    *now.lock().unwrap() = datetime!(2026-08-03 12:06 UTC);
    registry.touch("alpha").unwrap();
    assert_eq!(
        registry.get("alpha").unwrap().last_activity,
        "2026-08-03T12:06:00Z"
    );
}

#[test]
fn list_and_info_serialize_go_fields_and_omit_empty_scope() {
    let root = Root::new();
    let (registry, _, _) = clocked(&root);
    assert_eq!(
        serde_json::to_string(&registry.list_data()).unwrap(),
        r#"{"schema_version":1,"sessions":[]}"#
    );
    registry.ensure("beta").unwrap();
    let alpha = registry.ensure("alpha").unwrap();
    // SessionInfo's explicit Go json tags and info() defaults, including zeros.
    let expected = serde_json::json!({
        "name": "alpha", "pid": 4242, "started_at": "2026-08-03T12:00:00.123456789Z",
        "active_tabs": 0, "last_activity": "2026-08-03T12:00:00.123456789Z",
        "user_data_dir": root.options().user_data_root.join("alpha"),
        "browser_context_id": "context-alpha", "ref_count": 0
    });
    assert_eq!(serde_json::to_value(alpha).unwrap(), expected);
    let data = serde_json::to_value(registry.list_data()).unwrap();
    assert_eq!(data["schema_version"], 1);
    assert_eq!(data["sessions"][0], expected);
    assert_eq!(data["sessions"][1]["name"], "beta");
    assert_eq!(data["sessions"].as_array().unwrap().len(), 2);
    let scoped = SessionRegistry::with_clock(
        SessionRegistryOptions {
            pid: 0,
            scope: "worktree".into(),
            origin_path: "/fixture/project".into(),
            ..root.options()
        },
        || START,
    );
    let info = serde_json::to_value(scoped.ensure("scoped").unwrap()).unwrap();
    assert_eq!(info["pid"], std::process::id());
    assert_eq!(info["scope"], "worktree");
    assert_eq!(info["origin_path"], "/fixture/project");
}

#[test]
fn references_are_isolated_and_clear_preserves_profiles() {
    let root = Root::new();
    let (registry, now, _) = clocked(&root);
    let alpha = registry.ensure("alpha").unwrap();
    let beta = registry.ensure("beta").unwrap();
    assert_ne!(alpha.user_data_dir, beta.user_data_dir);
    assert_ne!(alpha.browser_context_id, beta.browser_context_id);
    registry.set_ref("alpha", "save", "@e1").unwrap();
    assert_eq!(
        registry.reference("beta", "save").unwrap_err().to_string(),
        "ref \"save\" not found in session \"beta\""
    );
    registry.set_ref("beta", "save", "@e2").unwrap();
    registry.set_ref("alpha", "save", "@e3").unwrap();
    assert_eq!(registry.reference("alpha", "save").unwrap(), "@e3");
    assert_eq!(registry.reference("beta", "save").unwrap(), "@e2");
    assert_eq!(registry.get("alpha").unwrap().ref_count, 1);
    let marker = PathBuf::from(&alpha.user_data_dir).join("profile-marker");
    fs::write(&marker, b"fixture").unwrap();
    registry.clear();
    assert!(registry.list().is_empty());
    assert_eq!(
        registry.get("alpha").unwrap_err(),
        SessionError::NotFound("alpha".into())
    );
    assert!(PathBuf::from(beta.user_data_dir).is_dir());
    assert_eq!(fs::read(&marker).unwrap(), b"fixture");
    *now.lock().unwrap() = LATER;
    let fresh = registry.ensure("alpha").unwrap();
    assert_eq!(fresh.user_data_dir, alpha.user_data_dir);
    assert_eq!(fresh.started_at, "2026-08-03T12:05:00.12Z");
    assert_eq!(fresh.active_tabs, 0);
    assert_eq!(fresh.ref_count, 0);
    assert!(registry.reference("alpha", "save").is_err());
}

#[test]
fn missing_sessions_and_empty_refs_have_stable_errors_without_creation() {
    let root = Root::new();
    let (registry, _, calls) = clocked(&root);
    assert_eq!(
        registry.ensure("../escape").unwrap_err().to_string(),
        "invalid session name: \"../escape\""
    );
    // Only Ensure validates; Get/Touch/ref operations report missing names.
    for name in ["missing", "", "../escape"] {
        let expected = SessionError::NotFound(name.into());
        assert_eq!(registry.get(name).unwrap_err(), expected);
        assert_eq!(registry.touch(name).unwrap_err(), expected);
        assert_eq!(registry.set_active_tabs(name, 1).unwrap_err(), expected);
        assert_eq!(registry.set_ref(name, "save", "@e1").unwrap_err(), expected);
        assert_eq!(registry.reference(name, "save").unwrap_err(), expected);
    }
    assert_eq!(
        registry.get("missing").unwrap_err().to_string(),
        "session not found: \"missing\""
    );
    for (key, reference) in [("", "@e1"), ("save", ""), ("", "")] {
        assert_eq!(
            registry.set_ref("missing", key, reference).unwrap_err(),
            SessionError::InvalidValue("ref key and ref are required".into())
        );
    }
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    assert!(registry.list().is_empty());
    assert!(!root.0.exists());
}

#[cfg(unix)]
#[test]
fn ensure_secures_new_and_preexisting_profile_directories() {
    use std::os::unix::fs::PermissionsExt;
    let root = Root::new();
    let existing = root.options().user_data_root.join("existing");
    fs::create_dir_all(&existing).unwrap();
    fs::set_permissions(&existing, fs::Permissions::from_mode(0o755)).unwrap();
    let (registry, _, _) = clocked(&root);
    for name in ["new", "existing"] {
        let info = registry.ensure(name).unwrap();
        assert_eq!(
            fs::metadata(info.user_data_dir)
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o700
        );
    }
}

#[cfg(unix)]
#[test]
fn daemon_list_touches_only_requested_session_and_info_defaults_to_server_session() {
    use std::{
        io::{BufRead, BufReader, Write},
        os::unix::net::UnixStream,
        thread,
        time::{Duration, Instant},
    };
    use symbrowse_daemon::{Server, ServerOptions};
    let root = Root::new();
    let (registry, now, calls) = clocked(&root);
    registry.ensure("alpha").unwrap();
    registry.ensure("beta").unwrap();
    let registry = Arc::new(registry);
    let socket = root.0.join("daemon.sock");
    let server = Arc::new(
        Server::new(ServerOptions {
            socket_path: socket.clone(),
            session: "alpha".into(),
            idle_timeout: None,
            registry: Some(registry.clone()),
            handler: Some(Arc::new(|_, _| Ok((None, Vec::new())))),
            ..Default::default()
        })
        .unwrap(),
    );
    let running = server.clone();
    let worker = thread::spawn(move || running.listen_and_serve());
    let deadline = Instant::now() + Duration::from_secs(5);
    let stream = loop {
        if let Ok(stream) = UnixStream::connect(&socket) {
            break stream;
        }
        assert!(Instant::now() < deadline, "daemon did not listen");
        thread::sleep(Duration::from_millis(5));
    };
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    let mut reader = BufReader::new(stream);
    let mut request = |frame: &str| {
        writeln!(reader.get_mut(), "{frame}").unwrap();
        let mut line = String::new();
        reader.read_line(&mut line).unwrap();
        serde_json::from_str::<serde_json::Value>(&line).unwrap()
    };
    *now.lock().unwrap() = LATER;
    let list = request(r#"{"cmd":"session.list","session":"beta"}"#);
    assert_eq!(list["success"], true);
    assert_eq!(
        list["data"]["sessions"][0]["last_activity"],
        "2026-08-03T12:00:00.123456789Z"
    );
    assert_eq!(
        list["data"]["sessions"][1]["last_activity"],
        "2026-08-03T12:05:00.12Z"
    );
    let before = calls.load(Ordering::SeqCst);
    let unknown = request(r#"{"cmd":"session.list","session":"missing"}"#);
    assert_eq!(unknown, list);
    assert_eq!(calls.load(Ordering::SeqCst), before);
    let error = request(r#"{"cmd":"session.info","session":"missing"}"#);
    assert_eq!(
        error,
        serde_json::json!({"success":false,"error":{"code":"session_not_found","message":"session not found: \"missing\""}})
    );
    let invalid = request(r#"{"cmd":"session.info","session":"../escape"}"#);
    assert_eq!(
        invalid,
        serde_json::json!({"success":false,"error":{"code":"invalid_session","message":"invalid session \"../escape\""}})
    );
    let info = request(r#"{"cmd":"session.info"}"#);
    assert_eq!(info["success"], true);
    assert_eq!(info["data"]["name"], "alpha");
    assert_eq!(info["data"]["started_at"], "2026-08-03T12:00:00.123456789Z");
    assert_eq!(info["data"]["last_activity"], "2026-08-03T12:05:00.12Z");
    server.stop();
    drop(reader);
    worker.join().unwrap().unwrap();
    assert!(registry.list().is_empty());
}
