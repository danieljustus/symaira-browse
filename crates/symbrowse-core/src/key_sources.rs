#![deny(unsafe_code)]

//! Runtime-detected SymVault, Keychain, and environment key sources.

use std::{
    fs,
    io::{Read, Write},
    path::{Path, PathBuf},
    process::{Command, ExitStatus, Stdio},
    time::Duration,
};

use fs2::FileExt;
use wait_timeout::ChildExt;
use zeroize::Zeroizing;

use crate::key_resolver::{
    KeyProvisioner, KeySources, MissingReason, ProbeError, ProvisionOutcome,
};

const DEFAULT_TIMEOUT: Duration = Duration::from_secs(15);
const MAX_OUTPUT_BYTES: usize = 1 << 20;

#[derive(Clone)]
pub struct SystemKeySources {
    symvault: PathBuf,
    #[cfg(target_os = "macos")]
    security: PathBuf,
    timeout: Duration,
    lock_path: PathBuf,
}

impl Default for SystemKeySources {
    fn default() -> Self {
        Self {
            symvault: PathBuf::from("symvault"),
            #[cfg(target_os = "macos")]
            security: PathBuf::from("security"),
            timeout: DEFAULT_TIMEOUT,
            lock_path: default_lock_path(),
        }
    }
}

impl SystemKeySources {
    #[must_use]
    pub fn with_programs(
        symvault: impl Into<PathBuf>,
        security: impl Into<PathBuf>,
        timeout: Duration,
    ) -> Self {
        #[cfg(target_os = "macos")]
        let security = security.into();
        #[cfg(not(target_os = "macos"))]
        let _ = security.into();
        let symvault = symvault.into();
        let lock_path = symvault.with_extension("key-init.lock");
        Self {
            symvault,
            #[cfg(target_os = "macos")]
            security,
            timeout,
            lock_path,
        }
    }

    fn acquire_init_lock(&self) -> Result<fs::File, ProbeError> {
        let lock = open_init_lock(&self.lock_path)
            .map_err(|error| ProbeError::Failed(format!("open key-init lock: {error}")))?;
        lock.lock_exclusive()
            .map_err(|error| ProbeError::Failed(format!("lock key initialization: {error}")))?;
        Ok(lock)
    }

    pub fn set_vault(&self, entry: &str, key: &[u8; 32]) -> Result<(), ProbeError> {
        let mut input = Zeroizing::new(hex_key(key).into_bytes());
        input.push(b'\n');
        let output = run_command(
            &self.symvault,
            &["set", entry, "--stdin-value"],
            Some(&input),
            self.timeout,
        )?;
        require_success(output.status, "symvault set")
    }

    #[cfg(target_os = "macos")]
    pub fn set_keychain(
        &self,
        service: &str,
        account: &str,
        key: &[u8; 32],
    ) -> Result<(), ProbeError> {
        let mut input = Zeroizing::new(hex_key(key).into_bytes());
        input.push(b'\n');
        let output = run_command(
            &self.security,
            &["add-generic-password", "-s", service, "-a", account, "-w"],
            Some(&input),
            self.timeout,
        )?;
        require_success(output.status, "keychain set")
    }

    #[cfg(not(target_os = "macos"))]
    pub fn set_keychain(
        &self,
        _service: &str,
        _account: &str,
        _key: &[u8; 32],
    ) -> Result<(), ProbeError> {
        Err(ProbeError::Missing(MissingReason::Unavailable))
    }
}

impl KeySources for SystemKeySources {
    fn vault(&self, entry: &str) -> Result<Option<Vec<u8>>, ProbeError> {
        let output = match run_command(&self.symvault, &["get", entry], None, self.timeout) {
            Ok(output) => output,
            Err(ProbeError::Missing(MissingReason::Unavailable)) => return Ok(None),
            Err(error) => return Err(error),
        };
        match output.status.code() {
            Some(0) => {
                Ok((!output.stdout.iter().all(u8::is_ascii_whitespace)).then_some(output.stdout))
            }
            Some(2) => Err(ProbeError::Missing(MissingReason::NotFound)),
            Some(3) => Err(ProbeError::Missing(MissingReason::NotInitialized)),
            Some(code) => Err(ProbeError::Failed(format!("exit status {code}"))),
            None => Err(ProbeError::Failed("terminated by signal".to_owned())),
        }
    }

