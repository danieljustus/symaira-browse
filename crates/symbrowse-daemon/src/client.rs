use crate::{
    DaemonError, Frame, MAX_FRAME_BYTES, Response, codes, default_log_path, default_socket_path,
    redact_json, redact_str,
};
use serde_json::Value;
#[cfg(unix)]
use std::io::{BufRead, BufReader, Write};
#[cfg(unix)]
use std::os::fd::{AsFd, AsRawFd};
#[cfg(unix)]
use std::os::unix::net::UnixStream;
#[cfg(unix)]
use std::os::unix::process::CommandExt;
#[cfg(windows)]
use std::os::windows::process::CommandExt;
#[cfg(unix)]
use std::path::Path;
use std::{
    fs::{self, OpenOptions},
    io,
    path::PathBuf,
    process::{Child, Command, Stdio},
    thread,
    time::{Duration, Instant},
};

#[derive(Clone, Debug)]
pub struct StartOptions {
    pub executable: PathBuf,
    pub log_path: PathBuf,
    pub args: Vec<String>,
}

#[derive(Clone, Debug)]
pub struct ClientOptions {
    pub socket_path: PathBuf,
    pub session: String,
    pub read_timeout: Duration,
    pub startup_timeout: Duration,
    pub autostart: bool,
    pub start: Option<StartOptions>,
    pub expected_engine: Option<String>,
    pub expected_policy: Option<crate::PolicyStatus>,
}
impl Default for ClientOptions {
    fn default() -> Self {
        Self {
            socket_path: default_socket_path("default"),
            session: "default".into(),
            read_timeout: Duration::from_millis(crate::DEFAULT_READ_TIMEOUT_MS),
            startup_timeout: Duration::from_millis(crate::DEFAULT_STARTUP_TIMEOUT_MS),
            autostart: std::env::var("SYMBROWSE_NO_AUTOSTART").as_deref() != Ok("1"),
            start: None,
            expected_engine: None,
            expected_policy: None,
        }
    }
}

#[derive(Debug)]
pub enum ClientError {
    Transport(DaemonError),
    Io(io::Error),
    Unsupported,
}
impl std::fmt::Display for ClientError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Transport(error) => error.fmt(f),
            Self::Io(error) => write!(f, "{}", redact_str(&error.to_string())),
            Self::Unsupported => f.write_str("daemon sockets are not supported on this platform"),
        }
    }
}
impl std::error::Error for ClientError {}
impl From<io::Error> for ClientError {
    fn from(value: io::Error) -> Self {
        Self::Io(value)
    }
}

pub struct Client {
    options: ClientOptions,
}
impl Client {
    pub fn new(mut options: ClientOptions) -> Self {
        if options.session.is_empty() {
            options.session = "default".into();
        }
        if options.socket_path.as_os_str().is_empty() {
            options.socket_path = default_socket_path(&options.session);
        }
        if options.read_timeout.is_zero() {
            options.read_timeout = Duration::from_millis(crate::DEFAULT_READ_TIMEOUT_MS);
        }
        if options.startup_timeout.is_zero() {
            options.startup_timeout = Duration::from_millis(crate::DEFAULT_STARTUP_TIMEOUT_MS);
        }
        Self { options }
    }
    pub fn options(&self) -> &ClientOptions {
        &self.options
    }
    pub fn request(&self, mut frame: Frame) -> Result<Response, ClientError> {
        if frame.session.is_empty() {
            frame.session = self.options.session.clone();
        }
        if !crate::validate_session(&frame.session) {
            return Err(ClientError::Transport(DaemonError {
                code: codes::INVALID_SESSION.into(),
                message: format!("invalid session {}", redact_str(&frame.session)),
                ..Default::default()
            }));
        }
        match self.checked_request(&frame) {
            Ok(response) => Ok(response),
            Err(error) if self.options.autostart && should_autostart(&error) => {
                let mut child = self.start_daemon()?;
                let deadline = Instant::now() + self.options.startup_timeout;
                let mut last = error;
                while Instant::now() < deadline {
                    thread::sleep(Duration::from_millis(25));
                    match self.checked_request(&frame) {
                        Ok(response) => {
                            // Dropping Child closes this client's process handle
                            // without killing the detached daemon. Forgetting it
                            // leaks the handle for every autostarted request.
                            drop(child);
                            return Ok(response);
                        }
                        Err(error) => last = error,
                    }
                }
                terminate_child(&mut child);
                Err(last)
            }
            Err(error) => Err(error),
        }
    }
    pub fn request_without_autostart(&self, frame: Frame) -> Result<Response, ClientError> {
        self.checked_request(&frame)
    }

