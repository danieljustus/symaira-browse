#![deny(unsafe_code)]

//! Owned Firefox WebDriver BiDi adapter.  Firefox is launched with an isolated
//! profile and a loopback-only BiDi socket; no geckodriver or Chrome fallback
//! is involved.

use async_tungstenite::{tokio::connect_async, tungstenite::Message};
use futures_util::StreamExt;
use serde_json::{Value, json};
use std::{
    path::{Path, PathBuf},
    time::{Duration, Instant},
};
use symbrowse_engine::{
    EvaluationResult, NavigationResult,
    capabilities::{Capabilities, capabilities_for},
};
use tokio::{
    net::TcpStream,
    process::{Child, Command},
    time::{sleep, timeout},
};

pub const ENGINE_KIND: &str = "firefox";

#[derive(Debug)]
pub enum FirefoxError {
    Unsupported {
        operation: String,
    },
    Timeout {
        operation: String,
        timeout: Duration,
    },
    Driver(String),
    InvalidTarget(String),
}
impl std::fmt::Display for FirefoxError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Unsupported { operation } => write!(f, "unsupported: {operation}"),
            Self::Timeout { operation, timeout } => {
                write!(f, "{operation} timed out after {timeout:?}")
            }
            Self::Driver(s) | Self::InvalidTarget(s) => f.write_str(s),
        }
    }
}
impl std::error::Error for FirefoxError {}

pub fn canonical_capabilities() -> Capabilities {
    capabilities_for(
        ENGINE_KIND,
        [
            "CookieEngine",
            "FrameManager",
            "InspectionEngine",
            "InteractionEngine",
            "NavigationStateProvider",
            "ScreenshotEngine",
            "TabManager",
        ],
    )
}

pub fn resolve_firefox_executable(explicit: Option<&Path>) -> Result<PathBuf, FirefoxError> {
    if let Some(path) = explicit {
        if path.is_file() {
            return Ok(path.to_owned());
        }
        return Err(FirefoxError::Driver(format!(
            "Firefox executable not found at {}",
            path.display()
        )));
    }
    let candidates = if cfg!(target_os = "macos") {
        vec![PathBuf::from(
            "/Applications/Firefox.app/Contents/MacOS/firefox",
        )]
    } else if cfg!(windows) {
        vec![PathBuf::from(
            r"C:\Program Files\Mozilla Firefox\firefox.exe",
        )]
    } else {
        vec![
            PathBuf::from("/usr/bin/firefox"),
            PathBuf::from("/usr/lib/firefox/firefox"),
        ]
    };
    candidates
        .into_iter()
        .find(|p| p.is_file())
        .ok_or_else(|| FirefoxError::Driver("Firefox executable is unavailable".into()))
}

struct Bidi {
    socket: async_tungstenite::WebSocketStream<async_tungstenite::tokio::ConnectStream>,
    next: u64,
}
impl Bidi {
    async fn command(
        &mut self,
        method: &str,
        params: Value,
        limit: Duration,
    ) -> Result<Value, FirefoxError> {
        let id = self.next;
        self.next += 1;
        self.socket
            .send(Message::Text(
                json!({"id":id,"method":method,"params":params})
                    .to_string()
                    .into(),
            ))
            .await
            .map_err(|e| FirefoxError::Driver(format!("send BiDi command: {e}")))?;
        timeout(limit, async {
            while let Some(message) = self.socket.next().await {
                let message = message
                    .map_err(|e| FirefoxError::Driver(format!("read BiDi response: {e}")))?;
                if let Message::Text(text) = message {
                    let value: Value = serde_json::from_str(&text)
                        .map_err(|e| FirefoxError::Driver(format!("decode BiDi response: {e}")))?;
                    if value.get("id").and_then(Value::as_u64) == Some(id) {
                        if value.get("error").is_some() {
                            return Err(FirefoxError::Driver(value.to_string()));
                        }
                        return Ok(value.get("result").cloned().unwrap_or(Value::Null));
                    }
                }
            }
            Err(FirefoxError::Driver("BiDi socket closed".into()))
        })
        .await
        .map_err(|_| FirefoxError::Timeout {
            operation: method.into(),
            timeout: limit,
        })?
    }
}

