#![deny(unsafe_code)]

//! Versioned, bounded NDJSON boundary for the legacy Go/AzureTLS transport.
//! The wire profile is intentionally not a browser identity: callers select
//! `compat` explicitly and receive only transport data.

use serde::{Deserialize, Serialize};
use std::{
    io,
    path::{Path, PathBuf},
    process::Stdio,
    sync::atomic::{AtomicU64, Ordering},
    time::Duration,
};
use tokio::{
    io::{AsyncBufReadExt, AsyncWriteExt, BufReader},
    process::{Child, ChildStdin, ChildStdout, Command},
    time::timeout,
};

pub const PROTOCOL_VERSION: u32 = 1;
pub const MAX_FRAME_BYTES: usize = 1 << 20;
pub const MAX_BODY_BYTES: usize = 10 << 20;

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum Frame {
    Handshake {
        protocol: u32,
        component: String,
        oracle: String,
    },
    HandshakeAck {
        protocol: u32,
        component: String,
        oracle: String,
    },
    Request(Request),
    Response(Response),
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct Request {
    pub id: u64,
    pub method: String,
    pub url: String,
    pub profile: String,
    #[serde(default)]
    pub headers: Vec<(String, String)>,
    #[serde(default)]
    pub body: String,
    pub timeout_ms: u64,
    pub max_body_bytes: usize,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct Response {
    pub id: u64,
    pub ok: bool,
    #[serde(default)]
    pub status: u16,
    #[serde(default)]
    pub final_url: String,
    #[serde(default)]
    pub headers: Vec<(String, Vec<String>)>,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub error: Option<TypedError>,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq, Eq)]
pub struct TypedError {
    pub code: String,
    pub message: String,
    #[serde(default)]
    pub retryable: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct EndpointPermissions {
    pub path: PathBuf,
    pub directory_mode: u32,
    pub endpoint_mode: u32,
    pub private: bool,
}

#[derive(Debug)]
pub enum CompatError {
    Io(io::Error),
    Json(serde_json::Error),
    Protocol(TypedError),
    Timeout,
    SidecarExited,
    Integrity(TypedError),
    FrameTooLarge,
}
impl std::fmt::Display for CompatError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{self:?}")
    }
}
impl std::error::Error for CompatError {}
impl From<io::Error> for CompatError {
    fn from(e: io::Error) -> Self {
        Self::Io(e)
    }
}
impl From<serde_json::Error> for CompatError {
    fn from(e: serde_json::Error) -> Self {
        Self::Json(e)
    }
}

/// Creates the private runtime directory used by endpoint-based deployments.
/// The default stdio transport remains portable; this schema is shared by
/// socket and stdio launchers and is tested so a future socket cannot weaken it.
pub fn private_endpoint(
    root: impl AsRef<Path>,
    name: &str,
) -> Result<EndpointPermissions, CompatError> {
    let dir = root.as_ref().join("symbrowse-compat");
    std::fs::create_dir_all(&dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700))?;
    }
    let path = dir.join(name);
    Ok(EndpointPermissions {
        path,
        directory_mode: 0o700,
        endpoint_mode: 0o600,
        private: true,
    })
}

