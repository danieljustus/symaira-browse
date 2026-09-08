#![deny(unsafe_code)]

use std::{
    env,
    ffi::OsString,
    fmt, fs,
    io::{Read, Write},
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, Instant},
};

use serde::{Deserialize, Serialize};
use wait_timeout::ChildExt;

use crate::policy::{Decision, RiskClass};

pub const GUARD_ENV_NAME: &str = "SYMBROWSE_SYMGUARD";
pub const GUARD_BINARY_NAME: &str = "symbrain";
pub const GUARD_TIMEOUT: Duration = Duration::from_secs(5);
const MAX_OUTPUT_BYTES: usize = 1 << 20;

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Guard {
    pub executable: PathBuf,
    pub subcommand: Vec<String>,
    pub timeout: Duration,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GuardInput {
    pub command: String,
    pub class: RiskClass,
    pub domain: String,
    pub warnings: Vec<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GuardOutcome {
    pub decision: Decision,
    pub reason: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GuardError(String);

impl fmt::Display for GuardError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl std::error::Error for GuardError {}

impl Guard {
    #[must_use]
    pub fn detect() -> Option<Self> {
        let override_value = env::var(GUARD_ENV_NAME).unwrap_or_default();
        let trimmed = override_value.trim();
        if matches!(
            trimmed.to_ascii_lowercase().as_str(),
            "0" | "none" | "off" | "false" | "disable"
        ) {
            return None;
        }
        if !trimmed.is_empty() {
            return Some(Self {
                executable: PathBuf::from(trimmed),
                subcommand: Vec::new(),
                timeout: GUARD_TIMEOUT,
            });
        }
        find_program(GUARD_BINARY_NAME).map(|executable| Self {
            executable,
            subcommand: vec!["guard".to_owned()],
            timeout: GUARD_TIMEOUT,
        })
    }

    #[must_use]
    pub fn active(&self) -> bool {
        !self.executable.as_os_str().is_empty()
    }

    #[must_use]
    pub fn command(&self) -> String {
        let mut parts = vec![self.executable.to_string_lossy().into_owned()];
        parts.extend(self.subcommand.clone());
        parts.push("decide".to_owned());
        parts.join(" ").trim().to_owned()
    }

    pub fn decide(&self, input: &GuardInput) -> Result<GuardOutcome, GuardError> {
        if !self.active() {
            return Err(GuardError("guard is not configured".to_owned()));
        }
        let payload = serde_json::to_vec(&GuardRequest {
            command: &input.command,
            risk_class: guard_risk_level(input.class)?,
            domain: &input.domain,
            warnings: &input.warnings,
        })
        .map_err(|error| GuardError(format!("guard: marshal request: {error}")))?;

        let mut command = Command::new(&self.executable);
        command
            .args(&self.subcommand)
            .arg("decide")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        configure_process_tree(&mut command);
        let mut child = command
            .spawn()
            .map_err(|error| GuardError(format!("guard decide failed: {error}")))?;
        let stdout = child.stdout.take().map(read_bounded);
        let stderr = child.stderr.take().map(read_bounded);
        if let Some(mut stdin) = child.stdin.take()
            && let Err(error) = stdin.write_all(&payload)
        {
            terminate_process_tree(&mut child);
            return Err(GuardError(format!("guard decide failed: {error}")));
        }
        let timeout = if self.timeout.is_zero() {
            GUARD_TIMEOUT
        } else {
            self.timeout
        };
        let deadline = Instant::now() + timeout;
        let status = match child.wait_timeout(timeout) {
            Ok(status) => status,
            Err(error) => {
                terminate_process_tree(&mut child);
                return Err(GuardError(format!("guard decide failed: {error}")));
            }
        };
        let Some(status) = status else {
            terminate_process_tree(&mut child);
            return Err(GuardError("guard decide failed: timed out".to_owned()));
        };
        let stdout = match receive_output(stdout, deadline) {
            Ok(output) => output,
            Err(()) => {
                terminate_process_tree(&mut child);
                return Err(GuardError("guard decide failed: timed out".to_owned()));
            }
        };
        let stderr = match receive_output(stderr, deadline) {
            Ok(output) => output,
            Err(()) => {
                terminate_process_tree(&mut child);
                return Err(GuardError("guard decide failed: timed out".to_owned()));
            }
        };
        if !status.success() {
            let message = String::from_utf8_lossy(&stderr).trim().to_owned();
            return Err(GuardError(format!(
                "guard decide failed: {}",
                if message.is_empty() {
                    status.to_string()
                } else {
                    message
                }
            )));
        }
        let response: GuardResponse = serde_json::from_slice(&stdout).map_err(|error| {
            GuardError(format!("guard returned an unparseable verdict: {error}"))
        })?;
        let decision = match response.decision.as_str() {
            "allow" => Decision::Allow,
            "confirm" => Decision::Confirm,
            "deny" => Decision::Deny,
            other => {
                return Err(GuardError(format!(
                    "guard returned invalid decision {other:?}"
                )));
            }
        };
        Ok(GuardOutcome {
            decision,
            reason: response.reason,
        })
    }
}

#[derive(Serialize)]
struct GuardRequest<'a> {
    command: &'a str,
    risk_class: &'static str,
    domain: &'a str,
    #[serde(skip_serializing_if = "warnings_empty")]
    warnings: &'a [String],
}

fn warnings_empty(value: &&[String]) -> bool {
    value.is_empty()
}

#[derive(Deserialize)]
struct GuardResponse {
    decision: String,
    #[serde(default)]
    reason: String,
}

fn guard_risk_level(class: RiskClass) -> Result<&'static str, GuardError> {
    match class {
        RiskClass::Read => Ok("low"),
        RiskClass::Navigate | RiskClass::Interact | RiskClass::Download => Ok("medium"),
        RiskClass::Submit | RiskClass::Upload | RiskClass::NetworkMock => Ok("high"),
        RiskClass::Eval | RiskClass::Credential => Ok("critical"),
    }
}

fn read_bounded<R: Read + Send + 'static>(mut reader: R) -> mpsc::Receiver<Vec<u8>> {
    let (sender, receiver) = mpsc::sync_channel(1);
    thread::spawn(move || {
        let mut kept = Vec::new();
        let mut buffer = [0_u8; 8192];
        loop {
            match reader.read(&mut buffer) {
                Ok(0) | Err(_) => break,
                Ok(read) => {
                    let remaining = MAX_OUTPUT_BYTES.saturating_sub(kept.len());
                    kept.extend_from_slice(&buffer[..read.min(remaining)]);
                }
            }
        }
        let _ = sender.send(kept);
    });
    receiver
}