    fn checked_request(&self, frame: &Frame) -> Result<Response, ClientError> {
        let response = self.request_once(frame)?;
        if !matches!(frame.cmd.as_str(), "daemon.status" | "daemon.stop") {
            self.verify_status()?;
        }
        Ok(response)
    }

    fn verify_status(&self) -> Result<(), ClientError> {
        let status = self.request_once(&Frame {
            cmd: "daemon.status".into(),
            session: self.options.session.clone(),
            ..Frame::default()
        })?;
        if !status.success {
            return Err(ClientError::Transport(status.error.unwrap_or_else(|| {
                DaemonError {
                    code: codes::OPERATION_FAILED.into(),
                    message: "daemon status request failed".into(),
                    ..Default::default()
                }
            })));
        }
        let data = status.data.unwrap_or(Value::Null);
        let session_ok =
            data.get("session").and_then(Value::as_str) == Some(self.options.session.as_str());
        let engine_ok = self
            .options
            .expected_engine
            .as_ref()
            .is_none_or(|expected| {
                data.get("engine").and_then(Value::as_str) == Some(expected.as_str())
            });
        let policy_ok = self
            .options
            .expected_policy
            .as_ref()
            .is_none_or(|expected| {
                serde_json::to_value(expected)
                    .ok()
                    .is_some_and(|value| data.get("policy") == Some(&value))
            });
        if session_ok && engine_ok && policy_ok {
            return Ok(());
        }
        let _ = self.request_once(&Frame {
            cmd: "daemon.stop".into(),
            session: self.options.session.clone(),
            ..Frame::default()
        });
        Err(ClientError::Transport(DaemonError {
            code: codes::DAEMON_UNAVAILABLE.into(),
            message: "existing daemon configuration is incompatible; it was stopped".into(),
            hint: "retry to start a daemon with the requested session configuration".into(),
            retryable: Some(true),
            ..Default::default()
        }))
    }

    fn start_daemon(&self) -> Result<Child, ClientError> {
        let start = self.options.start.clone().unwrap_or_else(|| StartOptions {
            executable: current_executable(),
            log_path: default_log_path(),
            args: vec![
                "daemon".into(),
                "--session".into(),
                self.options.session.clone(),
            ],
        });
        if let Some(parent) = start
            .log_path
            .parent()
            .filter(|path| !path.as_os_str().is_empty())
        {
            fs::create_dir_all(parent)?;
        }
        let mut log_options = OpenOptions::new();
        log_options.create(true).append(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            log_options.mode(0o600);
        }
        let log = log_options.open(&start.log_path)?;
        let stderr = log.try_clone()?;
        let mut command = Command::new(&start.executable);
        command
            .args(&start.args)
            .stdin(Stdio::null())
            .stdout(Stdio::from(log))
            .stderr(Stdio::from(stderr));
        detach_command(&mut command);
        command.spawn().map_err(|error| {
            ClientError::Transport(DaemonError {
                code: codes::DAEMON_UNAVAILABLE.into(),
                message: format!("failed to start daemon: {}", redact_str(&error.to_string())),
                hint: "start daemon manually with `symbrowse daemon`".into(),
                ..Default::default()
            })
        })
    }

