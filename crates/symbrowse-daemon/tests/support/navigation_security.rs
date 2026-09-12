use serde_json::{Value, json};
use std::{
    io::{BufRead, BufReader, Write},
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
    thread,
    time::Duration,
};
use symbrowse_daemon::{Frame, Server, ServerOptions, SessionSpec, Warning};

pub const CREDENTIAL_URL: &str = "https://private-user:private-password@blocked.example/path?token=private-token&password=query-password&view=public";
pub const SAFE_URL: &str = "https://example.com/path?view=public";
pub const BLOCKED_URL: &str = "https://blocked.example/path?view=public";
pub fn fixture() -> Value {
    serde_json::from_str(include_str!(
        "../../../../testdata/port/daemon/navigation-security.json"
    ))
    .unwrap()
}
pub fn assert_scrubbed(text: &str) {
    for secret in [
        "private-user",
        "private-password",
        "private-token",
        "query-password",
    ] {
        assert!(!text.contains(secret), "secret {secret} in {text}");
    }
}
pub struct Harness {
    pub socket: PathBuf,
    pub calls: Arc<AtomicUsize>,
    pub history: Arc<std::sync::Mutex<Vec<Warning>>>,
    server: Arc<Server>,
    worker: Option<thread::JoinHandle<Result<(), symbrowse_daemon::ServerError>>>,
    root: PathBuf,
}
impl Harness {
    #[allow(clippy::result_large_err)] // The existing DaemonHandler ABI returns DaemonError by value.
    pub fn new(label: &str) -> Self {
        let root = std::env::temp_dir().join(format!("browse-442-{label}-{}", std::process::id()));
        std::fs::create_dir_all(&root).unwrap();
        let mut spec = SessionSpec::for_session("boundary");
        spec.socket_path = root.join("daemon.sock");
        spec.state_dir = root.join("state");
        spec.cache_dir = root.join("cache");
        spec.allowed_domains = vec!["example.com".into()];
        let calls = Arc::new(AtomicUsize::new(0));
        let count = calls.clone();
        let history = Arc::new(std::sync::Mutex::new(vec![Warning {
            kind: "network_policy.limitation".into(),
            severity: "warning".into(),
            message: "fixture enforcement limitation".into(),
            ..Default::default()
        }]));
        let warnings = history.clone();
        let server = Arc::new(
            Server::new(ServerOptions {
                session_spec: Some(spec.clone()),
                handler: Some(Arc::new(move |frame, _| {
                    // Existing injectable engine/transport boundary: reaching this
                    // handler with an admission-denied target is itself a failure.
                    if matches!(frame.cmd.as_str(), "open" | "goto") {
                        count.fetch_add(1, Ordering::SeqCst);
                        assert_eq!(frame.args.as_ref().unwrap()["url"], SAFE_URL);
                    }
                    Ok((
                        Some(json!({"url":SAFE_URL})),
                        warnings.lock().unwrap().clone(),
                    ))
                })),
                ..Default::default()
            })
            .unwrap(),
        );
        let running = server.clone();
        let worker = thread::spawn(move || running.listen_and_serve());
        for _ in 0..200 {
            if spec.socket_path.exists() {
                break;
            }
            thread::sleep(Duration::from_millis(10));
        }
        assert!(spec.socket_path.exists());
        Self {
            socket: spec.socket_path,
            calls,
            history,
            server,
            worker: Some(worker),
            root,
        }
    }
    pub fn request(&self, command: &str, url: &str) -> Value {
        let mut stream = std::os::unix::net::UnixStream::connect(&self.socket).unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(5)))
            .unwrap();
        let frame = Frame {
            cmd: command.into(),
            session: "boundary".into(),
            args: Some(json!({"url":url})),
            ..Default::default()
        };
        writeln!(stream, "{}", serde_json::to_string(&frame).unwrap()).unwrap();
        let mut line = String::new();
        BufReader::new(stream).read_line(&mut line).unwrap();
        serde_json::from_str(&line).unwrap()
    }
    pub fn seed_engine_history(&self) {
        *self.history.lock().unwrap() = serde_json::from_value(
            fixture()["GO_KNOWN_DEFECT_442_CREDENTIAL_POLICY_WARNING"].clone(),
        )
        .unwrap();
    }
}
impl Drop for Harness {
    fn drop(&mut self) {
        self.server.stop();
        self.worker.take().unwrap().join().unwrap().unwrap();
        std::fs::remove_dir_all(&self.root).unwrap();
    }
}
