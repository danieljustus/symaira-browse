#![cfg(target_os = "macos")]

use std::{
    path::Path,
    sync::{Arc, Mutex},
};

use serde_json::{Value, json};
use symbrowse_core::policy::SsrfGuard;
use symbrowse_engine::capabilities::OPTIONAL_INTERFACE_NAMES;
use symbrowse_engine::{EvaluationResult, Page};
use symbrowse_engine_safari::{
    BIDI_ENGINE_KIND, BidiEngine, BidiError, BidiTransport, BoxFuture, DriverOptions,
    NavigationPolicy, ProcessAdapter, ProcessHandle, TransportConnector, parse_session_response,
    require_loopback, session_request,
};

#[derive(Clone, Default)]
struct FakeTransport {
    calls: Arc<Mutex<Vec<String>>>,
    close_count: Arc<Mutex<usize>>,
}
impl FakeTransport {
    fn calls(&self) -> Vec<String> {
        self.calls.lock().expect("calls lock").clone()
    }
}
impl BidiTransport for FakeTransport {
    fn command<'a>(&'a mut self, method: &'a str, _params: Value) -> BoxFuture<'a, Value> {
        self.calls
            .lock()
            .expect("calls lock")
            .push(method.to_owned());
        Box::pin(async move {
            match method {
                "browsingContext.navigate" => Ok(json!({"navigation": "nav-1"})),
                "script.evaluate" => Ok(json!({"result": {"type": "string", "value": "ok"}})),
                _ => Ok(json!({})),
            }
        })
    }
    fn close<'a>(&'a mut self) -> BoxFuture<'a, ()> {
        *self.close_count.lock().expect("close lock") += 1;
        Box::pin(async { Ok(()) })
    }
}

#[test]
fn bidi_capabilities_report_only_implemented_go_interfaces_without_transport() {
    let fake = FakeTransport::default();
    let engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1");
    let caps = BidiEngine::planned_capabilities();
    assert_eq!(engine.capabilities(), caps);
    assert_eq!(caps.kind, BIDI_ENGINE_KIND);
    assert_eq!(caps.launch_mode, "launch");
    // Go also has tabs/frames (internal/engine/safaribidi/tabs.go).
    // They remain unsupported in this Rust slice; never invent Go parity.
    assert_eq!(
        caps.interfaces,
        [
            "InspectionEngine",
            "NavigationStateProvider",
            "NetworkPolicyReporter",
        ]
    );
    assert_eq!(
        caps.unsupported,
        [
            "A11yAuditor",
            "AXSelectorResolver",
            "ClickDiagnosticEngine",
            "CookieEngine",
            "DialogController",
            "FileTransfer",
            "FrameManager",
            "InteractionEngine",
            "NetworkEvents",
            "OverlayHost",
            "RuntimeEvents",
            "ScreenshotEngine",
            "ScreenshotOptionsEngine",
            "ScriptDisabler",
            "SettingsEngine",
            "TabManager",
        ]
    );
    for name in OPTIONAL_INTERFACE_NAMES {
        assert_eq!(
            caps.interfaces
                .iter()
                .chain(&caps.unsupported)
                .filter(|item| *item == name)
                .count(),
            1,
            "{name} must occur in exactly one capability partition"
        );
    }
    assert!(
        fake.calls().is_empty(),
        "capabilities must not call the transport"
    );
    assert_eq!(*fake.close_count.lock().expect("close lock"), 0);
}

#[test]
fn session_request_enables_both_bidi_capabilities() {
    let request = session_request();
    let always = &request["capabilities"]["alwaysMatch"];
    assert_eq!(always["browserName"], "safari");
    assert_eq!(always["webSocketUrl"], true);
    assert_eq!(always["safari:experimentalWebSocketUrl"], true);
}

#[test]
fn session_response_rejects_safari_boolean_socket() {
    let body = json!({"value": {"sessionId": "session-1", "capabilities": {"webSocketUrl": true}}});
    assert!(matches!(
        parse_session_response(200, &body),
        Err(BidiError::NoBidiSocket { .. })
    ));
    let good = json!({"value": {"sessionId": "session-1", "capabilities": {"webSocketUrl": "ws://127.0.0.1:1234/session"}}});
    let parsed = parse_session_response(200, &good).expect("valid session");
    assert_eq!(parsed.session_id, "session-1");
}

