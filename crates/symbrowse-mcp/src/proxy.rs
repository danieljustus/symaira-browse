#![deny(unsafe_code)]

use std::{
    env,
    fs::{self, OpenOptions},
    io,
    path::Path,
    process::{Child, Command, Stdio},
    thread,
    time::{Duration, Instant},
};

#[cfg(unix)]
use std::os::unix::process::CommandExt;
#[cfg(windows)]
use std::os::windows::process::CommandExt;

#[cfg(unix)]
use std::io::{BufRead, BufReader, Write};

use serde_json::{Map, Value, json};
use symbrowse_daemon::{default_log_path, redact_str};

use crate::registry::ToolSpec;

const MAX_DAEMON_FRAME_BYTES: usize = 1 << 20;
const DEFAULT_READ_TIMEOUT: Duration = Duration::from_secs(30);
const DEFAULT_STARTUP_TIMEOUT: Duration = Duration::from_secs(5);
const MCP_DEFAULT_MAX_TOKENS: i64 = 4_000;

/// A structured failure that can be rendered as an MCP tool result.
#[derive(Clone, Debug, PartialEq)]
pub struct ToolError {
    pub code: String,
    pub message: String,
    pub hint: Option<String>,
    pub retryable: Option<bool>,
    pub requires_user_confirmation: Option<bool>,
    pub resume_hint: Option<String>,
    pub details: Option<Value>,
}

impl ToolError {
    #[must_use]
    pub fn unavailable(message: impl Into<String>) -> Self {
        Self {
            code: "daemon_unavailable".to_owned(),
            message: redact_str(&message.into()),
            hint: None,
            retryable: Some(true),
            requires_user_confirmation: None,
            resume_hint: None,
            details: None,
        }
    }

    fn transport(code: &str, message: impl Into<String>, session: &str, endpoint: &str) -> Self {
        let endpoint = redact_str(endpoint);
        Self {
            code: code.to_owned(),
            message: redact_str(&message.into()),
            hint: None,
            retryable: Some(code == "daemon_unavailable" || code == "operation_timeout"),
            requires_user_confirmation: None,
            resume_hint: None,
            details: Some(json!({"session": redact_str(session), "socket_path": endpoint})),
        }
    }

    #[must_use]
    pub fn display_message(&self) -> String {
        format!("{}: {}", self.code, self.message)
    }

    #[must_use]
    pub fn metadata(&self) -> Value {
        let mut data = serde_json::Map::new();
        data.insert("code".to_owned(), Value::String(self.code.clone()));
        data.insert("message".to_owned(), Value::String(self.display_message()));
        if let Some(hint) = &self.hint {
            data.insert("hint".to_owned(), Value::String(hint.clone()));
        }
        if let Some(value) = self.retryable {
            data.insert("retryable".to_owned(), Value::Bool(value));
        }
        if let Some(value) = self.requires_user_confirmation {
            data.insert("requires_confirmation".to_owned(), Value::Bool(value));
        }
        if let Some(value) = &self.resume_hint {
            data.insert("resume_hint".to_owned(), Value::String(value.clone()));
        }
        if let Some(value) = &self.details {
            data.insert("details".to_owned(), value.clone());
        }
        Value::Object(data)
    }
}

/// A daemon frame proxy. The Go daemon speaks newline-delimited JSON over a
/// Unix socket on macOS/Linux and a named pipe endpoint on Windows. Keeping the
/// transport here, instead of importing the Go daemon package, preserves the
/// standalone single-binary boundary.
#[derive(Clone, Debug)]
pub struct DaemonProxyOptions {
    pub session: String,
    pub executable: String,
    pub allow_private: bool,
    pub engine: Option<String>,
    pub mode: Option<String>,
    pub endpoint: Option<String>,
    pub daemon_log_path: Option<String>,
    pub read_timeout: Duration,
    pub startup_timeout: Duration,
}

impl Default for DaemonProxyOptions {
    fn default() -> Self {
        Self {
            session: "default".to_owned(),
            executable: current_executable(),
            allow_private: false,
            engine: None,
            mode: None,
            endpoint: None,
            daemon_log_path: None,
            read_timeout: DEFAULT_READ_TIMEOUT,
            startup_timeout: DEFAULT_STARTUP_TIMEOUT,
        }
    }
}

#[derive(Debug)]
pub struct DaemonProxy {
    options: DaemonProxyOptions,
    child: Option<Child>,
}

#[derive(Debug)]
enum CheckedRequestError {
    Autostart(ToolError),
    Fatal(ToolError),
}

fn classify_checked_error(error: ToolError) -> CheckedRequestError {
    if error.code == "daemon_unavailable" {
        CheckedRequestError::Autostart(error)
    } else {
        CheckedRequestError::Fatal(error)
    }
}

impl DaemonProxy {
    #[must_use]
    pub fn new(mut options: DaemonProxyOptions) -> Self {
        if options.session.is_empty() {
            options.session = "default".to_owned();
        }
        if options.executable.is_empty() {
            options.executable = current_executable();
        }
        if options.read_timeout.is_zero() {
            options.read_timeout = DEFAULT_READ_TIMEOUT;
        }
        if options.startup_timeout.is_zero() {
            options.startup_timeout = DEFAULT_STARTUP_TIMEOUT;
        }
        Self {
            options,
            child: None,
        }
    }