    #[cfg(target_os = "macos")]
    fn keychain(&self, service: &str, account: &str) -> Result<Option<Vec<u8>>, ProbeError> {
        let output = match run_command(
            &self.security,
            &["find-generic-password", "-s", service, "-a", account, "-w"],
            None,
            self.timeout,
        ) {
            Ok(output) => output,
            Err(ProbeError::Missing(MissingReason::Unavailable)) => return Ok(None),
            Err(error) => return Err(error),
        };
        match output.status.code() {
            Some(0) => {
                let value = trim_ascii_space(&output.stdout);
                Ok((!value.is_empty()).then(|| value.to_vec()))
            }
            Some(44) => Ok(None),
            Some(code) => Err(ProbeError::Failed(format!(
                "keychain lookup: exit status {code}"
            ))),
            None => Err(ProbeError::Failed(
                "keychain lookup: terminated by signal".to_owned(),
            )),
        }
    }

    #[cfg(not(target_os = "macos"))]
    fn keychain(&self, _service: &str, _account: &str) -> Result<Option<Vec<u8>>, ProbeError> {
        Ok(None)
    }

    fn environment(&self, name: &str) -> Option<String> {
        std::env::var(name).ok()
    }
}

impl KeyProvisioner for SystemKeySources {
    fn provision_vault(&self, entry: &str, key: &[u8; 32]) -> Result<ProvisionOutcome, ProbeError> {
        let _lock = self.acquire_init_lock()?;
        match self.vault(entry) {
            Ok(Some(_)) => return Ok(ProvisionOutcome::Existing),
            Ok(None) | Err(ProbeError::Missing(_)) => {}
            Err(error) => return Err(error),
        }
        self.set_vault(entry, key)?;
        Ok(ProvisionOutcome::Created)
    }

    fn provision_keychain(
        &self,
        service: &str,
        account: &str,
        key: &[u8; 32],
    ) -> Result<ProvisionOutcome, ProbeError> {
        let _lock = self.acquire_init_lock()?;
        match self.keychain(service, account) {
            Ok(Some(_)) => return Ok(ProvisionOutcome::Existing),
            Ok(None) | Err(ProbeError::Missing(_)) => {}
            Err(error) => return Err(error),
        }
        self.set_keychain(service, account, key)?;
        Ok(ProvisionOutcome::Created)
    }
}

struct CommandOutput {
    status: ExitStatus,
    stdout: Vec<u8>,
}

