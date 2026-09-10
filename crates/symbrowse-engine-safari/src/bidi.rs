use std::{
    fmt,
    future::Future,
    path::{Path, PathBuf},
    pin::Pin,
    process::{Child, Command, Stdio},
    time::Duration,
};

use async_tungstenite::{tokio::connect_async, tungstenite::Message};
use futures_util::StreamExt;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use symbrowse_engine::capabilities::{Capabilities, capabilities_for};
use symbrowse_engine::{Context, EvaluationResult, NavigationResult, Page};
use tokio::time::timeout;
use url::Url;

use crate::attach::{NavigationPolicy, SafariPrerequisite};

pub const ENGINE_KIND: &str = "safari-bidi";
pub const DRIVER_PATH: &str = "/usr/bin/safaridriver";
const MAX_CONNECT_ATTEMPTS: usize = 4;

/// Typed failures from the Safari BiDi lifecycle and protocol.
#[derive(Debug, Clone, Eq, PartialEq)]
pub enum BidiError {
    Closed,
    Prerequisite {
        check: SafariPrerequisite,
        message: String,
    },
    NotLaunched,
    Unsupported {
        operation: String,
    },
    InvalidTarget {
        target: String,
        reason: String,
    },
    NonLoopback {
        endpoint: String,
    },
    Driver {
        operation: String,
        message: String,
    },
    SessionNotCreated {
        code: String,
        message: String,
    },
    NoBidiSocket {
        value: String,
    },
    Protocol {
        method: String,
        code: String,
        message: String,
    },
    Timeout {
        operation: String,
        timeout: Duration,
    },
}

impl fmt::Display for BidiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Closed => f.write_str("safari-bidi engine: closed"),
            Self::Prerequisite { check, message } => {
                write!(f, "safari-bidi prerequisite {check}: {message}")
            }
            Self::NotLaunched => f.write_str("safari-bidi engine: not launched"),
            Self::Unsupported { operation } => {
                write!(f, "safari-bidi engine: unsupported operation: {operation}")
            }
            Self::InvalidTarget { target, reason } => {
                write!(f, "safari-bidi engine: invalid target {target:?}: {reason}")
            }
            Self::NonLoopback { endpoint } => {
                write!(f, "refusing non-loopback bidi socket {endpoint:?}")
            }
            Self::Driver { operation, message } => {
                write!(f, "safaridriver {operation} failed: {message}")
            }
            Self::SessionNotCreated { code, message } => {
                write!(f, "safaridriver session not created ({code}): {message}")
            }
            Self::NoBidiSocket { value } => write!(
                f,
                "safaridriver created a session without a BiDi socket (webSocketUrl={value}); this Safari does not support safari:experimentalWebSocketUrl"
            ),
            Self::Protocol {
                method,
                code,
                message,
            } => {
                if message.is_empty() {
                    write!(f, "bidi {method} failed: {code}")
                } else {
                    write!(f, "bidi {method} failed ({code}): {message}")
                }
            }
            Self::Timeout { operation, timeout } => {
                write!(f, "safari-bidi {operation} timed out after {timeout:?}")
            }
        }
    }
}
impl std::error::Error for BidiError {}

/// Options for the isolated safaridriver process.
#[derive(Clone, Debug)]
pub struct DriverOptions {
    pub driver_path: PathBuf,
    pub diagnose: bool,
    pub ready_timeout: Duration,
    pub session_timeout: Duration,
    pub request_timeout: Duration,
    pub navigation_policy: NavigationPolicy,
}
impl Default for DriverOptions {
    fn default() -> Self {
        Self {
            driver_path: PathBuf::from(DRIVER_PATH),
            diagnose: false,
            ready_timeout: Duration::from_secs(10),
            session_timeout: Duration::from_secs(60),
            request_timeout: Duration::from_secs(30),
            navigation_policy: NavigationPolicy::default(),
        }
    }
}

/// Capabilities returned by a W3C session. `web_socket_url` is kept as a
/// string only after validation; Safari's misleading boolean `true` is rejected.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct SessionCapabilities {
    pub session_id: String,
    pub web_socket_url: String,
    pub capabilities: Value,
}

/// The exact WebDriver capabilities required by Safari 27's BiDi endpoint.
#[must_use]
pub fn session_request() -> Value {
    json!({
        "capabilities": {"alwaysMatch": {
            "browserName": "safari",
            "webSocketUrl": true,
            "safari:experimentalWebSocketUrl": true,
        }}
    })
}

