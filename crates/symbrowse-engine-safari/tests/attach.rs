#![cfg(target_os = "macos")]

use std::{
    fs,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use symbrowse_core::policy::SsrfGuard;
use symbrowse_engine::capabilities::Capabilities;
use symbrowse_engine_safari::{
    ATTACH_ENGINE_KIND, AttachEngine, AttachError, NavigationPolicy, OsascriptRunner, ScriptRunner,
};

#[derive(Clone, Default)]
struct FakeRunner {
    calls: Arc<Mutex<Vec<String>>>,
    answer: String,
}
impl FakeRunner {
    fn with_answer(answer: &str) -> Self {
        Self {
            calls: Arc::new(Mutex::new(Vec::new())),
            answer: answer.to_owned(),
        }
    }
    fn calls(&self) -> Vec<String> {
        self.calls.lock().expect("calls lock").clone()
    }
}
impl ScriptRunner for FakeRunner {
    fn run(&self, script: &str, _timeout: Duration) -> Result<String, AttachError> {
        self.calls
            .lock()
            .expect("calls lock")
            .push(script.to_owned());
        Ok(self.answer.clone())
    }
}

#[test]
fn attach_is_noop_and_close_does_not_quit_safari() {
    let runner = FakeRunner::default();
    let mut engine = AttachEngine::new(runner.clone());
    engine.launch().expect("attach launch");
    engine.close().expect("close");
    engine.close().expect("idempotent close");
    assert!(runner.calls().is_empty());
    assert!(matches!(engine.new_context(), Err(AttachError::Closed)));
}

#[test]
fn attach_uses_named_tab_and_refuses_non_web_targets_before_runner() {
    let runner = FakeRunner::with_answer("\"https://example.test/\"");
    let engine = AttachEngine::new(runner.clone()).with_tab_name("Symaira");
    let page = engine.new_page(&engine.new_context().unwrap()).unwrap();
    assert!(engine.navigate(&page, "file:///etc/passwd").is_err());
    assert!(runner.calls().is_empty());
    let _ = engine.evaluate("document.title");
    assert!(
        runner.calls().is_empty(),
        "evaluation needs explicit opt-in"
    );
}

#[test]
fn attach_navigation_and_inspection_are_bounded_and_typed() {
    let runner = FakeRunner::with_answer("\"https://example.test/\"");
    let mut engine = AttachEngine::new(runner.clone())
        .with_navigation_timeout(Duration::from_millis(50))
        .with_poll_interval(Duration::from_millis(1));
    engine.set_interactions_opt_in(true);
    let page = engine.new_page(&engine.new_context().unwrap()).unwrap();
    let result = engine.navigate(&page, "https://example.test/").unwrap();
    assert_eq!(result.frame_id, "safari-live");
    assert_eq!(result.url, "https://example.test/");
    let calls = runner.calls();
    assert!(calls[0].contains("set URL of its document"));
    assert!(
        calls
            .iter()
            .any(|call| call.contains("tab \"Symaira\" of window 1"))
    );
    assert_eq!(
        engine.evaluate("document.title").unwrap(),
        serde_json::Value::String("https://example.test/".to_owned())
    );
    assert!(matches!(
        engine.evaluate("document.cookie"),
        Err(AttachError::Unsupported { .. })
    ));
}

#[test]
fn attach_capabilities_are_honest() {
    let mut engine = AttachEngine::new(FakeRunner::default());
    let read_only: Capabilities = engine.capabilities();
    assert_eq!(read_only.kind, ATTACH_ENGINE_KIND);
    assert_eq!(read_only.launch_mode, "attach");
    assert!(
        read_only
            .interfaces
            .iter()
            .any(|name| name == "InspectionEngine")
    );
    assert!(
        !read_only
            .interfaces
            .iter()
            .any(|name| name == "InteractionEngine")
    );
    engine.set_interactions_opt_in(true);
    assert!(
        !engine
            .capabilities()
            .interfaces
            .iter()
            .any(|name| name == "InteractionEngine")
    );
    engine.set_navigation_policy(NavigationPolicy::from_allowlist(&[
        "https://example.test".to_owned()
    ]));
    assert!(
        !engine
            .capabilities()
            .interfaces
            .iter()
            .any(|name| name == "InteractionEngine"),
        "invalid policy configuration must not advertise interactions"
    );
    engine.set_navigation_policy(NavigationPolicy::from_allowlist(&[
        "example.test".to_owned()
    ]));
    assert!(
        engine
            .capabilities()
            .interfaces
            .iter()
            .any(|name| name == "InteractionEngine")
    );
}

#[test]
fn attach_policy_denies_before_script_runner_and_ssrf_fails_closed() {
    let runner = FakeRunner::with_answer("\\\"https://allowed.example/\\\"");
    let policy = NavigationPolicy::from_allowlist(&["allowed.example".to_owned()]);
    let engine = AttachEngine::new(runner.clone()).with_navigation_policy(policy);
    let page = engine.new_page(&engine.new_context().unwrap()).unwrap();
    let error = engine
        .navigate(&page, "https://blocked.example/")
        .expect_err("blocked navigation");
    assert!(
        matches!(error, AttachError::InvalidTarget { reason, .. } if reason.contains("allowlist"))
    );
    assert!(runner.calls().is_empty(), "denied URL reached ScriptRunner");

    let ssrf = SsrfGuard::with_lookup(false, |_host| Ok(vec!["192.168.1.10".to_owned()]));
    let runner = FakeRunner::default();
    let engine = AttachEngine::new(runner.clone())
        .with_navigation_policy(NavigationPolicy::new().with_ssrf_guard(ssrf));
    let page = engine.new_page(&engine.new_context().unwrap()).unwrap();
    let error = engine
        .navigate(&page, "https://public.example/")
        .expect_err("SSRF-denied navigation");
    assert!(matches!(error, AttachError::InvalidTarget { reason, .. } if reason.contains("SSRF")));
    assert!(runner.calls().is_empty(), "denied URL reached ScriptRunner");

    let ssrf = SsrfGuard::with_lookup(false, |_host| Err("resolver unavailable".to_owned()));
    let runner = FakeRunner::default();
    let engine = AttachEngine::new(runner.clone())
        .with_navigation_policy(NavigationPolicy::new().with_ssrf_guard(ssrf));
    let page = engine.new_page(&engine.new_context().unwrap()).unwrap();
    let error = engine
        .navigate(&page, "https://unresolvable.example/")
        .expect_err("resolution failure must deny navigation");
    assert!(matches!(error, AttachError::InvalidTarget { reason, .. } if reason.contains("SSRF")));
    assert!(
        runner.calls().is_empty(),
        "resolution failure reached ScriptRunner"
    );
}

#[test]
fn attach_prerequisites_are_distinct_and_never_enable_permissions() {
    for (token, expected) in [
        ("application_unavailable", "Safari application unavailable"),
        (
            "automation_permission_denied",
            "Safari Automation permission denied",
        ),
        ("window_unavailable", "Safari selected window unavailable"),
        ("tab_unavailable", "Safari selected tab unavailable"),
    ] {
        let engine = AttachEngine::new(FakeRunner::with_answer(token));
        let error = engine
            .check_prerequisites()
            .expect_err("blocked prerequisite");
        assert!(error.to_string().contains(expected));
    }
}

#[test]
fn attach_accepts_a_stable_redirect_url_and_times_out_unsettled_navigation() {
    let redirect = FakeRunner::with_answer("\"https://example.test/final\"");
    let engine = AttachEngine::new(redirect)
        .with_navigation_timeout(Duration::from_millis(50))
        .with_poll_interval(Duration::from_millis(1));
    let page = engine.new_page(&engine.new_context().unwrap()).unwrap();
    let result = engine
        .navigate(&page, "https://example.test/start")
        .unwrap();
    assert_eq!(result.url, "https://example.test/final");

    let hanging = FakeRunner::default();
    let engine = AttachEngine::new(hanging)
        .with_navigation_timeout(Duration::from_millis(10))
        .with_poll_interval(Duration::from_millis(1));
    let page = engine.new_page(&engine.new_context().unwrap()).unwrap();
    assert!(matches!(
        engine.navigate(&page, "https://example.test/start"),
        Err(AttachError::NavigationDidNotSettle { .. })
    ));
}

#[test]
fn osascript_runner_kills_descendant_holding_stdout() {
    use std::os::unix::fs::PermissionsExt;

    let suffix = format!(
        "{}-{}",
        std::process::id(),
        Instant::now().elapsed().as_nanos()
    );
    let path = std::env::temp_dir().join(format!("symbrowse-safari-descendant-{suffix}"));
    let marker = std::env::temp_dir().join(format!("symbrowse-safari-descendant-pid-{suffix}"));
    let _ = fs::remove_file(&path);
    let _ = fs::remove_file(&marker);
    fs::write(
        &path,
        format!(
            "#!/bin/sh\n(sleep 60) &\nprintf '%s' \"$!\" > '{}'\nexit 0\n",
            marker.display()
        ),
    )
    .expect("write descendant helper");
    let mut permissions = fs::metadata(&path).expect("helper metadata").permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&path, permissions).expect("make helper executable");

    let started = Instant::now();
    let result = OsascriptRunner::new()
        .with_program(&path)
        .run("script delivered on stdin", Duration::from_millis(300));
    let elapsed = started.elapsed();
    assert!(
        result.is_err(),
        "descendant-held stdout must not report success"
    );
    assert!(
        elapsed < Duration::from_secs(2),
        "runner exceeded bound: {elapsed:?}"
    );

    let descendant = fs::read_to_string(&marker)
        .expect("helper recorded descendant pid")
        .trim()
        .to_owned();
    let mut dead = false;
    for _ in 0..50 {
        let status = std::process::Command::new("/bin/kill")
            .args(["-0", &descendant])
            .stderr(std::process::Stdio::null())
            .status()
            .expect("probe descendant");
        if !status.success() {
            dead = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    assert!(
        dead,
        "descendant {descendant} survived process-group cleanup"
    );
    let _ = fs::remove_file(path);
    let _ = fs::remove_file(marker);
}

#[test]
fn osascript_runner_drains_large_output_and_enforces_bound() {
    use std::os::unix::fs::PermissionsExt;

    let path = std::env::temp_dir().join(format!(
        "symbrowse-safari-osascript-test-{}",
        std::process::id()
    ));
    let _ = fs::remove_file(&path);
    fs::write(
        &path,
        "#!/bin/sh\ndd if=/dev/zero bs=100000 count=1 2>/dev/null | tr '\\000' x\n",
    )
    .expect("write helper");
    let mut permissions = fs::metadata(&path).expect("helper metadata").permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&path, permissions).expect("make helper executable");

    let runner = OsascriptRunner::new()
        .with_program(&path)
        .with_max_output_bytes(100_000);
    let output = runner
        .run("ignored", Duration::from_secs(2))
        .expect("large output should drain without deadlock");
    assert_eq!(output.len(), 100_000);

    let bounded = OsascriptRunner::new()
        .with_program(&path)
        .with_max_output_bytes(99_999);
    assert!(matches!(
        bounded.run("ignored", Duration::from_secs(2)),
        Err(AttachError::Runner { message }) if message.contains("stdout exceeded")
    ));
    fs::remove_file(path).expect("remove helper");
}

#[test]
fn osascript_runner_bounds_blocked_stdin_and_script_size() {
    use std::os::unix::fs::PermissionsExt;

    let path = std::env::temp_dir().join(format!(
        "symbrowse-safari-stdin-test-{}",
        std::process::id()
    ));
    let _ = fs::remove_file(&path);
    fs::write(&path, "#!/bin/sh\nsleep 60\n").expect("write helper");
    let mut permissions = fs::metadata(&path).expect("helper metadata").permissions();
    permissions.set_mode(0o755);
    fs::set_permissions(&path, permissions).expect("make helper executable");

    let runner = OsascriptRunner::new().with_program(&path);
    let started = Instant::now();
    assert!(matches!(
        runner.run(&"x".repeat(128 * 1024), Duration::from_millis(100)),
        Err(AttachError::TimedOut { .. })
    ));
    assert!(started.elapsed() < Duration::from_secs(2));
    assert!(matches!(
        runner.run(&"x".repeat(1024 * 1024 + 1), Duration::from_secs(1)),
        Err(AttachError::Runner { message }) if message.contains("script exceeded")
    ));
    fs::remove_file(path).expect("remove helper");
}