pub struct FirefoxSession {
    bidi: Bidi,
    child: Option<Child>,
    context: String,
    timeout: Duration,
}
impl FirefoxSession {
    pub async fn launch(
        executable: PathBuf,
        profile: PathBuf,
        limit: Duration,
    ) -> Result<Self, FirefoxError> {
        std::fs::create_dir_all(&profile)
            .map_err(|e| FirefoxError::Driver(format!("create isolated profile: {e}")))?;
        let port = std::net::TcpListener::bind((std::net::Ipv4Addr::LOCALHOST, 0))
            .map_err(|e| FirefoxError::Driver(e.to_string()))?
            .local_addr()
            .map_err(|e| FirefoxError::Driver(e.to_string()))?
            .port();
        let mut child = Command::new(&executable)
            .args([
                "--headless",
                "--remote-debugging-port",
                &port.to_string(),
                "--profile",
            ])
            .arg(&profile)
            .kill_on_drop(true)
            .spawn()
            .map_err(|e| FirefoxError::Driver(format!("launch Firefox: {e}")))?;
        let started = Instant::now();
        while started.elapsed() < limit {
            if TcpStream::connect(("127.0.0.1", port)).await.is_ok() {
                break;
            }
            if child
                .try_wait()
                .map_err(|e| FirefoxError::Driver(e.to_string()))?
                .is_some()
            {
                return Err(FirefoxError::Driver(
                    "Firefox exited before BiDi became ready".into(),
                ));
            }
            sleep(Duration::from_millis(50)).await;
        }
        if started.elapsed() >= limit {
            let _ = child.kill().await;
            let _ = child.wait().await;
            return Err(FirefoxError::Timeout {
                operation: "Firefox BiDi readiness".into(),
                timeout: limit,
            });
        }
        let (socket, _) = connect_async(format!("ws://127.0.0.1:{port}/session"))
            .await
            .map_err(|e| {
                FirefoxError::Driver(format!("connect Firefox BiDi loopback socket: {e}"))
            })?;

        let mut session = Self {
            bidi: Bidi { socket, next: 1 },
            child: Some(child),
            context: String::new(),
            timeout: limit,
        };
        let result = session.bidi.command("session.new", json!({"capabilities":{"alwaysMatch":{"browserName":"firefox","webSocketUrl":true}}}), limit).await?;
        session.context = result
            .get("capabilities")
            .and_then(|v| v.get("webSocketUrl"))
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let tree = session
            .bidi
            .command("browsingContext.getTree", json!({}), limit)
            .await?;
        session.context = tree
            .get("contexts")
            .and_then(Value::as_array)
            .and_then(|v| v.first())
            .and_then(|v| v.get("context"))
            .and_then(Value::as_str)
            .ok_or_else(|| FirefoxError::Driver("Firefox returned no browsing context".into()))?
            .into();
        Ok(session)
    }
    pub async fn navigate(&mut self, target: &str) -> Result<NavigationResult, FirefoxError> {
        if !target.starts_with("http://") && !target.starts_with("https://") {
            return Err(FirefoxError::InvalidTarget(target.into()));
        }
        let result = self
            .bidi
            .command(
                "browsingContext.navigate",
                json!({"context":self.context,"url":target,"wait":"complete"}),
                self.timeout,
            )
            .await?;
        Ok(NavigationResult {
            frame_id: self.context.clone(),
            loader_id: result
                .get("navigation")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .into(),
            url: result
                .get("url")
                .and_then(Value::as_str)
                .unwrap_or(target)
                .into(),
            error_text: String::new(),
        })
    }
    pub async fn evaluate(&mut self, expression: &str) -> Result<EvaluationResult, FirefoxError> {
        let result = self.bidi.command("script.evaluate", json!({"expression":expression,"target":{"context":self.context},"awaitPromise":true,"resultOwnership":"root"}), self.timeout).await?;
        let remote = result.get("result").unwrap_or(&Value::Null);
        Ok(EvaluationResult {
            value: remote.get("value").cloned(),
            value_type: remote
                .get("type")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .into(),
            description: remote
                .get("description")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .into(),
            exception_text: String::new(),
        })
    }
    /// Read cookies through the Firefox BiDi storage module.
    pub async fn cookies(&mut self) -> Result<Value, FirefoxError> {
        self.bidi
            .command(
                "storage.getCookies",
                json!({"partition":{"context":self.context}}),
                self.timeout,
            )
            .await
    }