#[test]
fn loopback_boundary_rejects_foreign_socket() {
    assert!(require_loopback("ws://127.0.0.1:8091/session/x").is_ok());
    assert!(require_loopback("wss://[::1]:8091/session/x").is_ok());
    assert!(matches!(
        require_loopback("http://127.0.0.1:8091/session/x"),
        Err(BidiError::NonLoopback { .. })
    ));
    assert!(matches!(
        require_loopback("ws://user:password@127.0.0.1:8091/session/x"),
        Err(BidiError::NonLoopback { .. })
    ));
    assert!(matches!(
        require_loopback("ws://127.0.0.1/session/x"),
        Err(BidiError::NonLoopback { .. })
    ));
    assert!(matches!(
        require_loopback("not a websocket endpoint"),
        Err(BidiError::Driver { .. })
    ));
}

#[tokio::test]
async fn bidi_policy_denies_before_transport() {
    let fake = FakeTransport::default();
    let policy = NavigationPolicy::from_allowlist(&["allowed.example".to_owned()]);
    let mut engine =
        BidiEngine::from_transport(Box::new(fake.clone()), "page-1").with_navigation_policy(policy);
    let page = engine.new_page().expect("page");
    let error = engine
        .navigate(&page, "https://blocked.example/")
        .await
        .expect_err("blocked navigation");
    assert!(
        matches!(error, BidiError::InvalidTarget { reason, .. } if reason.contains("allowlist"))
    );
    assert!(fake.calls().is_empty(), "denied URL reached BidiTransport");

    let fake = FakeTransport::default();
    let ssrf = SsrfGuard::with_lookup(false, |_host| Ok(vec!["127.0.0.1".to_owned()]));
    let mut engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1")
        .with_navigation_policy(NavigationPolicy::new().with_ssrf_guard(ssrf));
    let page = engine.new_page().expect("page");
    let error = engine
        .navigate(&page, "https://public.example/")
        .await
        .expect_err("SSRF-denied navigation");
    assert!(matches!(error, BidiError::InvalidTarget { reason, .. } if reason.contains("SSRF")));
    assert!(fake.calls().is_empty(), "denied URL reached BidiTransport");
}

#[tokio::test]
async fn bidi_navigation_evaluation_and_cleanup_use_injected_transport() {
    let fake = FakeTransport::default();
    let close_count = fake.close_count.clone();
    let mut engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1");
    let page = engine.new_page().expect("page");
    let navigation = engine
        .navigate(&page, "https://example.test/")
        .await
        .expect("navigate");
    assert_eq!(navigation.loader_id, "nav-1");
    assert!(navigation.url.is_empty());
    let result: EvaluationResult = engine
        .evaluate(&page, "document.title")
        .await
        .expect("evaluate");
    assert_eq!(result.value_type, "string");
    assert_eq!(result.value, Some(json!("ok")));
    assert_eq!(engine.capabilities().kind, BIDI_ENGINE_KIND);
    assert!(
        engine
            .capabilities()
            .unsupported
            .iter()
            .any(|name| name == "CookieEngine")
    );
    assert!(
        engine
            .capabilities()
            .unsupported
            .iter()
            .any(|name| name == "NetworkEvents")
    );
    assert!(engine.screenshot().is_err());
    engine.close().await.expect("close");
    engine.close().await.expect("idempotent close");
    assert_eq!(*close_count.lock().expect("close lock"), 1);
    assert_eq!(
        fake.calls(),
        vec!["browsingContext.navigate", "script.evaluate"]
    );
}

#[tokio::test]
async fn failed_driver_startup_reaps_the_process_handle() {
    #[derive(Clone, Default)]
    struct Probe {
        kills: Arc<Mutex<usize>>,
        waits: Arc<Mutex<usize>>,
    }
    struct ExitedProcess {
        probe: Probe,
    }
    impl ProcessHandle for ExitedProcess {
        fn try_wait(&mut self) -> Result<Option<i32>, BidiError> {
            Ok(Some(7))
        }
        fn kill(&mut self) -> Result<(), BidiError> {
            *self.probe.kills.lock().expect("kill lock") += 1;
            Ok(())
        }
        fn wait(&mut self) -> Result<Option<i32>, BidiError> {
            *self.probe.waits.lock().expect("wait lock") += 1;
            Ok(Some(7))
        }
    }
    struct ExitedAdapter {
        probe: Probe,
    }
    impl ProcessAdapter for ExitedAdapter {
        fn spawn(
            &self,
            _program: &Path,
            _args: &[String],
        ) -> Result<Box<dyn ProcessHandle>, BidiError> {
            Ok(Box::new(ExitedProcess {
                probe: self.probe.clone(),
            }))
        }
    }
    struct NeverConnector;
    impl TransportConnector for NeverConnector {
        fn connect<'a>(
            &'a self,
            _endpoint: &'a str,
            _timeout: std::time::Duration,
        ) -> BoxFuture<'a, Box<dyn BidiTransport>> {
            Box::pin(async {
                Err(BidiError::Driver {
                    operation: "unused connector".to_owned(),
                    message: String::new(),
                })
            })
        }
    }

    let probe = Probe::default();
    let adapter = ExitedAdapter {
        probe: probe.clone(),
    };
    let error =
        BidiEngine::launch_with_adapters(DriverOptions::default(), &adapter, &NeverConnector)
            .await
            .err()
            .expect("exited driver");
    assert!(
        matches!(error, BidiError::Driver { operation, .. } if operation == "wait for readiness")
    );
    assert_eq!(*probe.kills.lock().expect("kill lock"), 1);
    assert_eq!(*probe.waits.lock().expect("wait lock"), 1);
}