fn run_command(
    program: &Path,
    args: &[&str],
    input: Option<&[u8]>,
    timeout: Duration,
) -> Result<CommandOutput, ProbeError> {
    let mut command = Command::new(program);
    let output_file = tempfile::NamedTempFile::new()
        .map_err(|error| ProbeError::Failed(format!("create command output file: {error}")))?;
    let output_handle = output_file
        .reopen()
        .map_err(|error| ProbeError::Failed(format!("open command output file: {error}")))?;
    command
        .args(args)
        .stdin(if input.is_some() {
            Stdio::piped()
        } else {
            Stdio::null()
        })
        .stdout(output_handle)
        .stderr(Stdio::null());
    configure_process_tree(&mut command);
    let mut child = command.spawn().map_err(|error| {
        if error.kind() == std::io::ErrorKind::NotFound {
            ProbeError::Missing(MissingReason::Unavailable)
        } else {
            ProbeError::Failed(error.to_string())
        }
    })?;

    if let Some(input) = input
        && let Some(mut stdin) = child.stdin.take()
        && let Err(error) = stdin.write_all(input)
    {
        terminate_process_tree(&mut child);
        return Err(ProbeError::Failed(error.to_string()));
    }

    let status = match child
        .wait_timeout(timeout)
        .map_err(|error| ProbeError::Failed(error.to_string()))?
    {
        Some(status) => status,
        None => {
            terminate_process_tree(&mut child);
            return Err(ProbeError::Failed(format!(
                "command timed out after {}ms",
                timeout.as_millis()
            )));
        }
    };
    let stdout = read_bounded(
        output_file
            .reopen()
            .map_err(|error| ProbeError::Failed(format!("open command output file: {error}")))?,
    )?;
    Ok(CommandOutput { status, stdout })
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

fn read_bounded(mut reader: impl Read) -> Result<Vec<u8>, ProbeError> {
    let mut output = Vec::new();
    let mut buffer = [0_u8; 8192];
    let mut oversized = false;
    loop {
        let read = reader
            .read(&mut buffer)
            .map_err(|error| ProbeError::Failed(error.to_string()))?;
        if read == 0 {
            break;
        }
        if output.len() < MAX_OUTPUT_BYTES {
            let keep = read.min(MAX_OUTPUT_BYTES - output.len());
            output.extend_from_slice(&buffer[..keep]);
            oversized |= keep != read;
        } else {
            oversized = true;
        }
    }
    if oversized {
        return Err(ProbeError::Failed(format!(
            "command output exceeds {MAX_OUTPUT_BYTES} bytes"
        )));
    }
    Ok(output)
}

fn require_success(status: ExitStatus, action: &str) -> Result<(), ProbeError> {
    match status.code() {
        Some(0) => Ok(()),
        Some(code) => Err(ProbeError::Failed(format!("{action}: exit status {code}"))),
        None => Err(ProbeError::Failed(format!(
            "{action}: terminated by signal"
        ))),
    }
}

#[cfg(target_os = "macos")]
fn trim_ascii_space(value: &[u8]) -> &[u8] {
    value.trim_ascii()
}

fn default_lock_path() -> PathBuf {
    let base = std::env::var_os("XDG_RUNTIME_DIR")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".local/state")))
        .unwrap_or_else(std::env::temp_dir);
    base.join("symbrowse/key-init.lock")
}

fn prepare_lock_parent(path: &Path) -> std::io::Result<()> {
    let parent = path
        .parent()
        .ok_or_else(|| std::io::Error::other("key-init lock has no parent"))?;
    if let Ok(metadata) = fs::symlink_metadata(parent)
        && (!metadata.is_dir() || metadata.file_type().is_symlink())
    {
        return Err(std::io::Error::other(
            "key-init lock parent is not a regular directory",
        ));
    }
    fs::create_dir_all(parent)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(parent, fs::Permissions::from_mode(0o700))?;
    }
    Ok(())
}

#[cfg(unix)]
fn open_init_lock(path: &Path) -> std::io::Result<fs::File> {
    use rustix::fs::{Mode, OFlags, open};

    prepare_lock_parent(path)?;
    let descriptor = open(
        path,
        OFlags::CREATE | OFlags::RDWR | OFlags::CLOEXEC | OFlags::NOFOLLOW,
        Mode::RUSR | Mode::WUSR,
    )?;
    Ok(fs::File::from(descriptor))
}

#[cfg(not(unix))]
fn open_init_lock(path: &Path) -> std::io::Result<fs::File> {
    prepare_lock_parent(path)?;
    if let Ok(metadata) = fs::symlink_metadata(path)
        && metadata.file_type().is_symlink()
    {
        return Err(std::io::Error::other("key-init lock is a symlink"));
    }
    fs::OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(path)
}

fn hex_key(key: &[u8; 32]) -> String {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut encoded = String::with_capacity(64);
    for byte in key {
        encoded.push(HEX[(byte >> 4) as usize] as char);
        encoded.push(HEX[(byte & 0x0f) as usize] as char);
    }
    encoded
}