/// Keeps process management injectable without requiring a real safaridriver.
pub trait ProcessAdapter: Send + Sync {
    fn spawn(&self, program: &Path, args: &[String]) -> Result<Box<dyn ProcessHandle>, BidiError>;
}
pub trait ProcessHandle: Send {
    fn try_wait(&mut self) -> Result<Option<i32>, BidiError>;
    fn kill(&mut self) -> Result<(), BidiError>;
    fn wait(&mut self) -> Result<Option<i32>, BidiError>;
}

#[derive(Clone, Copy, Debug, Default)]
pub struct SystemProcessAdapter;
impl ProcessAdapter for SystemProcessAdapter {
    fn spawn(&self, program: &Path, args: &[String]) -> Result<Box<dyn ProcessHandle>, BidiError> {
        let mut command = Command::new(program);
        command
            .args(args)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null());
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            command.process_group(0);
        }
        let child = command.spawn().map_err(|error| BidiError::Driver {
            operation: format!("start {}", program.display()),
            message: error.to_string(),
        })?;
        Ok(Box::new(SystemProcessHandle { child }))
    }
}
struct SystemProcessHandle {
    child: Child,
}
impl ProcessHandle for SystemProcessHandle {
    fn try_wait(&mut self) -> Result<Option<i32>, BidiError> {
        self.child
            .try_wait()
            .map(|status| status.and_then(|s| s.code()))
            .map_err(|error| BidiError::Driver {
                operation: "poll process".to_owned(),
                message: error.to_string(),
            })
    }
    fn kill(&mut self) -> Result<(), BidiError> {
        #[cfg(unix)]
        {
            use rustix::process::{Pid, Signal, kill_process_group};
            if let Some(pid) = Pid::from_raw(self.child.id() as i32) {
                return kill_process_group(pid, Signal::KILL).map_err(|error| BidiError::Driver {
                    operation: "stop process group".to_owned(),
                    message: error.to_string(),
                });
            }
        }
        match self.child.kill() {
            Ok(()) => Ok(()),
            Err(error) if error.kind() == std::io::ErrorKind::InvalidInput => Ok(()),
            Err(error) => Err(BidiError::Driver {
                operation: "stop process".to_owned(),
                message: error.to_string(),
            }),
        }
    }
    fn wait(&mut self) -> Result<Option<i32>, BidiError> {
        self.child
            .wait()
            .map(|s| s.code())
            .map_err(|error| BidiError::Driver {
                operation: "wait process".to_owned(),
                message: error.to_string(),
            })
    }
}

pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, BidiError>> + Send + 'a>>;

/// Complete transport seam used by the engine; tests can provide a deterministic
/// frame adapter without opening a socket.
pub trait BidiTransport: Send {
    fn command<'a>(&'a mut self, method: &'a str, params: Value) -> BoxFuture<'a, Value>;
    fn close<'a>(&'a mut self) -> BoxFuture<'a, ()>;
}

pub trait TransportConnector: Send + Sync {
    fn connect<'a>(
        &'a self,
        endpoint: &'a str,
        timeout: Duration,
    ) -> BoxFuture<'a, Box<dyn BidiTransport>>;
}

#[derive(Clone, Copy, Debug, Default)]
pub struct WebSocketConnector;
impl TransportConnector for WebSocketConnector {
    fn connect<'a>(
        &'a self,
        endpoint: &'a str,
        duration: Duration,
    ) -> BoxFuture<'a, Box<dyn BidiTransport>> {
        Box::pin(async move {
            let result = timeout(duration, connect_async(endpoint))
                .await
                .map_err(|_| BidiError::Timeout {
                    operation: "dial bidi socket".to_owned(),
                    timeout: duration,
                })?;
            let (socket, _) = result.map_err(|error| BidiError::Driver {
                operation: "dial bidi socket".to_owned(),
                message: error.to_string(),
            })?;
            Ok(Box::new(WebSocketTransport { socket, next_id: 0 }) as Box<dyn BidiTransport>)
        })
    }
}