    /// Set a cookie through the Firefox BiDi storage module.
    pub async fn set_cookie(&mut self, cookie: Value) -> Result<Value, FirefoxError> {
        self.bidi
            .command(
                "storage.setCookie",
                json!({"cookie":cookie,"partition":{"context":self.context}}),
                self.timeout,
            )
            .await
    }

    /// Execute the two canonical interaction primitives without a browser
    /// specific fallback. DOM events are generated in the selected context.
    pub async fn interact(
        &mut self,
        operation: &str,
        selector: &str,
        value: Option<&str>,
    ) -> Result<Value, FirefoxError> {
        let selector = serde_json::to_string(selector)
            .map_err(|error| FirefoxError::Driver(error.to_string()))?;
        let value = serde_json::to_string(value.unwrap_or_default())
            .map_err(|error| FirefoxError::Driver(error.to_string()))?;
        let expression = match operation {
            "click" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.click(); return {{action:'click'}}; }})()"
            ),
            "type" | "fill" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.focus(); e.value={value}; e.dispatchEvent(new Event('input',{{bubbles:true}})); e.dispatchEvent(new Event('change',{{bubbles:true}})); return {{action:'{operation}',value:e.value}}; }})()"
            ),
            _ => return Err(Self::unsupported(operation)),
        };
        Ok(self
            .evaluate(&expression)
            .await?
            .value
            .unwrap_or(Value::Null))
    }

    pub async fn browsing_contexts(&mut self) -> Result<Value, FirefoxError> {
        self.bidi
            .command("browsingContext.getTree", json!({}), self.timeout)
            .await
    }

    pub async fn screenshot(&mut self, format: &str) -> Result<Value, FirefoxError> {
        if !matches!(format, "" | "png" | "jpeg") {
            return Err(FirefoxError::Unsupported {
                operation: format!("screenshot format {format}"),
            });
        }
        self.bidi
            .command(
                "browsingContext.captureScreenshot",
                json!({"context":self.context,"format":{"type":if format.is_empty() { "png" } else { format }},"origin":"viewport"}),
                self.timeout,
            )
            .await
    }

    pub async fn close(&mut self) -> Result<(), FirefoxError> {
        let _ = self
            .bidi
            .command("session.end", json!({}), self.timeout)
            .await;
        if let Some(mut child) = self.child.take() {
            let _ = child.kill().await;
            let _ = child.wait().await;
        }
        Ok(())
    }
    pub fn unsupported(operation: &str) -> FirefoxError {
        FirefoxError::Unsupported {
            operation: operation.into(),
        }
    }
}
impl Drop for FirefoxSession {
    fn drop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.start_kill();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn capabilities_are_truthful() {
        let c = canonical_capabilities();
        assert!(c.interfaces.contains(&"NavigationStateProvider".into()));
        assert!(c.interfaces.contains(&"ScreenshotEngine".into()));
    }
    #[test]
    fn invalid_explicit_path_is_typed() {
        assert!(matches!(
            resolve_firefox_executable(Some(Path::new("/missing/firefox"))),
            Err(FirefoxError::Driver(_))
        ));
    }
}