    #[cfg(unix)]
    fn request_once(&self, frame: &Frame) -> Result<Response, ClientError> {
        let mut stream = connect_unix(&self.options.socket_path, self.options.read_timeout)
            .map_err(|error| connect_error(&self.options, error))?;
        stream
            .set_read_timeout(Some(self.options.read_timeout))
            .map_err(|error| map_io_error(&self.options, error, "set daemon read deadline"))?;
        stream
            .set_write_timeout(Some(self.options.read_timeout))
            .map_err(|error| map_io_error(&self.options, error, "set daemon write deadline"))?;
        let mut payload = serde_json::to_vec(frame).map_err(io::Error::other)?;
        if payload.len() >= MAX_FRAME_BYTES {
            return Err(ClientError::Transport(DaemonError {
                code: codes::MALFORMED_REQUEST.into(),
                message: "daemon frame exceeds size limit".into(),
                ..Default::default()
            }));
        }
        payload.push(b'\n');
        stream
            .write_all(&payload)
            .map_err(|error| map_io_error(&self.options, error, "write daemon frame"))?;
        stream
            .flush()
            .map_err(|error| map_io_error(&self.options, error, "flush daemon frame"))?;
        let mut reader = BufReader::new(stream);
        read_response(&mut reader).map_err(|error| match error {
            ClientError::Io(error) => map_io_error(&self.options, error, "read daemon response"),
            other => other,
        })
    }
    #[cfg(windows)]
    fn request_once(&self, frame: &Frame) -> Result<Response, ClientError> {
        use interprocess::{
            ConnectWaitMode,
            os::windows::named_pipe::{pipe_mode, tokio::PipeStream},
        };
        use tokio::io::{AsyncReadExt, AsyncWriteExt};

        let path = self.options.socket_path.to_string_lossy();
        let mut payload = serde_json::to_vec(frame).map_err(io::Error::other)?;
        if payload.len() >= MAX_FRAME_BYTES {
            return Err(ClientError::Transport(DaemonError {
                code: codes::MALFORMED_REQUEST.into(),
                message: "daemon frame exceeds size limit".into(),
                ..Default::default()
            }));
        }
        payload.push(b'\n');
        let timeout = self.options.read_timeout;
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(ClientError::Io)?;
        runtime.block_on(async {
            let mut stream = tokio::time::timeout(
                timeout,
                PipeStream::<pipe_mode::Bytes, pipe_mode::Bytes>::connect_by_path_with_wait_mode(
                    path.as_ref(),
                    ConnectWaitMode::Timeout(timeout),
                ),
            )
            .await
            .map_err(|_| unavailable(&self.options, timed_out("connect to daemon")))?
            .map_err(|error| unavailable(&self.options, error))?;
            tokio::time::timeout(timeout, async {
                stream.write_all(&payload).await?;
                stream.flush().await?;
                let mut response = Vec::new();
                let mut chunk = [0_u8; 8192];
                loop {
                    let read = stream.read(&mut chunk).await?;
                    if read == 0 {
                        break;
                    }
                    if response.len().saturating_add(read) > MAX_FRAME_BYTES {
                        return Err(io::Error::new(
                            io::ErrorKind::InvalidData,
                            "daemon response exceeds size limit",
                        ));
                    }
                    response.extend_from_slice(&chunk[..read]);
                    if let Some(end) = response.iter().position(|byte| *byte == b'\n') {
                        response.truncate(end);
                        break;
                    }
                }
                if response.is_empty() {
                    return Err(io::Error::new(
                        io::ErrorKind::UnexpectedEof,
                        "daemon closed connection without a response",
                    ));
                }
                serde_json::from_slice(&response).map_err(|error| {
                    io::Error::new(
                        io::ErrorKind::InvalidData,
                        format!("decode daemon response: {error}"),
                    )
                })
            })
            .await
            .map_err(|_| map_io_error(&self.options, timed_out("daemon I/O"), "daemon I/O"))?
            .map_err(|error| map_io_error(&self.options, error, "daemon I/O"))
        })
    }
    #[cfg(not(any(unix, windows)))]
    fn request_once(&self, _frame: &Frame) -> Result<Response, ClientError> {
        Err(ClientError::Unsupported)
    }
}