    #[must_use]
    pub fn options(&self) -> &DaemonProxyOptions {
        &self.options
    }

    fn call_inner(&mut self, tool: &ToolSpec, arguments: &Value) -> Result<Value, Box<ToolError>> {
        let session = arguments
            .get("session")
            .and_then(Value::as_str)
            .filter(|value| !value.is_empty())
            .unwrap_or(&self.options.session);
        let endpoint = self.endpoint(session)?;
        let command = daemon_command(tool, arguments);
        let max_tokens = requested_max_tokens(&command, arguments);
        let frame = DaemonFrame {
            cmd: command,
            args: daemon_args(tool, arguments),
            session: session.to_owned(),
            request_id: request_id(),
            max_tokens: (max_tokens > 0).then_some(max_tokens),
            retrieval_surface: Some("mcp".to_owned()),
        };

        match self.checked_request(&endpoint, &frame) {
            Ok(response) => response.into_result(),
            Err(CheckedRequestError::Autostart(first)) => {
                let child = self.start_daemon(session).map_err(Box::new)?;
                self.child = Some(child);
                let deadline = Instant::now() + self.options.startup_timeout;
                let mut last = first;
                while Instant::now() < deadline {
                    thread::sleep(Duration::from_millis(25));
                    match self.checked_request(&endpoint, &frame) {
                        Ok(response) => return response.into_result(),
                        Err(CheckedRequestError::Autostart(error)) => last = error,
                        Err(CheckedRequestError::Fatal(error)) => return Err(Box::new(error)),
                    }
                }
                if let Some(mut child) = self.child.take() {
                    terminate_child(&mut child);
                }
                Err(Box::new(ToolError {
                    code: "daemon_unavailable".to_owned(),
                    message: format!(
                        "daemon did not become ready for session {}",
                        redact_str(session)
                    ),
                    hint: Some(self.daemon_hint(session)),
                    retryable: Some(true),
                    requires_user_confirmation: None,
                    resume_hint: None,
                    details: last.details,
                }))
            }
            Err(CheckedRequestError::Fatal(error)) => Err(Box::new(error)),
        }
    }

    fn endpoint(&self, session: &str) -> Result<String, Box<ToolError>> {
        if !valid_session(session) {
            return Err(Box::new(ToolError {
                code: "invalid_session".to_owned(),
                message: format!("invalid session {}", redact_str(session)),
                hint: Some("use 1-64 letters, digits, '.', '_' or '-'".to_owned()),
                retryable: Some(false),
                requires_user_confirmation: None,
                resume_hint: None,
                details: None,
            }));
        }
        if let Some(endpoint) = &self.options.endpoint {
            return Ok(endpoint.clone());
        }
        default_endpoint(session).map_err(|message| {
            Box::new(ToolError::transport(
                "daemon_unavailable",
                message,
                session,
                "<default>",
            ))
        })
    }

    #[cfg(unix)]
    #[allow(clippy::result_large_err)]
    fn request(&self, endpoint: &str, frame: &DaemonFrame) -> Result<DaemonResponse, ToolError> {
        let payload = serde_json::to_vec(frame).map_err(|error| {
            ToolError::transport(
                "operation_failed",
                format!("encode daemon frame: {error}"),
                &frame.session,
                endpoint,
            )
        })?;
        let transport = symbrowse_daemon::connect_unix(Path::new(endpoint), self.options.read_timeout)
            .map_err(|error| {
                let timeout = error.kind() == io::ErrorKind::TimedOut;
                let code = if timeout {
                    "operation_timeout"
                } else {
                    "daemon_unavailable"
                };
                let mut result = ToolError::transport(
                    code,
                    if timeout {
                        format!("daemon connect timed out after {:?}", self.options.read_timeout)
                    } else {
                        format!("daemon is unavailable for session {:?}", frame.session)
                    },
                    &frame.session,
                    endpoint,
                );
                result.hint = Some(if timeout {
                    format!(
                        "increase timeout with SYMBROWSE_READ_TIMEOUT or inspect daemon logs for session {:?}",
                        frame.session
                    )
                } else {
                    self.daemon_hint(&frame.session)
                });
                result.details = Some(json!({
                    "session": redact_str(&frame.session),
                    "socket_path": redact_str(endpoint),
                    "timeout_seconds": self.options.read_timeout.as_secs_f64(),
                    "transport_error": redact_str(&error.to_string()),
                }));
                result
            })?;
        let mut transport = transport;
        write_frame(&mut transport, &payload, self.options.read_timeout).map_err(|_error| {
            ToolError::transport(
                "daemon_unavailable",
                format!(
                    "failed to write daemon frame for session {:?}",
                    frame.session
                ),
                &frame.session,
                endpoint,
            )
        })?;
        let response = read_response(&mut transport, self.options.read_timeout).map_err(|error| {
            let timeout = error.kind() == io::ErrorKind::TimedOut
                || error.kind() == io::ErrorKind::WouldBlock;
            let code = if timeout {
                "operation_timeout"
            } else {
                "daemon_unavailable"
            };
            let mut result = ToolError::transport(
                code,
                if timeout {
                    format!("daemon response timed out after {:?}", self.options.read_timeout)
                } else {
                    format!("failed to read daemon response for session {:?}", frame.session)
                },
                &frame.session,
                endpoint,
            );
            result.hint = Some(if timeout {
                format!(
                    "increase timeout with SYMBROWSE_READ_TIMEOUT or inspect daemon logs for session {:?}",
                    frame.session
                )
            } else {
                self.daemon_hint(&frame.session)
            });
            result.details = Some(json!({
                "session": redact_str(&frame.session),
                "socket_path": redact_str(endpoint),
                "timeout_seconds": self.options.read_timeout.as_secs_f64(),
                "transport_error": redact_str(&error.to_string()),
            }));
            result
        })?;
        Ok(response)
    }