struct WebSocketTransport {
    socket: async_tungstenite::WebSocketStream<async_tungstenite::tokio::ConnectStream>,
    next_id: u64,
}
impl BidiTransport for WebSocketTransport {
    fn command<'a>(&'a mut self, method: &'a str, params: Value) -> BoxFuture<'a, Value> {
        Box::pin(async move {
            self.next_id = self.next_id.wrapping_add(1).max(1);
            let id = self.next_id;
            let payload = json!({"id": id, "method": method, "params": params});
            self.socket
                .send(Message::Text(payload.to_string().into()))
                .await
                .map_err(|error| BidiError::Driver {
                    operation: format!("write BiDi command {method}"),
                    message: error.to_string(),
                })?;
            while let Some(frame) = self.socket.next().await {
                let frame = frame.map_err(|error| BidiError::Driver {
                    operation: format!("read BiDi response {method}"),
                    message: error.to_string(),
                })?;
                let Message::Text(text) = frame else { continue };
                let message: WireMessage =
                    serde_json::from_str(&text).map_err(|error| BidiError::Driver {
                        operation: format!("decode BiDi response {method}"),
                        message: error.to_string(),
                    })?;
                if message.kind == "event" || message.id != Some(id) {
                    continue;
                }
                if message.kind == "error" || message.error.is_some() {
                    return Err(BidiError::Protocol {
                        method: method.to_owned(),
                        code: message.error.unwrap_or_else(|| "unknown error".to_owned()),
                        message: message.message.unwrap_or_default(),
                    });
                }
                return Ok(message.result.unwrap_or(Value::Null));
            }
            Err(BidiError::Protocol {
                method: method.to_owned(),
                code: "connection closed".to_owned(),
                message: String::new(),
            })
        })
    }
    fn close<'a>(&'a mut self) -> BoxFuture<'a, ()> {
        Box::pin(async move {
            self.socket
                .close(None)
                .await
                .map_err(|error| BidiError::Driver {
                    operation: "close bidi socket".to_owned(),
                    message: error.to_string(),
                })
        })
    }
}

#[derive(Deserialize)]
struct WireMessage {
    #[serde(rename = "type")]
    kind: String,
    id: Option<u64>,
    result: Option<Value>,
    error: Option<String>,
    message: Option<String>,
}

struct DriverSession {
    process: Option<Box<dyn ProcessHandle>>,
    transport: Box<dyn BidiTransport>,
    http: Option<reqwest::Client>,
    base_url: Option<String>,
    session_id: Option<String>,
    request_timeout: Duration,
}
impl DriverSession {
    async fn command(&mut self, method: &str, params: Value) -> Result<Value, BidiError> {
        let timeout_duration = self.request_timeout;
        timeout(timeout_duration, self.transport.command(method, params))
            .await
            .map_err(|_| BidiError::Timeout {
                operation: format!("BiDi command {method}"),
                timeout: timeout_duration,
            })?
    }

    async fn close(&mut self) -> Result<(), BidiError> {
        let mut first = None;
        if let (Some(http), Some(base), Some(id)) = (&self.http, &self.base_url, &self.session_id) {
            let result = timeout(
                self.request_timeout,
                http.delete(format!("{base}/session/{id}")).send(),
            )
            .await;
            match result {
                Ok(Ok(response))
                    if response.status().is_success() || response.status().as_u16() == 404 => {}
                Ok(Ok(response)) => {
                    first = Some(BidiError::Driver {
                        operation: "delete WebDriver session".to_owned(),
                        message: format!("HTTP {}", response.status()),
                    })
                }
                Ok(Err(error)) => {
                    first = Some(BidiError::Driver {
                        operation: "delete WebDriver session".to_owned(),
                        message: error.to_string(),
                    })
                }
                Err(_) => {
                    first = Some(BidiError::Timeout {
                        operation: "delete WebDriver session".to_owned(),
                        timeout: self.request_timeout,
                    })
                }
            }
        }
        self.session_id = None;
        let close_result = timeout(self.request_timeout, self.transport.close()).await;
        let transport_result = match close_result {
            Ok(result) => result,
            Err(_) => Err(BidiError::Timeout {
                operation: "close bidi transport".to_owned(),
                timeout: self.request_timeout,
            }),
        };
        if let Err(error) = transport_result
            && first.is_none()
        {
            first = Some(error);
        }
        if let Some(mut process) = self.process.take() {
            stop_process(&mut process);
        }
        first.map_or(Ok(()), Err)
    }
}
impl Drop for DriverSession {
    fn drop(&mut self) {
        if let Some(mut process) = self.process.take() {
            stop_process(&mut process);
        }
    }
}