fn receive_output(
    receiver: Option<mpsc::Receiver<Vec<u8>>>,
    deadline: Instant,
) -> Result<Vec<u8>, ()> {
    let Some(receiver) = receiver else {
        return Ok(Vec::new());
    };
    let remaining = deadline.saturating_duration_since(Instant::now());
    if remaining.is_zero() {
        return Err(());
    }
    receiver.recv_timeout(remaining).map_err(|_| ())
}

fn find_program(name: &str) -> Option<PathBuf> {
    let path = env::var_os("PATH")?;
    for directory in env::split_paths(&path) {
        for candidate in program_candidates(&directory, name) {
            if fs::metadata(&candidate).is_ok_and(|metadata| metadata.is_file()) {
                return Some(candidate);
            }
        }
    }
    None
}

fn program_candidates(directory: &Path, name: &str) -> Vec<PathBuf> {
    let base = directory.join(name);
    #[cfg(windows)]
    {
        let extensions =
            env::var_os("PATHEXT").unwrap_or_else(|| OsString::from(".COM;.EXE;.BAT;.CMD"));
        let mut candidates = vec![base.clone()];
        candidates.extend(
            extensions
                .to_string_lossy()
                .split(';')
                .filter(|value| !value.is_empty())
                .map(|extension| directory.join(format!("{name}{extension}"))),
        );
        candidates
    }
    #[cfg(not(windows))]
    {
        let _ = OsString::new();
        vec![base]
    }
}

#[cfg(unix)]
fn configure_process_tree(command: &mut Command) {
    use std::os::unix::process::CommandExt;
    command.process_group(0);
}

#[cfg(windows)]
fn configure_process_tree(command: &mut Command) {
    use std::os::windows::process::CommandExt;
    const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
    command.creation_flags(CREATE_NEW_PROCESS_GROUP);
}

#[cfg(not(any(unix, windows)))]
fn configure_process_tree(_command: &mut Command) {}

#[cfg(unix)]
fn terminate_process_tree(child: &mut std::process::Child) {
    use rustix::process::{Pid, Signal, kill_process_group};
    if let Some(pid) = Pid::from_raw(child.id() as i32) {
        let _ = kill_process_group(pid, Signal::KILL);
    }
    let _ = child.kill();
    let _ = child.wait();
}

#[cfg(windows)]
fn terminate_process_tree(child: &mut std::process::Child) {
    let pid = child.id().to_string();
    let _ = Command::new("taskkill.exe")
        .args(["/PID", &pid, "/T", "/F"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status();
    let _ = child.kill();
    let _ = child.wait();
}

#[cfg(not(any(unix, windows)))]
fn terminate_process_tree(child: &mut std::process::Child) {
    let _ = child.kill();
    let _ = child.wait();
}