    #[cfg(windows)]
    #[allow(clippy::result_large_err)]
    fn request(&self, endpoint: &str, frame: &DaemonFrame) -> Result<DaemonResponse, ToolError> {
        use interprocess::{
            ConnectWaitMode,
            os::windows::named_pipe::{pipe_mode, tokio::PipeStream},
        };
        use tokio::io::AsyncWriteExt;

        let payload = serde_json::to_vec(frame).map_err(|error| {
            ToolError::transport(
                "operation_failed",
                format!("encode daemon frame: {error}"),
                &frame.session,
                endpoint,
            )
        })?;
        if payload.len().saturating_add(1) > MAX_DAEMON_FRAME_BYTES {
            return Err(ToolError::transport(
                "malformed_request",
                "daemon frame exceeds size limit",
                &frame.session,
                endpoint,
            ));
        }
        let timeout = self.options.read_timeout;
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|error| {
                ToolError::transport(
                    "daemon_unavailable",
                    format!("create daemon runtime: {error}"),
                    &frame.session,
                    endpoint,
                )
            })?;
        let result: io::Result<DaemonResponse> = runtime.block_on(async {
            let mut transport = tokio::time::timeout(
                timeout,
                PipeStream::<pipe_mode::Bytes, pipe_mode::Bytes>::connect_by_path_with_wait_mode(
                    endpoint,
                    ConnectWaitMode::Timeout(timeout),
                ),
            )
            .await
            .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "daemon connect timed out"))??;
            tokio::time::timeout(timeout, transport.write_all(&payload))
                .await
                .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "daemon write timed out"))??;
            tokio::time::timeout(timeout, transport.write_all(b"\n"))
                .await
                .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "daemon write timed out"))??;
            tokio::time::timeout(timeout, transport.flush())
                .await
                .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "daemon flush timed out"))??;
            tokio::time::timeout(timeout, read_response_windows(&mut transport))
                .await
                .map_err(|_| io::Error::new(io::ErrorKind::TimedOut, "daemon read timed out"))?
        });
        result.map_err(|error| {
            let timeout = error.kind() == io::ErrorKind::TimedOut;
            let code = if timeout {
                "operation_timeout"
            } else {
                "daemon_unavailable"
            };
            let mut result = ToolError::transport(
                code,
                if timeout {
                    format!("daemon response timed out after {:?}", self.options.read_timeout)
                } else {
                    format!("failed to communicate with daemon for session {:?}", frame.session)
                },
                &frame.session,
                endpoint,
            );
            result.hint = Some(if timeout {
                format!(
                    "increase timeout with SYMBROWSE_READ_TIMEOUT or inspect daemon logs for session {:?}",
                    frame.session
                )
            } else {
                self.daemon_hint(&frame.session)
            });
            result.details = Some(json!({
                "session": redact_str(&frame.session),
                "socket_path": redact_str(endpoint),
                "timeout_seconds": self.options.read_timeout.as_secs_f64(),
                "transport_error": redact_str(&error.to_string()),
            }));
            result
        })
    }

    #[allow(clippy::result_large_err)]
    fn checked_request(
        &self,
        endpoint: &str,
        frame: &DaemonFrame,
    ) -> Result<DaemonResponse, CheckedRequestError> {
        let status = self
            .request(
                endpoint,
                &DaemonFrame {
                    cmd: "daemon.status".to_owned(),
                    args: None,
                    session: frame.session.clone(),
                    request_id: request_id(),
                    max_tokens: None,
                    retrieval_surface: Some("mcp".to_owned()),
                },
            )
            .map_err(classify_checked_error)?;
        if !status.success {
            return Err(CheckedRequestError::Fatal(status.into_tool_error()));
        }
        let data = status.data.unwrap_or(Value::Null);
        let session_ok =
            data.get("session").and_then(Value::as_str) == Some(frame.session.as_str());
        let engine_ok = self.options.engine.as_ref().is_none_or(|expected| {
            data.get("engine").and_then(Value::as_str) == Some(expected.as_str())
        });
        let policy_ok = data
            .get("policy")
            .and_then(|policy| policy.get("allow_private"))
            .and_then(Value::as_bool)
            == Some(self.options.allow_private);
        if !(session_ok && engine_ok && policy_ok) {
            let _ = self.request(
                endpoint,
                &DaemonFrame {
                    cmd: "daemon.stop".to_owned(),
                    args: None,
                    session: frame.session.clone(),
                    request_id: request_id(),
                    max_tokens: None,
                    retrieval_surface: Some("mcp".to_owned()),
                },
            );
            return Err(CheckedRequestError::Autostart(ToolError {
                code: "daemon_unavailable".to_owned(),
                message: "existing daemon configuration is incompatible; it was stopped".to_owned(),
                hint: Some(
                    "retry to start a daemon with the requested session configuration".to_owned(),
                ),
                retryable: Some(true),
                requires_user_confirmation: None,
                resume_hint: None,
                details: None,
            }));
        }
        self.request(endpoint, frame)
            .map_err(classify_checked_error)
    }

    #[allow(clippy::result_large_err)]
    fn start_daemon(&self, session: &str) -> Result<Child, ToolError> {
        if self.options.executable.is_empty() {
            return Err(ToolError::unavailable("daemon executable path is empty"));
        }
        let log_path = self
            .options
            .daemon_log_path
            .clone()
            .unwrap_or_else(|| default_log_path().to_string_lossy().into_owned());
        if let Some(parent) = Path::new(&log_path)
            .parent()
            .filter(|path| !path.as_os_str().is_empty())
        {
            fs::create_dir_all(parent).map_err(|error| {
                ToolError::unavailable(format!("create daemon log directory: {error}"))
            })?;
        }
        let mut log_options = OpenOptions::new();
        log_options.create(true).append(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            log_options.mode(0o600);
        }
        let log = log_options
            .open(&log_path)
            .map_err(|error| ToolError::unavailable(format!("open daemon log: {error}")))?;
        let stderr = log.try_clone().map_err(|error| {
            ToolError::unavailable(format!("duplicate daemon log handle: {error}"))
        })?;
        let mut command = Command::new(&self.options.executable);
        command
            .arg("daemon")
            .arg("--session")
            .arg(session)
            .arg("--ssrf")
            .arg("--mcp-mode")
            .stdin(Stdio::null());
        if let Some(mode) = &self.options.mode {
            command.arg("--mode").arg(mode);
        }
        if let Some(engine) = &self.options.engine {
            command.arg("--engine").arg(engine);
        }
        if self.options.allow_private {
            command.arg("--allow-private");
        }
        command.stdout(Stdio::from(log)).stderr(Stdio::from(stderr));
        detach_command(&mut command);
        command
            .spawn()
            .map_err(|error| ToolError::unavailable(format!("failed to start daemon: {error}")))
    }

    fn daemon_hint(&self, session: &str) -> String {
        format!(
            "start daemon with 'symbrowse daemon --session {session}'; see daemon log at {}",
            self.options
                .daemon_log_path
                .as_deref()
                .unwrap_or("the configured daemon log")
        )
    }
}