/// An isolated Safari BiDi engine. It owns its driver session after launch.
pub struct BidiEngine {
    session: Option<DriverSession>,
    context: String,
    navigation_policy: NavigationPolicy,
    closed: bool,
}
impl BidiEngine {
    /// Construct an engine around a fake or otherwise injected transport.
    #[must_use]
    pub fn from_transport(transport: Box<dyn BidiTransport>, context: impl Into<String>) -> Self {
        Self::from_transport_with_timeout(
            transport,
            context,
            DriverOptions::default().request_timeout,
        )
    }

    /// Construct an engine with injected transport, policy, and timeout.
    #[must_use]
    pub fn from_transport_with_timeout(
        transport: Box<dyn BidiTransport>,
        context: impl Into<String>,
        request_timeout: Duration,
    ) -> Self {
        Self {
            session: Some(DriverSession {
                process: None,
                transport,
                http: None,
                base_url: None,
                session_id: None,
                request_timeout: nonzero_timeout(request_timeout),
            }),
            context: context.into(),
            navigation_policy: NavigationPolicy::default(),
            closed: false,
        }
    }

    #[must_use]
    pub fn with_navigation_policy(mut self, policy: NavigationPolicy) -> Self {
        self.navigation_policy = policy;
        self
    }

    pub fn set_navigation_policy(&mut self, policy: NavigationPolicy) {
        self.navigation_policy = policy;
    }

    /// Launch the system safaridriver and create a verified BiDi session.
    pub async fn launch(options: DriverOptions) -> Result<Self, BidiError> {
        Self::launch_with_adapters(options, &SystemProcessAdapter, &WebSocketConnector).await
    }

    /// Launch with injectable process and transport adapters.
    pub async fn launch_with_adapters<P: ProcessAdapter, C: TransportConnector>(
        options: DriverOptions,
        process: &P,
        connector: &C,
    ) -> Result<Self, BidiError> {
        let mut last_error = None;
        for _ in 0..MAX_CONNECT_ATTEMPTS {
            if !options.driver_path.is_file() {
                return Err(BidiError::Prerequisite {
                    check: SafariPrerequisite::DriverUnavailable,
                    message: format!("driver not found at {}", options.driver_path.display()),
                });
            }
            match launch_once(&options, process, connector).await {
                Ok(mut session) => {
                    let tree = match session.command("browsingContext.getTree", json!({})).await {
                        Ok(tree) => tree,
                        Err(error) => {
                            let _ = session.close().await;
                            return Err(error);
                        }
                    };
                    let context = match tree
                        .get("contexts")
                        .and_then(Value::as_array)
                        .and_then(|contexts| contexts.first())
                        .and_then(|value| value.get("context"))
                        .and_then(Value::as_str)
                    {
                        Some(context) => context.to_owned(),
                        None => {
                            let _ = session.close().await;
                            return Err(BidiError::Driver {
                                operation: "read browsing context tree".to_owned(),
                                message: "session has no browsing context".to_owned(),
                            });
                        }
                    };
                    return Ok(Self {
                        session: Some(session),
                        context,
                        navigation_policy: options.navigation_policy.clone(),
                        closed: false,
                    });
                }
                Err(error) if retryable_socket_error(&error) => last_error = Some(error),
                Err(error) => return Err(error),
            }
        }
        Err(last_error.unwrap_or_else(|| BidiError::Driver {
            operation: "launch".to_owned(),
            message: "no attempts".to_owned(),
        }))
    }

    pub fn new_context(&self) -> Result<Context, BidiError> {
        self.ensure_open()?;
        Ok(Context {
            id: ENGINE_KIND.to_owned(),
        })
    }
    pub fn new_page(&self) -> Result<Page, BidiError> {
        self.ensure_open()?;
        if self.context.is_empty() {
            return Err(BidiError::NotLaunched);
        }
        Ok(Page {
            id: self.context.clone(),
            session_id: String::new(),
        })
    }