pub struct CompatClient {
    command: String,
    args: Vec<String>,
    child: Option<Child>,
    stdin: Option<ChildStdin>,
    stdout: Option<BufReader<ChildStdout>>,
    next_id: AtomicU64,
    request_timeout: Duration,
}
impl CompatClient {
    pub fn new(
        command: impl Into<String>,
        args: impl IntoIterator<Item = String>,
        request_timeout: Duration,
    ) -> Self {
        Self {
            command: command.into(),
            args: args.into_iter().collect(),
            child: None,
            stdin: None,
            stdout: None,
            next_id: AtomicU64::new(1),
            request_timeout,
        }
    }
    pub fn from_environment(timeout: Duration) -> Self {
        let command =
            std::env::var("SYMBROWSE_COMPAT_BINARY").unwrap_or_else(|_| "symbrowse".into());
        Self::new(command, ["compat-sidecar".to_owned()], timeout)
    }
    async fn start(&mut self) -> Result<(), CompatError> {
        let mut command = Command::new(&self.command);
        command
            .args(&self.args)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null());
        let mut child = command.spawn().map_err(|_| CompatError::SidecarExited)?;
        let mut stdin = child.stdin.take().ok_or(CompatError::SidecarExited)?;
        let stdout = child.stdout.take().ok_or(CompatError::SidecarExited)?;
        write_frame(
            &mut stdin,
            &Frame::Handshake {
                protocol: PROTOCOL_VERSION,
                component: "symbrowse-rust".into(),
                oracle: "go-azuretls-v0.8.0".into(),
            },
        )
        .await?;
        let mut reader = BufReader::new(stdout);
        let line = read_line(&mut reader).await?;
        let ack: Frame = serde_json::from_slice(&line)?;
        match ack {
            Frame::HandshakeAck { protocol, .. } if protocol == PROTOCOL_VERSION => {}
            Frame::HandshakeAck { protocol, .. } => {
                return Err(CompatError::Integrity(TypedError {
                    code: "compat_protocol_mismatch".into(),
                    message: format!("unsupported sidecar protocol {protocol}"),
                    retryable: false,
                }));
            }
            _ => {
                return Err(CompatError::Integrity(TypedError {
                    code: "compat_handshake_invalid".into(),
                    message: "sidecar did not acknowledge handshake".into(),
                    retryable: false,
                }));
            }
        }
        self.stdin = Some(stdin);
        self.stdout = Some(reader);
        self.child = Some(child);
        Ok(())
    }
    pub async fn request(&mut self, mut request: Request) -> Result<Response, CompatError> {
        for attempt in 0..2 {
            if self.stdin.is_none() {
                self.start().await?;
            }
            request.id = self.next_id.fetch_add(1, Ordering::Relaxed);
            let result = async {
                write_frame(
                    self.stdin.as_mut().ok_or(CompatError::SidecarExited)?,
                    &Frame::Request(request.clone()),
                )
                .await?;
                let line =
                    read_line(self.stdout.as_mut().ok_or(CompatError::SidecarExited)?).await?;
                let frame: Frame = serde_json::from_slice(&line)?;
                match frame {
                    Frame::Response(response) if response.id == request.id => Ok(response),
                    _ => Err(CompatError::Integrity(TypedError {
                        code: "compat_request_id_mismatch".into(),
                        message: "sidecar response id did not match request".into(),
                        retryable: false,
                    })),
                }
            }
            .await;
            match result {
                Ok(response) => return Ok(response),
                Err(error)
                    if attempt == 0
                        && matches!(error, CompatError::Io(_) | CompatError::SidecarExited) =>
                {
                    self.restart().await;
                }
                Err(error) => return Err(error),
            }
        }
        Err(CompatError::SidecarExited)
    }
    pub async fn request_with_timeout(
        &mut self,
        request: Request,
    ) -> Result<Response, CompatError> {
        timeout(self.request_timeout, self.request(request))
            .await
            .map_err(|_| CompatError::Timeout)?
    }
    async fn restart(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill().await;
        }
        self.stdin = None;
        self.stdout = None;
    }
}
async fn write_frame(stdin: &mut ChildStdin, frame: &Frame) -> Result<(), CompatError> {
    let bytes = serde_json::to_vec(frame)?;
    if bytes.len() > MAX_FRAME_BYTES {
        return Err(CompatError::FrameTooLarge);
    }
    stdin.write_all(&bytes).await?;
    stdin.write_all(b"\n").await?;
    stdin.flush().await?;
    Ok(())
}
async fn read_line(reader: &mut BufReader<ChildStdout>) -> Result<Vec<u8>, CompatError> {
    let mut line = Vec::new();
    let n = reader.read_until(b'\n', &mut line).await?;
    if n == 0 {
        return Err(CompatError::SidecarExited);
    }
    if line.len() > MAX_FRAME_BYTES {
        return Err(CompatError::FrameTooLarge);
    }
    Ok(line)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn endpoint_schema_is_private() {
        let root =
            std::env::temp_dir().join(format!("symbrowse-compat-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let endpoint = private_endpoint(&root, "sidecar.sock").unwrap();
        assert!(endpoint.private);
        assert_eq!(endpoint.directory_mode, 0o700);
        assert_eq!(endpoint.endpoint_mode, 0o600);
        let _ = std::fs::remove_dir_all(root);
    }
    #[test]
    fn frames_are_versioned_and_typed() {
        let frame = Frame::Handshake {
            protocol: PROTOCOL_VERSION,
            component: "x".into(),
            oracle: "y".into(),
        };
        let raw = serde_json::to_string(&frame).unwrap();
        assert!(raw.contains("handshake"));
        assert!(raw.contains("protocol"));
    }
}