#[tokio::test]
async fn bidi_commands_are_bounded_by_request_timeout() {
    struct HangingTransport;
    impl BidiTransport for HangingTransport {
        fn command<'a>(&'a mut self, _method: &'a str, _params: Value) -> BoxFuture<'a, Value> {
            Box::pin(async {
                tokio::time::sleep(std::time::Duration::from_secs(5)).await;
                Ok(Value::Null)
            })
        }
        fn close<'a>(&'a mut self) -> BoxFuture<'a, ()> {
            Box::pin(async { Ok(()) })
        }
    }

    let mut engine = BidiEngine::from_transport_with_timeout(
        Box::new(HangingTransport),
        "page-1",
        std::time::Duration::from_millis(30),
    );
    let page = engine.new_page().expect("page");
    let started = std::time::Instant::now();
    let error = engine
        .evaluate(&page, "document.title")
        .await
        .expect_err("hanging command");
    assert!(started.elapsed() < std::time::Duration::from_secs(1));
    assert!(
        matches!(error, BidiError::Timeout { operation, .. } if operation.contains("script.evaluate"))
    );
}

#[tokio::test]
async fn bidi_protocol_errors_remain_typed() {
    struct ErrorTransport;
    impl BidiTransport for ErrorTransport {
        fn command<'a>(&'a mut self, method: &'a str, _params: Value) -> BoxFuture<'a, Value> {
            Box::pin(async move {
                Err(BidiError::Protocol {
                    method: method.to_owned(),
                    code: "unknown command".to_owned(),
                    message: "input domain was not found".to_owned(),
                })
            })
        }
        fn close<'a>(&'a mut self) -> BoxFuture<'a, ()> {
            Box::pin(async { Ok(()) })
        }
    }
    let mut engine = BidiEngine::from_transport(Box::new(ErrorTransport), "page-1");
    let error = engine
        .evaluate(
            &Page {
                id: "page-1".to_owned(),
                session_id: String::new(),
            },
            "1",
        )
        .await
        .expect_err("protocol error");
    assert!(matches!(error, BidiError::Protocol { code, .. } if code == "unknown command"));
}

#[tokio::test]
async fn real_safari_bidi_launch_is_opt_in_or_reports_typed_blocked_gate() {
    let native =
        std::env::var_os("SYMBROWSE_NATIVE_TARGETS").as_deref() == Some(std::ffi::OsStr::new("1"));
    let options = if native {
        DriverOptions::default()
    } else {
        DriverOptions {
            // A non-directory parent guarantees a missing driver without
            // probing Safari or its Remote Automation permissions.
            driver_path: Path::new("/dev/null/safaridriver").to_path_buf(),
            ready_timeout: std::time::Duration::from_millis(100),
            session_timeout: std::time::Duration::from_millis(100),
            request_timeout: std::time::Duration::from_millis(100),
            ..DriverOptions::default()
        }
    };
    let result = BidiEngine::launch(options).await;
    if native {
        let mut engine = result.expect("launch isolated safaridriver BiDi session");
        let page = engine.new_page().expect("initial Safari automation page");
        let title = engine
            .evaluate(&page, "document.title")
            .await
            .expect("evaluate document.title in Safari")
            .value
            .expect("Safari returned a title value");
        assert_eq!(title, "");
        engine.close().await.expect("close isolated Safari session");
    } else {
        assert!(
            matches!(result, Err(BidiError::Prerequisite { .. })),
            "native gate must be typed, not skipped"
        );
    }
}