impl Default for DaemonProxy {
    fn default() -> Self {
        Self::new(DaemonProxyOptions::default())
    }
}

/// Proxy seam used by the owned stdio adapter.
pub trait ToolProxy {
    fn call(&mut self, tool: &ToolSpec, arguments: &Value) -> Result<Value, Box<ToolError>>;
}

impl ToolProxy for DaemonProxy {
    fn call(&mut self, tool: &ToolSpec, arguments: &Value) -> Result<Value, Box<ToolError>> {
        self.call_inner(tool, arguments)
    }
}

#[derive(Debug, Default)]
pub struct NoopProxy;

impl ToolProxy for NoopProxy {
    fn call(&mut self, tool: &ToolSpec, _arguments: &Value) -> Result<Value, Box<ToolError>> {
        Err(Box::new(ToolError::unavailable(format!(
            "daemon proxy for {} is not configured",
            tool.command
        ))))
    }
}

#[derive(Debug, serde::Serialize)]
struct DaemonFrame {
    cmd: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    args: Option<Value>,
    #[serde(skip_serializing_if = "str::is_empty")]
    session: String,
    #[serde(skip_serializing_if = "str::is_empty")]
    request_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    max_tokens: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    retrieval_surface: Option<String>,
}

#[derive(Debug, serde::Deserialize)]
struct DaemonResponse {
    success: bool,
    data: Option<Value>,
    error: Option<DaemonError>,
    #[serde(default)]
    warnings: Vec<Value>,
}

#[derive(Debug, serde::Deserialize)]
struct DaemonError {
    code: String,
    message: String,
    #[serde(default)]
    hint: Option<String>,
    #[serde(default)]
    details: Option<Value>,
    #[serde(default)]
    retryable: Option<bool>,
    #[serde(default)]
    requires_user_confirmation: Option<bool>,
    #[serde(default)]
    resume_hint: Option<String>,
}