#[cfg(unix)]
/// Connect to a Unix daemon endpoint without allowing the kernel connect call
/// to block past `timeout`. The returned stream is restored to blocking mode;
/// subsequent reads and writes use their own socket deadlines.
pub fn connect_unix(path: &Path, timeout: Duration) -> io::Result<UnixStream> {
    use nix::{
        errno::Errno,
        fcntl::{FcntlArg, OFlag, fcntl},
        sys::socket::{
            AddressFamily, SockFlag, SockType, UnixAddr, connect, getsockopt, socket,
            sockopt::SocketError,
        },
    };

    if timeout.is_zero() {
        return Err(io::Error::new(
            io::ErrorKind::TimedOut,
            "daemon connect timed out",
        ));
    }
    let socket = socket(
        AddressFamily::Unix,
        SockType::Stream,
        SockFlag::empty(),
        None,
    )
    .map_err(nix_io_error)?;
    let current_flags = fcntl(&socket, FcntlArg::F_GETFL).map_err(nix_io_error)?;
    fcntl(
        &socket,
        FcntlArg::F_SETFL(OFlag::from_bits_retain(current_flags) | OFlag::O_NONBLOCK),
    )
    .map_err(nix_io_error)?;
    let address = UnixAddr::new(path).map_err(nix_io_error)?;
    match connect(socket.as_raw_fd(), &address) {
        Ok(()) => {}
        Err(Errno::EINPROGRESS | Errno::EWOULDBLOCK) => {
            wait_for_connect_ready(socket.as_fd(), timeout)?;
            let socket_error = getsockopt(&socket, SocketError).map_err(nix_io_error)?;
            if socket_error != 0 {
                return Err(io::Error::from_raw_os_error(socket_error));
            }
        }
        Err(error) => return Err(nix_io_error(error)),
    }
    let stream = UnixStream::from(socket);
    stream.set_nonblocking(false)?;
    Ok(stream)
}

#[cfg(unix)]
fn wait_for_connect_ready(fd: std::os::fd::BorrowedFd<'_>, timeout: Duration) -> io::Result<()> {
    use nix::poll::{PollFd, PollFlags, poll};

    let deadline = Instant::now() + timeout;
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "daemon connect timed out",
            ));
        }
        let timeout_ms = remaining.as_millis().clamp(1, u16::MAX.into()) as u16;
        let mut descriptors = [PollFd::new(
            fd,
            PollFlags::POLLOUT | PollFlags::POLLERR | PollFlags::POLLHUP,
        )];
        if poll(&mut descriptors, timeout_ms).map_err(nix_io_error)? == 0 {
            continue;
        }
        return Ok(());
    }
}

#[cfg(unix)]
fn nix_io_error(error: nix::errno::Errno) -> io::Error {
    io::Error::from_raw_os_error(error as i32)
}

#[cfg(unix)]
fn connect_error(options: &ClientOptions, error: io::Error) -> ClientError {
    if error.kind() == io::ErrorKind::TimedOut {
        map_io_error(options, error, "connect to daemon")
    } else {
        unavailable(options, error)
    }
}

fn should_autostart(error: &ClientError) -> bool {
    matches!(
        error,
        ClientError::Transport(DaemonError {
            code,
            ..
        }) if code == codes::DAEMON_UNAVAILABLE
    )
}

fn map_io_error(options: &ClientOptions, error: io::Error, operation: &str) -> ClientError {
    if matches!(
        error.kind(),
        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock
    ) {
        return ClientError::Transport(DaemonError {
            code: codes::OPERATION_TIMEOUT.into(),
            message: format!("{operation} timed out after {:?}", options.read_timeout),
            details: Some(redact_json(&serde_json::json!({
                "session": options.session,
                "socket_path": options.socket_path,
                "timeout_seconds": options.read_timeout.as_secs_f64(),
            }))),
            ..Default::default()
        });
    }
    ClientError::Io(error)
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
        let _ = child.kill();
    }
    #[cfg(windows)]
    {
        let _ = child.kill();
    }
    let deadline = Instant::now() + CHILD_REAP_TIMEOUT;
    while Instant::now() < deadline {
        match child.try_wait() {
            Ok(Some(_)) | Err(_) => return,
            Ok(None) => thread::sleep(Duration::from_millis(10)),
        }
    }
    // Do not fall back to Child::wait: a platform process can remain
    // unkillable, and cleanup must never turn a bounded request into an
    // unbounded join. The final try_wait preserves best-effort reaping.
    let _ = child.try_wait();
}