    pub async fn navigate(
        &mut self,
        page: &Page,
        target: &str,
    ) -> Result<NavigationResult, BidiError> {
        self.ensure_open()?;
        let target = validate_target(target)?;
        if let Err(reason) = self.navigation_policy.check(&target) {
            return Err(BidiError::InvalidTarget { target, reason });
        }
        let context = if page.id.is_empty() {
            self.context.clone()
        } else {
            page.id.clone()
        };
        self.session_mut()?
            .command(
                "browsingContext.navigate",
                json!({"context": context, "url": target, "wait": "complete"}),
            )
            .await
            .map(|result| NavigationResult {
                frame_id: context,
                loader_id: result
                    .get("navigation")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                url: result
                    .get("url")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                error_text: String::new(),
            })
    }

    pub async fn evaluate(
        &mut self,
        page: &Page,
        expression: &str,
    ) -> Result<EvaluationResult, BidiError> {
        self.ensure_open()?;
        let context = if page.id.is_empty() {
            self.context.clone()
        } else {
            page.id.clone()
        };
        let result = self.session_mut()?.command("script.evaluate", json!({"expression": expression, "target": {"context": context}, "awaitPromise": true})).await?;
        if result.get("type").and_then(Value::as_str) == Some("exception") {
            return Ok(EvaluationResult {
                exception_text: result
                    .get("exceptionDetails")
                    .and_then(|detail| detail.get("text"))
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
                ..EvaluationResult::default()
            });
        }
        let remote = result.get("result").unwrap_or(&Value::Null);
        Ok(EvaluationResult {
            value: remote.get("value").cloned(),
            value_type: remote
                .get("type")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            description: remote
                .get("description")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            exception_text: String::new(),
        })
    }

    /// Evaluate a bounded script in the selected browsing context.
    pub async fn evaluate_script(
        &mut self,
        page: &Page,
        expression: &str,
    ) -> Result<EvaluationResult, BidiError> {
        self.evaluate(page, expression).await
    }

    /// Read cookies through Safari's WebDriver BiDi storage module.
    pub async fn get_cookies(&mut self, page: &Page) -> Result<Value, BidiError> {
        let context = if page.id.is_empty() {
            self.context.clone()
        } else {
            page.id.clone()
        };
        self.session_mut()?
            .command("storage.getCookies", json!({"filter":{"context":context}}))
            .await
    }

    /// Set one cookie through the BiDi storage module.
    pub async fn set_cookie(&mut self, cookie: Value, page: &Page) -> Result<Value, BidiError> {
        let context = if page.id.is_empty() {
            self.context.clone()
        } else {
            page.id.clone()
        };
        self.session_mut()?
            .command(
                "storage.setCookie",
                json!({"cookie":cookie,"partition":{"type":"context","context":context}}),
            )
            .await
    }

    /// Delete one cookie through the BiDi storage module.
    pub async fn delete_cookie(
        &mut self,
        name: &str,
        domain: &str,
        path: &str,
        page: &Page,
    ) -> Result<Value, BidiError> {
        let context = if page.id.is_empty() {
            self.context.clone()
        } else {
            page.id.clone()
        };
        self.session_mut()?
            .command("storage.deleteCookies", json!({"filter":{"name":name,"domain":domain,"path":path},"partition":{"type":"context","context":context}}))
            .await
    }

    pub fn screenshot(&self) -> Result<Vec<u8>, BidiError> {
        Err(BidiError::Unsupported {
            operation: "screenshot".to_owned(),
        })
    }

    #[must_use]
    pub fn capabilities(&self) -> Capabilities {
        let mut caps = capabilities_for(
            ENGINE_KIND,
            [
                "CookieEngine",
                "InspectionEngine",
                "InteractionEngine",
                "NavigationStateProvider",
            ],
        );
        caps.launch_mode = "launch".to_owned();
        caps
    }

    /// Safari's measured BiDi surface has no network module, so policy reaches
    /// direct navigation targets only.
    #[must_use]
    pub fn limitations(&self) -> [&'static str; 1] {
        [
            "safari-bidi enforces URL policy on navigation targets only: Safari has no WebDriver BiDi network module, so redirects and subresource requests are not intercepted",
        ]
    }

    pub async fn close(&mut self) -> Result<(), BidiError> {
        if self.closed {
            return Ok(());
        }
        self.closed = true;
        match self.session.take() {
            Some(mut session) => session.close().await,
            None => Ok(()),
        }
    }