// Go oracle: safaribidi_test.go policy and aggregation cases; state.go sorting.
#[tokio::test]
async fn policy_reports_sorted_owned_history_and_preserves_first_reason() {
    let fake = FakeTransport::default();
    let mut engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1")
        .with_navigation_policy(NavigationPolicy::from_allowlist(
            &["allowed.example".into()],
        ));
    let page = engine.new_page().expect("page");
    assert!(engine.blocked_requests().is_empty());
    for url in [
        "https://z.example/",
        " https://a.example/ ",
        "https://a.example/",
    ] {
        assert!(engine.navigate(&page, url).await.is_err());
    }
    let mut snapshot = engine.blocked_requests();
    assert_eq!(
        serde_json::to_value(&snapshot).expect("JSON"),
        json!([
            {"url":"https://a.example/", "resource_type":"document", "count":2, "reason":"domain allowlist"},
            {"url":"https://z.example/", "resource_type":"document", "count":1, "reason":"domain allowlist"}
        ])
    );
    snapshot[0].count = 99;
    engine.set_navigation_policy(
        NavigationPolicy::new().with_ssrf_guard(SsrfGuard::with_lookup(false, |_| {
            Ok(vec!["127.0.0.1".into()])
        })),
    );
    assert!(engine.navigate(&page, "https://a.example/").await.is_err());
    assert!(engine.navigate(&page, "https://b.example/").await.is_err());
    let entries = engine.blocked_requests();
    assert_eq!(entries[0].count, 3);
    assert_eq!(entries[0].reason, "domain allowlist");
    assert_eq!(entries[1].reason, "ssrf guard");
    assert_eq!(entries[2].url, "https://z.example/");
    assert_eq!(
        engine.blocked_requests(),
        entries,
        "reporting must not drain"
    );
    assert!(fake.calls().is_empty());
}

#[tokio::test]
async fn policy_does_not_record_invalid_targets_configuration_or_closed_engine() {
    let fake = FakeTransport::default();
    let mut engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1")
        .with_navigation_policy(NavigationPolicy::from_allowlist(&[
            "https://invalid.example".into(),
        ]));
    let page = engine.new_page().expect("page");
    for url in [
        "",
        "not a URL",
        "about:blank",
        "file:///tmp/test",
        "https://example.test/",
    ] {
        assert!(matches!(
            engine.navigate(&page, url).await,
            Err(BidiError::InvalidTarget { .. })
        ));
    }
    assert!(engine.blocked_requests().is_empty());
    engine.close().await.expect("close");
    assert_eq!(
        engine.navigate(&page, "https://example.test/").await,
        Err(BidiError::Closed)
    );
    assert!(engine.blocked_requests().is_empty());
    assert!(fake.calls().is_empty());
}

#[tokio::test]
async fn policy_does_not_record_allowed_navigation_or_transport_failure() {
    struct FailedNavigation;
    impl BidiTransport for FailedNavigation {
        fn command<'a>(&'a mut self, method: &'a str, params: Value) -> BoxFuture<'a, Value> {
            assert_eq!(method, "browsingContext.navigate");
            assert_eq!(
                params,
                json!({"context":"page-1", "url":"https://allowed.example/", "wait":"complete"})
            );
            Box::pin(async {
                Err(BidiError::Protocol {
                    method: "browsingContext.navigate".into(),
                    code: "unknown error".into(),
                    message: "fixture failure".into(),
                })
            })
        }
        fn close<'a>(&'a mut self) -> BoxFuture<'a, ()> {
            Box::pin(async { Ok(()) })
        }
    }
    let mut failed = BidiEngine::from_transport(Box::new(FailedNavigation), "page-1");
    let page = failed.new_page().expect("page");
    assert!(matches!(
        failed.navigate(&page, "https://allowed.example/").await,
        Err(BidiError::Protocol { .. })
    ));
    assert!(failed.blocked_requests().is_empty());
    let fake = FakeTransport::default();
    let mut allowed = BidiEngine::from_transport(Box::new(fake.clone()), "page-1");
    allowed
        .navigate(&page, "https://allowed.example/")
        .await
        .expect("navigate");
    assert!(allowed.blocked_requests().is_empty());
    assert_eq!(fake.calls(), ["browsingContext.navigate"]);
}

#[test]
fn policy_limitation_matches_current_go_oracle_without_transport() {
    let fake = FakeTransport::default();
    let engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1");
    let oracle = include_str!("../../../internal/engine/safaribidi/state.go");
    assert_eq!(engine.limitations().len(), 1);
    assert!(oracle.contains(&format!("\"{}\"", engine.limitations()[0])));
    assert!(fake.calls().is_empty());
}