impl DaemonResponse {
    fn into_tool_error(self) -> ToolError {
        let error = self.error.unwrap_or(DaemonError {
            code: "operation_failed".to_owned(),
            message: "daemon request failed".to_owned(),
            hint: None,
            details: None,
            retryable: None,
            requires_user_confirmation: None,
            resume_hint: None,
        });
        ToolError {
            code: redact_str(&error.code),
            message: redact_str(&error.message),
            hint: error.hint.as_deref().map(redact_str),
            retryable: error.retryable,
            requires_user_confirmation: error.requires_user_confirmation,
            resume_hint: error.resume_hint.as_deref().map(redact_str),
            details: error.details.as_ref().map(symbrowse_daemon::redact_json),
        }
    }

    fn into_result(self) -> Result<Value, Box<ToolError>> {
        if !self.success {
            return Err(Box::new(self.into_tool_error()));
        }
        let data = self.data.unwrap_or(Value::Null);
        if self.warnings.is_empty() {
            Ok(data)
        } else {
            Ok(json!({"data": data, "warnings": self.warnings}))
        }
    }
}

#[allow(clippy::collapsible_if)]
fn daemon_command(tool: &ToolSpec, input: &Value) -> String {
    if tool.name == "get" {
        if let Some(kind) = input.get("kind").and_then(Value::as_str) {
            return match kind {
                "visible" | "enabled" | "checked" => format!("is.{kind}"),
                _ => format!("get.{kind}"),
            };
        }
    }
    tool.command.to_owned()
}

fn daemon_args(tool: &ToolSpec, input: &Value) -> Option<Value> {
    let object = input.as_object().cloned().unwrap_or_default();
    if matches!(tool.name, "back" | "forward" | "reload") {
        return None;
    }
    let mut args = Map::new();
    let mut copy = |name: &str| {
        if let Some(value) = object.get(name) {
            args.insert(name.to_owned(), value.clone());
        }
    };
    match tool.name {
        "open" | "goto" => copy("url"),
        "snapshot" => {
            args.insert(
                "interactive".to_owned(),
                object
                    .get("interactive")
                    .cloned()
                    .unwrap_or(Value::Bool(false)),
            );
            args.insert(
                "compact".to_owned(),
                object.get("compact").cloned().unwrap_or(Value::Bool(false)),
            );
            args.insert(
                "depth".to_owned(),
                object.get("depth").cloned().unwrap_or(Value::from(0)),
            );
            args.insert(
                "urls".to_owned(),
                object.get("urls").cloned().unwrap_or(Value::Bool(false)),
            );
            if let Some(value) = object.get("selector") {
                args.insert("selector".to_owned(), value.clone());
            }
        }
        "click" => copy("selector"),
        "fill" => {
            copy("selector");
            copy("value");
        }
        "type" => {
            copy("selector");
            copy("value");
        }
        "press" => {
            copy("selector");
            copy("key");
        }
        "wait" => {
            for name in ["kind", "value", "state"] {
                copy(name);
            }
            if let Some(load) = object.get("load") {
                args.insert("load_state".to_owned(), load.clone());
            }
            if let Some(ms) = object.get("ms").and_then(Value::as_i64) {
                args.insert(
                    "duration".to_owned(),
                    Value::from(ms.saturating_mul(1_000_000)),
                );
            }
        }
        "read" => {
            args.insert(
                "url".to_owned(),
                object
                    .get("url")
                    .cloned()
                    .unwrap_or_else(|| Value::String(String::new())),
            );
        }
        "get" => {
            for name in ["kind", "selector", "attribute"] {
                copy(name);
            }
        }
        "find" => {
            for name in ["kind", "query", "action", "value", "name", "exact", "index"] {
                copy(name);
            }
        }
        "fetch_url" => {
            copy_fetch_fields(&object, &mut args, false);
        }
        "fetch_batch" => {
            copy_fetch_fields(&object, &mut args, true);
        }
        "cache_get" => {
            copy("cache_id");
            copy("range");
        }
        "wayback_snapshots" => {
            for name in ["url", "from", "to", "limit", "match_type"] {
                copy(name);
            }
        }
        _ => {
            for (name, value) in object {
                if name != "session" && name != "max_tokens" {
                    args.insert(name, value);
                }
            }
        }
    }
    Some(Value::Object(args))
}

#[allow(clippy::collapsible_if)]
fn copy_fetch_fields(input: &Map<String, Value>, output: &mut Map<String, Value>, batch: bool) {
    let strings = [
        "url",
        "format",
        "css_selector",
        "query",
        "schema_path",
        "wayback_timestamp",
        "injection_patterns",
    ];
    for name in strings {
        if let Some(value) = input.get(name) {
            output.insert(name.to_owned(), value.clone());
        }
    }
    for name in ["max_chars", "char_limit", "concurrency", "top_k"] {
        if let Some(value) = input.get(name) {
            output.insert(name.to_owned(), value.clone());
        }
    }
    for name in [
        "frontmatter",
        "include_links",
        "raw",
        "store_full_text",
        "no_cache",
        "wayback_fallback",
        "no_injection_scan",
        "content_boundaries",
    ] {
        if let Some(value) = input.get(name) {
            output.insert(name.to_owned(), value.clone());
        }
    }
    if batch {
        if let Some(value) = input.get("urls") {
            output.insert("urls".to_owned(), value.clone());
        }
    }
    output.insert(
        "retrieval_surface".to_owned(),
        Value::String("mcp".to_owned()),
    );
}