    fn ensure_open(&self) -> Result<(), BidiError> {
        if self.closed {
            Err(BidiError::Closed)
        } else if self.session.is_none() {
            Err(BidiError::NotLaunched)
        } else {
            Ok(())
        }
    }
    fn session_mut(&mut self) -> Result<&mut DriverSession, BidiError> {
        self.session.as_mut().ok_or(BidiError::NotLaunched)
    }
}

fn retryable_socket_error(error: &BidiError) -> bool {
    matches!(error, BidiError::Driver { operation, .. } if operation.contains("dial") || operation.contains("verify"))
}

async fn launch_once<P: ProcessAdapter, C: TransportConnector>(
    options: &DriverOptions,
    process: &P,
    connector: &C,
) -> Result<DriverSession, BidiError> {
    let http_port = free_port()?;
    let bidi_port = free_port_excluding(http_port)?;
    let mut args = vec![
        "-p".to_owned(),
        http_port.to_string(),
        "--bidi".to_owned(),
        bidi_port.to_string(),
    ];
    if options.diagnose {
        args.push("--diagnose".to_owned());
    }
    let mut child = process.spawn(&options.driver_path, &args)?;
    let client = reqwest::Client::new();
    let base = format!("http://127.0.0.1:{http_port}");
    if let Err(error) = wait_ready(&client, &base, child.as_mut(), options.ready_timeout).await {
        let mut child = child;
        let _ = child.kill();
        let _ = child.wait();
        return Err(match error {
            BidiError::Timeout { timeout, .. } => BidiError::Prerequisite {
                check: SafariPrerequisite::LoopbackSessionUnavailable,
                message: format!("safaridriver did not become ready within {timeout:?}"),
            },
            other => other,
        });
    }
    let response = match timeout(
        options.session_timeout,
        client
            .post(format!("{base}/session"))
            .header("content-type", "application/json")
            .body(session_request().to_string())
            .send(),
    )
    .await
    {
        Ok(Ok(response)) => response,
        Ok(Err(error)) => {
            stop_process(&mut child);
            return Err(BidiError::Driver {
                operation: "create safaridriver session".to_owned(),
                message: error.to_string(),
            });
        }
        Err(_) => {
            stop_process(&mut child);
            return Err(BidiError::Prerequisite {
                check: SafariPrerequisite::RemoteAutomationDisabled,
                message: format!(
                    "safaridriver session creation timed out after {:?}",
                    options.session_timeout
                ),
            });
        }
    };
    let status = response.status();
    let body_text = match timeout(nonzero_timeout(options.request_timeout), response.text()).await {
        Ok(Ok(body_text)) => body_text,
        Ok(Err(error)) => {
            stop_process(&mut child);
            return Err(BidiError::Driver {
                operation: "read session response".to_owned(),
                message: error.to_string(),
            });
        }
        Err(_) => {
            stop_process(&mut child);
            return Err(BidiError::Timeout {
                operation: "read session response".to_owned(),
                timeout: nonzero_timeout(options.request_timeout),
            });
        }
    };
    let body: Value = match serde_json::from_str(&body_text) {
        Ok(body) => body,
        Err(error) => {
            stop_process(&mut child);
            return Err(BidiError::Driver {
                operation: "decode session response".to_owned(),
                message: error.to_string(),
            });
        }
    };
    let parsed = match parse_session_response(status.as_u16(), &body) {
        Ok(parsed) => parsed,
        Err(error) => {
            if let Some(session_id) = body
                .get("value")
                .and_then(|value| value.get("sessionId"))
                .and_then(Value::as_str)
            {
                delete_session_best_effort(&client, &base, session_id, options.request_timeout)
                    .await;
            }
            stop_process(&mut child);
            return Err(error);
        }
    };
    if let Err(error) = require_loopback(&parsed.web_socket_url) {
        delete_session_best_effort(&client, &base, &parsed.session_id, options.request_timeout)
            .await;
        stop_process(&mut child);
        return Err(error);
    }
    let mut transport = match connector
        .connect(&parsed.web_socket_url, options.ready_timeout)
        .await
    {
        Ok(transport) => transport,
        Err(error) => {
            delete_session_best_effort(&client, &base, &parsed.session_id, options.request_timeout)
                .await;
            stop_process(&mut child);
            return Err(error);
        }
    };
    let status_result = timeout(
        nonzero_timeout(options.request_timeout),
        transport.command("session.status", json!({})),
    )
    .await;
    let status_result = match status_result {
        Ok(result) => result,
        Err(_) => Err(BidiError::Timeout {
            operation: "BiDi command session.status".to_owned(),
            timeout: nonzero_timeout(options.request_timeout),
        }),
    };
    if let Err(error) = status_result {
        delete_session_best_effort(&client, &base, &parsed.session_id, options.request_timeout)
            .await;
        stop_process(&mut child);
        return Err(BidiError::Driver {
            operation: "verify WebDriver BiDi socket".to_owned(),
            message: error.to_string(),
        });
    }
    Ok(DriverSession {
        process: Some(child),
        transport,
        http: Some(client),
        base_url: Some(base),
        session_id: Some(parsed.session_id),
        request_timeout: nonzero_timeout(options.request_timeout),
    })
}