fn current_executable() -> PathBuf {
    std::env::current_exe().unwrap_or_else(|_| PathBuf::from("symbrowse"))
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

#[cfg(unix)]
fn read_response<R: BufRead>(reader_source: &mut R) -> Result<Response, ClientError> {
    let mut reader = reader_source;
    let mut line = Vec::new();
    read_limited_line(&mut reader, &mut line, MAX_FRAME_BYTES)?;
    if line.is_empty() {
        return Err(ClientError::Transport(DaemonError {
            code: codes::DAEMON_UNAVAILABLE.into(),
            message: "daemon closed connection without a response".into(),
            ..Default::default()
        }));
    }
    serde_json::from_slice(line.trim_ascii_end()).map_err(|error| {
        ClientError::Transport(DaemonError {
            code: codes::OPERATION_FAILED.into(),
            message: format!("decode daemon response: {error}"),
            ..Default::default()
        })
    })
}

#[cfg(unix)]
fn read_limited_line<R: BufRead>(
    reader: &mut R,
    output: &mut Vec<u8>,
    limit: usize,
) -> io::Result<()> {
    output.clear();
    loop {
        let available = reader.fill_buf()?;
        if available.is_empty() {
            return Ok(());
        }
        let take = available
            .iter()
            .position(|b| *b == b'\n')
            .map_or(available.len(), |n| n + 1);
        if output.len().saturating_add(take) > limit {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "daemon response exceeds size limit",
            ));
        }
        output.extend_from_slice(&available[..take]);
        reader.consume(take);
        if output.last() == Some(&b'\n') {
            return Ok(());
        }
    }
}

#[cfg(windows)]
fn timed_out(operation: &str) -> io::Error {
    io::Error::new(io::ErrorKind::TimedOut, operation)
}

fn unavailable(options: &ClientOptions, error: io::Error) -> ClientError {
    ClientError::Transport(DaemonError {
        code: codes::DAEMON_UNAVAILABLE.into(),
        message: format!("daemon is unavailable for session {:?}", options.session),
        hint: format!(
            "start daemon with `symbrowse daemon --session {}`",
            options.session
        ),
        details: Some(redact_json(
            &serde_json::json!({"session": options.session, "socket_path": options.socket_path.display().to_string(), "transport_error": error.to_string()}),
        )),
        ..Default::default()
    })
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU64, Ordering};

    const UNIX_SOCKET_PATH_LIMIT: usize = if cfg!(target_os = "macos") { 104 } else { 108 };
    static TEST_SOCKET_COUNTER: AtomicU64 = AtomicU64::new(0);

    fn test_socket_path() -> PathBuf {
        let counter = TEST_SOCKET_COUNTER.fetch_add(1, Ordering::Relaxed);
        let path = PathBuf::from(format!("/tmp/sb-{}-{counter}.sock", std::process::id()));
        let length = path.as_os_str().as_encoded_bytes().len();
        assert!(
            length < UNIX_SOCKET_PATH_LIMIT,
            "test socket path is {length} bytes, must be shorter than {UNIX_SOCKET_PATH_LIMIT}"
        );
        path
    }

    #[test]
    fn unavailable_errors_omit_secret_like_transport_text() {
        let error = unavailable(
            &ClientOptions::default(),
            io::Error::other("password=hidden"),
        );
        assert!(!error.to_string().contains("hidden"));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn unix_connect_deadline_handles_saturated_backlog() {
        use std::os::unix::net::UnixListener;

        let path = test_socket_path();
        let _ = std::fs::remove_file(&path);
        let listener = UnixListener::bind(&path).expect("bind test listener");
        nix::sys::socket::listen(&listener, nix::sys::socket::Backlog::new(0).unwrap())
            .expect("set a zero-length Unix listen backlog");
        // A full Unix-domain listen queue is allowed to accept a second local
        // connection immediately on some kernels and to report EINPROGRESS on
        // others. Both outcomes preserve the client contract; only an
        // unexpected error is a regression.
        let pending = connect_unix(&path, Duration::from_secs(1)).expect("fill listen backlog");
        let timeout = Duration::from_millis(50);
        let started = Instant::now();
        match connect_unix(&path, timeout) {
            Ok(stream) => drop(stream),
            Err(error) => assert_eq!(error.kind(), io::ErrorKind::TimedOut),
        }
        assert!(
            started.elapsed() < Duration::from_secs(1),
            "saturated-backlog connection blocked for {:?}",
            started.elapsed()
        );
        drop(pending);
        drop(listener);
        let _ = std::fs::remove_file(path);
    }

    #[test]
    fn unix_connect_succeeds_for_listening_socket() {
        use std::os::unix::net::UnixListener;

        let path = test_socket_path();
        let _ = std::fs::remove_file(&path);
        let listener = UnixListener::bind(&path).expect("bind test listener");
        let stream = connect_unix(&path, Duration::from_secs(1)).expect("connect to listener");
        drop(stream);
        drop(listener);
        let _ = std::fs::remove_file(path);
    }

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