fn budgeted_command(command: &str) -> bool {
    matches!(
        command,
        "snapshot"
            | "read"
            | "get.html"
            | "console.list"
            | "errors.list"
            | "network.requests"
            | "a11y"
            | "network.har"
            | "fetch.url"
            | "fetch.batch"
    )
}

fn requested_max_tokens(command: &str, arguments: &Value) -> i64 {
    if !budgeted_command(command) {
        return 0;
    }
    arguments
        .get("max_tokens")
        .and_then(|value| {
            value
                .as_i64()
                .or_else(|| value.as_f64().map(|number| number as i64))
        })
        .filter(|value| *value > 0)
        .unwrap_or(MCP_DEFAULT_MAX_TOKENS)
}

fn request_id() -> String {
    format!(
        "{}",
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |value| value.as_nanos())
    )
}

fn valid_session(value: &str) -> bool {
    let mut chars = value.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    (first.is_ascii_alphanumeric())
        && chars.count() < 64
        && value.chars().all(|character| {
            character.is_ascii_alphanumeric() || matches!(character, '.' | '_' | '-')
        })
}

const CHILD_REAP_TIMEOUT: Duration = Duration::from_secs(1);

fn terminate_child(child: &mut Child) {
    #[cfg(unix)]
    {
        if let Ok(pid) = i32::try_from(child.id()) {
            let _ = nix::sys::signal::killpg(
                nix::unistd::Pid::from_raw(pid),
                nix::sys::signal::Signal::SIGKILL,
            );
        }
    }
    let _ = child.kill();
    let deadline = Instant::now() + CHILD_REAP_TIMEOUT;
    while Instant::now() < deadline {
        match child.try_wait() {
            Ok(Some(_)) | Err(_) => return,
            Ok(None) => thread::sleep(Duration::from_millis(10)),
        }
    }
    // Do not fall back to Child::wait: cleanup must remain finite even when a
    // platform process cannot be killed. The final try_wait preserves
    // best-effort reaping without an unbounded join.
    let _ = child.try_wait();
}

fn detach_command(command: &mut Command) {
    #[cfg(unix)]
    {
        command.process_group(0);
    }
    #[cfg(windows)]
    {
        const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
        const DETACHED_PROCESS: u32 = 0x0000_0008;
        command.creation_flags(CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS);
    }
}

fn current_executable() -> String {
    env::current_exe()
        .ok()
        .and_then(|path| path.into_os_string().into_string().ok())
        .unwrap_or_else(|| "symbrowse".to_owned())
}

fn default_endpoint(session: &str) -> Result<String, String> {
    symbrowse_daemon::default_socket_path(session)
        .into_os_string()
        .into_string()
        .map_err(|_| "daemon endpoint is not valid UTF-8".to_owned())
}

#[cfg(windows)]
async fn read_response_windows(
    transport: &mut (impl tokio::io::AsyncRead + Unpin),
) -> io::Result<DaemonResponse> {
    use tokio::io::AsyncReadExt;

    let mut line = Vec::new();
    let mut byte = [0_u8; 1];
    loop {
        let read = transport.read(&mut byte).await?;
        if read == 0 {
            if line.is_empty() {
                return Err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "daemon closed connection without a response",
                ));
            }
            break;
        }
        if line.len().saturating_add(read) > MAX_DAEMON_FRAME_BYTES {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "daemon response exceeds size limit",
            ));
        }
        line.push(byte[0]);
        if byte[0] == b'\n' {
            break;
        }
    }
    serde_json::from_slice(line.trim_ascii_end()).map_err(|error| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            format!("decode daemon response: {error}"),
        )
    })
}

#[cfg(unix)]
type DaemonTransport = std::os::unix::net::UnixStream;

#[cfg(unix)]
fn write_frame(
    transport: &mut DaemonTransport,
    payload: &[u8],
    timeout: Duration,
) -> io::Result<()> {
    #[cfg(unix)]
    {
        transport.set_write_timeout(Some(timeout))?;
    }
    transport.write_all(payload)?;
    transport.write_all(b"\n")?;
    transport.flush()
}

#[cfg(unix)]
fn read_response(transport: &mut DaemonTransport, timeout: Duration) -> io::Result<DaemonResponse> {
    #[cfg(unix)]
    {
        transport.set_read_timeout(Some(timeout))?;
    }
    let mut line = Vec::new();
    let mut reader = BufReader::new(transport);
    let read = read_limited_line(&mut reader, &mut line, MAX_DAEMON_FRAME_BYTES)?;
    if read == 0 {
        return Err(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            "daemon closed connection without a response",
        ));
    }
    serde_json::from_slice(line.trim_ascii_end()).map_err(|error| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            format!("decode daemon response: {error}"),
        )
    })
}