fn nonzero_timeout(timeout: Duration) -> Duration {
    if timeout.is_zero() {
        DriverOptions::default().request_timeout
    } else {
        timeout
    }
}

fn stop_process(process: &mut Box<dyn ProcessHandle>) {
    let _ = process.kill();
    let _ = process.wait();
}

async fn delete_session_best_effort(
    client: &reqwest::Client,
    base: &str,
    session_id: &str,
    duration: Duration,
) {
    let _ = timeout(
        duration,
        client.delete(format!("{base}/session/{session_id}")).send(),
    )
    .await;
}

async fn wait_ready(
    client: &reqwest::Client,
    base: &str,
    process: &mut dyn ProcessHandle,
    duration: Duration,
) -> Result<(), BidiError> {
    let started = std::time::Instant::now();
    while started.elapsed() < duration {
        match process.try_wait() {
            Ok(Some(code)) => {
                return Err(BidiError::Driver {
                    operation: "wait for readiness".to_owned(),
                    message: format!("process exited with {code:?}"),
                });
            }
            Ok(None) => {}
            Err(error) => return Err(error),
        }
        let attempt = timeout(
            Duration::from_millis(500),
            client.get(format!("{base}/status")).send(),
        )
        .await;
        if matches!(attempt, Ok(Ok(response)) if response.status().is_success()) {
            return Ok(());
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    Err(BidiError::Timeout {
        operation: "wait for safaridriver readiness".to_owned(),
        timeout: duration,
    })
}

pub fn parse_session_response(status: u16, body: &Value) -> Result<SessionCapabilities, BidiError> {
    let value = body.get("value").unwrap_or(body);
    if let Some(code) = value.get("error").and_then(Value::as_str) {
        let message = value
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        return Err(session_creation_error(status, code, &message));
    }
    let session_id = value
        .get("sessionId")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned();
    if session_id.is_empty() {
        return Err(BidiError::SessionNotCreated {
            code: format!("HTTP {status}"),
            message: "safaridriver returned no session id".to_owned(),
        });
    }
    let capabilities = value.get("capabilities").cloned().unwrap_or(Value::Null);
    let raw_socket = capabilities
        .get("webSocketUrl")
        .cloned()
        .unwrap_or(Value::Null);
    let Some(web_socket_url) = raw_socket.as_str().filter(|url| !url.is_empty()) else {
        return Err(BidiError::NoBidiSocket {
            value: raw_socket.to_string(),
        });
    };
    Ok(SessionCapabilities {
        session_id,
        web_socket_url: web_socket_url.to_owned(),
        capabilities,
    })
}

fn session_creation_error(status: u16, code: &str, message: &str) -> BidiError {
    if message.contains("remote automation")
        || message.contains("Allow remote automation")
        || message.contains("already running")
        || message.contains("Cmd-Q")
    {
        return BidiError::Prerequisite {
            check: SafariPrerequisite::RemoteAutomationDisabled,
            message: format!("{message} (HTTP {status})"),
        };
    }
    if !message.contains("Request creation of a new automation session") {
        return BidiError::SessionNotCreated {
            code: code.to_owned(),
            message: message.to_owned(),
        };
    }
    BidiError::SessionNotCreated {
        code: code.to_owned(),
        message: format!(
            "safaridriver could not create a session ({code}, HTTP {status}). Safari reports this same timeout for two causes: Safari may already be running for normal browsing (quit it completely with Cmd-Q), or remote automation may not be permitted (run `sudo safaridriver --enable` and enable Allow remote automation). Apple's own message was: {message}"
        ),
    }
}

pub fn require_loopback(endpoint: &str) -> Result<(), BidiError> {
    let parsed = Url::parse(endpoint).map_err(|error| BidiError::Driver {
        operation: "parse BiDi socket URL".to_owned(),
        message: error.to_string(),
    })?;
    if !matches!(parsed.scheme(), "ws" | "wss")
        || parsed.host_str().is_none()
        || parsed.port().is_none()
        || !parsed.username().is_empty()
        || parsed.password().is_some()
    {
        return Err(BidiError::NonLoopback {
            endpoint: endpoint.to_owned(),
        });
    }
    let is_loopback = match parsed.host() {
        Some(url::Host::Domain(host)) if host.eq_ignore_ascii_case("localhost") => true,
        Some(url::Host::Ipv4(address)) => address.is_loopback(),
        Some(url::Host::Ipv6(address)) => address.is_loopback(),
        _ => false,
    };
    if is_loopback {
        Ok(())
    } else {
        Err(BidiError::NonLoopback {
            endpoint: endpoint.to_owned(),
        })
    }
}

fn validate_target(target: &str) -> Result<String, BidiError> {
    let trimmed = target.trim();
    let parsed = Url::parse(trimmed).map_err(|error| BidiError::InvalidTarget {
        target: target.to_owned(),
        reason: error.to_string(),
    })?;
    if !matches!(parsed.scheme(), "http" | "https") || parsed.host_str().is_none() {
        return Err(BidiError::InvalidTarget {
            target: target.to_owned(),
            reason: "http/https URL required".to_owned(),
        });
    }
    Ok(trimmed.to_owned())
}

fn free_port_excluding(excluded: u16) -> Result<u16, BidiError> {
    for _ in 0..16 {
        let port = free_port()?;
        if port != excluded {
            return Ok(port);
        }
    }
    Err(BidiError::Driver {
        operation: "allocate distinct driver ports".to_owned(),
        message: "failed to allocate different WebDriver and BiDi ports".to_owned(),
    })
}
fn free_port() -> Result<u16, BidiError> {
    let listener =
        std::net::TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0)).map_err(|error| {
            BidiError::Driver {
                operation: "allocate driver port".to_owned(),
                message: error.to_string(),
            }
        })?;
    listener
        .local_addr()
        .map(|address| address.port())
        .map_err(|error| BidiError::Driver {
            operation: "read driver port".to_owned(),
            message: error.to_string(),
        })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Mutex};

    #[derive(Clone, Default)]
    struct ProcessProbe(Arc<Mutex<(usize, usize)>>);

    struct ProbeProcess(ProcessProbe);

    impl ProcessHandle for ProbeProcess {
        fn try_wait(&mut self) -> Result<Option<i32>, BidiError> {
            Ok(None)
        }

        fn kill(&mut self) -> Result<(), BidiError> {
            self.0.0.lock().expect("kill lock").0 += 1;
            Ok(())
        }

        fn wait(&mut self) -> Result<Option<i32>, BidiError> {
            self.0.0.lock().expect("wait lock").1 += 1;
            Ok(Some(0))
        }
    }

    struct NoopTransport;

    impl BidiTransport for NoopTransport {
        fn command<'a>(&'a mut self, _method: &'a str, _params: Value) -> BoxFuture<'a, Value> {
            Box::pin(async { Ok(Value::Null) })
        }

        fn close<'a>(&'a mut self) -> BoxFuture<'a, ()> {
            Box::pin(async { Ok(()) })
        }
    }

    #[test]
    fn dropping_driver_session_kills_and_reaps_owned_process() {
        let probe = ProcessProbe::default();
        let session = DriverSession {
            process: Some(Box::new(ProbeProcess(probe.clone()))),
            transport: Box::new(NoopTransport),
            http: None,
            base_url: None,
            session_id: None,
            request_timeout: Duration::from_secs(1),
        };
        drop(session);
        assert_eq!(*probe.0.lock().expect("probe lock"), (1, 1));
    }

    #[test]
    fn separately_allocated_driver_ports_are_never_equal() {
        for _ in 0..64 {
            let webdriver = free_port().expect("WebDriver port");
            let bidi = free_port_excluding(webdriver).expect("BiDi port");
            assert_ne!(webdriver, bidi);
        }
    }
}