#[cfg(unix)]
fn read_limited_line<R: BufRead>(
    reader: &mut R,
    output: &mut Vec<u8>,
    limit: usize,
) -> io::Result<usize> {
    output.clear();
    loop {
        let available = reader.fill_buf()?;
        if available.is_empty() {
            return Ok(output.len());
        }
        let take = available
            .iter()
            .position(|byte| *byte == b'\n')
            .map_or(available.len(), |index| index + 1);
        if output.len().saturating_add(take) > limit {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "daemon response exceeds size limit",
            ));
        }
        output.extend_from_slice(&available[..take]);
        reader.consume(take);
        if output.last() == Some(&b'\n') {
            return Ok(output.len());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(unix)]
    #[test]
    fn runtime_proxy_sends_daemon_frame_and_preserves_warnings() {
        use std::{
            io::{BufRead, BufReader, Write},
            os::unix::net::UnixListener,
            sync::mpsc,
        };

        let path = std::env::temp_dir().join(format!("symbrowse-mcp-test-{}.sock", request_id()));
        let listener = UnixListener::bind(&path).expect("bind daemon fixture socket");
        let (seen, receive) = mpsc::channel();
        let server = std::thread::spawn(move || {
            let (stream, _) = listener.accept().expect("accept daemon fixture");
            let mut reader = BufReader::new(stream.try_clone().expect("clone fixture stream"));
            let mut line = String::new();
            reader.read_line(&mut line).expect("read daemon frame");
            let frame: Value = serde_json::from_str(&line).expect("decode daemon frame");
            let mut stream = stream;
            if frame["cmd"] == "daemon.status" {
                stream
                    .write_all(br#"{"success":true,"data":{"session":"test","engine":"chrome","policy":{"allow_private":false}}}"#)
                    .expect("write status response");
                stream.write_all(b"\n").expect("terminate status response");
                let (stream2, _) = listener.accept().expect("accept daemon request");
                let mut reader2 =
                    BufReader::new(stream2.try_clone().expect("clone fixture request stream"));
                let mut line2 = String::new();
                reader2.read_line(&mut line2).expect("read daemon request");
                seen.send(serde_json::from_str::<Value>(&line2).expect("decode daemon frame"))
                    .expect("send observed frame");
                let mut stream2 = stream2;
                stream2
                    .write_all(
                        br#"{"success":true,"data":{"ok":true},"warnings":[{"kind":"policy","message":"denied"}]}"#,
                    )
                    .expect("write daemon response");
                stream2.write_all(b"\n").expect("terminate daemon response");
            } else {
                seen.send(frame).expect("send observed frame");
                stream
                    .write_all(
                        br#"{"success":true,"data":{"ok":true},"warnings":[{"kind":"policy","message":"denied"}]}"#,
                    )
                    .expect("write daemon response");
                stream.write_all(b"\n").expect("terminate daemon response");
            }
        });
        let mut proxy = DaemonProxy::new(DaemonProxyOptions {
            session: "test".to_owned(),
            endpoint: Some(path.to_string_lossy().into_owned()),
            read_timeout: Duration::from_secs(1),
            startup_timeout: Duration::from_millis(10),
            ..DaemonProxyOptions::default()
        });
        let spec = ToolSpec {
            name: "open",
            canonical: "open",
            command: "open",
            profile: "core",
        };
        let result = proxy
            .call(
                &spec,
                &json!({"url":"https://example.com", "session":"test"}),
            )
            .expect("proxy call");
        assert_eq!(result["data"]["ok"], Value::Bool(true));
        assert_eq!(
            result["warnings"][0]["kind"],
            Value::String("policy".to_owned())
        );
        let frame = receive.recv().expect("observed daemon frame");
        assert_eq!(frame["cmd"], Value::String("open".to_owned()));
        assert_eq!(
            frame["args"]["url"],
            Value::String("https://example.com".to_owned())
        );
        assert_eq!(frame["session"], Value::String("test".to_owned()));
        server.join().expect("daemon fixture thread");
        let _ = std::fs::remove_file(path);
    }

    #[cfg(unix)]
    #[test]
    fn failed_status_preserves_error_without_stop_or_autostart() {
        use std::{
            io::{BufRead, BufReader, Write},
            sync::mpsc,
            thread,
            time::{Duration, Instant},
        };

        use std::os::unix::net::UnixListener;

        let path = std::env::temp_dir().join(format!(
            "symbrowse-mcp-status-failure-{}.sock",
            request_id()
        ));
        let listener = UnixListener::bind(&path).expect("bind daemon fixture socket");
        listener
            .set_nonblocking(true)
            .expect("make daemon fixture nonblocking");
        let (commands, receive) = mpsc::channel();
        let server = thread::spawn(move || {
            let startup_deadline = Instant::now() + Duration::from_secs(2);
            let mut cleanup_deadline = None;
            while Instant::now() < cleanup_deadline.unwrap_or(startup_deadline) {
                match listener.accept() {
                    Ok((stream, _)) => {
                        let mut reader = BufReader::new(stream);
                        let mut line = String::new();
                        reader.read_line(&mut line).expect("read daemon frame");
                        let frame: Value =
                            serde_json::from_str(&line).expect("decode daemon frame");
                        commands
                            .send(frame["cmd"].clone())
                            .expect("record daemon command");
                        let mut writer = reader.into_inner();
                        if frame["cmd"] == "daemon.status" {
                            cleanup_deadline = Some(Instant::now() + Duration::from_millis(250));
                            writer
                                .write_all(
                                    br#"{"success":false,"error":{"code":"daemon_unavailable","message":"status backend failed","hint":"status hint","details":{"source":"fixture"},"retryable":true}}"#,
                                )
                                .expect("write failed status");
                            writer.write_all(b"\n").expect("terminate failed status");
                        }
                    }
                    Err(error) if error.kind() == io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(1));
                    }
                    Err(error) => panic!("fixture listener failed: {error}"),
                }
            }
        });

        let mut proxy = DaemonProxy::new(DaemonProxyOptions {
            session: "test".to_owned(),
            endpoint: Some(path.to_string_lossy().into_owned()),
            executable: "/definitely/missing/symbrowse".to_owned(),
            read_timeout: Duration::from_millis(100),
            startup_timeout: Duration::from_millis(100),
            ..DaemonProxyOptions::default()
        });
        let spec = ToolSpec {
            name: "open",
            canonical: "open",
            command: "open",
            profile: "core",
        };
        let error = proxy
            .call(
                &spec,
                &json!({"url":"https://example.com", "session":"test"}),
            )
            .expect_err("failed status must reach the MCP caller");
        assert_eq!(error.code, "daemon_unavailable");
        assert_eq!(error.message, "status backend failed");
        assert_eq!(error.hint.as_deref(), Some("status hint"));
        assert_eq!(error.details, Some(json!({"source": "fixture"})));
        assert_eq!(error.retryable, Some(true));
        server.join().expect("daemon fixture thread");
        let seen: Vec<_> = receive.try_iter().collect();
        assert_eq!(
            seen.len(),
            1,
            "status failure must not trigger follow-up commands"
        );
        assert_eq!(seen[0], Value::String("daemon.status".to_owned()));
        let _ = std::fs::remove_file(path);
    }

    #[test]
    fn aliases_and_budget_commands_map_to_daemon_frames() {
        let spec = ToolSpec {
            name: "goto",
            canonical: "open",
            command: "open",
            profile: "core",
        };
        let args = daemon_args(
            &spec,
            &json!({"url":"https://example.com", "session":"x", "max_tokens": 1}),
        );
        assert_eq!(args, Some(json!({"url":"https://example.com"})));
        assert!(budgeted_command("fetch.url"));
        assert!(!budgeted_command("open"));

        let fetch_batch = ToolSpec {
            name: "fetch_batch",
            canonical: "fetch_batch",
            command: "fetch.batch",
            profile: "fetch",
        };
        let arguments = json!({"urls":["https://example.com"], "max_tokens":1.5});
        assert_eq!(requested_max_tokens("fetch.batch", &arguments), 1);
        assert_eq!(
            daemon_args(&fetch_batch, &arguments),
            Some(json!({"urls":["https://example.com"], "retrieval_surface":"mcp"}))
        );
    }

    #[test]
    fn wait_milliseconds_use_go_duration_nanoseconds() {
        let spec = ToolSpec {
            name: "wait",
            canonical: "wait",
            command: "wait",
            profile: "core",
        };
        assert_eq!(
            daemon_args(&spec, &json!({"kind":"ms", "ms": 25})),
            Some(json!({"kind":"ms", "duration":25_000_000}))
        );
    }

    #[cfg(unix)]
    #[test]
    fn daemon_frame_limit_applies_while_reading() {
        let mut reader = BufReader::with_capacity(4, io::Cursor::new(b"123456789\n"));
        let mut output = Vec::new();
        let error = read_limited_line(&mut reader, &mut output, 8).expect_err("oversized frame");
        assert_eq!(error.kind(), io::ErrorKind::InvalidData);
        assert!(output.len() <= 8);
    }

    #[test]
    fn session_validation_matches_daemon_contract() {
        assert!(valid_session("default-1"));
        assert!(!valid_session("../escape"));
        assert!(!valid_session(""));
    }

    #[test]
    fn transport_metadata_redacts_endpoint_and_error_text() {
        let error = ToolError::transport(
            "daemon_unavailable",
            "password=topsecret",
            "safe",
            "https://user:secret@example.test/socket",
        );
        let rendered = error.metadata().to_string();
        assert!(!rendered.contains("topsecret"));
        assert!(!rendered.contains("secret@example"));
        assert!(rendered.contains("[REDACTED]"));
    }

    #[cfg(unix)]
    #[test]
    fn terminate_child_reaps_within_bounded_cleanup_window() {
        let mut child = Command::new("/bin/sh")
            .args(["-c", "sleep 10"])
            .spawn()
            .expect("spawn controllable child");
        terminate_child(&mut child);
        assert!(
            child
                .try_wait()
                .expect("check child after bounded cleanup")
                .is_some(),
            "killed child should be reaped by the bounded cleanup loop"
        );
    }
}
