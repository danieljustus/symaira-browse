use std::{
    fmt,
    io::{Read, Write},
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, Instant},
};

use symbrowse_core::policy::{Allowlist, SsrfGuard};
use symbrowse_engine::capabilities::{Capabilities, capabilities_for};
use symbrowse_engine::{Context, NavigationResult, Page};
use url::Url;

pub const ENGINE_KIND: &str = "safari-attach";
pub const DEFAULT_TAB_NAME: &str = "Symaira";
pub const DEFAULT_COMMAND_TIMEOUT: Duration = Duration::from_secs(10);
pub const DEFAULT_NAVIGATION_TIMEOUT: Duration = Duration::from_secs(30);
const MAX_SCRIPT_BYTES: usize = 1024 * 1024;

/// A prerequisite that must be diagnosed before Safari automation is used.
#[derive(Debug, Clone, Eq, PartialEq)]
pub enum SafariPrerequisite {
    ApplicationUnavailable,
    AutomationPermissionDenied,
    WindowUnavailable,
    TabUnavailable,
    DriverUnavailable,
    RemoteAutomationDisabled,
    LoopbackSessionUnavailable,
}

impl fmt::Display for SafariPrerequisite {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Self::ApplicationUnavailable => "Safari application unavailable",
            Self::AutomationPermissionDenied => "Safari Automation permission denied",
            Self::WindowUnavailable => "Safari selected window unavailable",
            Self::TabUnavailable => "Safari selected tab unavailable",
            Self::DriverUnavailable => "SafariDriver unavailable",
            Self::RemoteAutomationDisabled => "Safari Remote Automation disabled",
            Self::LoopbackSessionUnavailable => "Safari loopback session unavailable",
        })
    }
}

/// Errors returned by the live-session Apple Events adapter.
#[derive(Debug, Clone, Eq, PartialEq)]
pub enum AttachError {
    Closed,
    Prerequisite {
        check: SafariPrerequisite,
        message: String,
    },
    Unsupported {
        operation: String,
    },
    InvalidTarget {
        target: String,
        reason: String,
    },
    Runner {
        message: String,
    },
    TimedOut {
        operation: String,
        timeout: Duration,
    },
    NavigationDidNotSettle {
        target: String,
    },
}

impl fmt::Display for AttachError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Closed => f.write_str("safari attach engine: engine is closed"),
            Self::Prerequisite { check, message } => {
                write!(f, "safari attach prerequisite {check}: {message}")
            }
            Self::Unsupported { operation } => {
                write!(
                    f,
                    "safari attach engine: unsupported operation: {operation}"
                )
            }
            Self::InvalidTarget { target, reason } => {
                write!(
                    f,
                    "safari attach engine: invalid target {target:?}: {reason}"
                )
            }
            Self::Runner { message } => write!(f, "safari engine: osascript failed: {message}"),
            Self::TimedOut { operation, timeout } => {
                write!(f, "safari engine: {operation} timed out after {timeout:?}")
            }
            Self::NavigationDidNotSettle { target } => {
                write!(f, "safari engine: navigation to {target:?} did not settle")
            }
        }
    }
}
impl std::error::Error for AttachError {}

/// The complete side-effect seam for AppleScript execution.
pub trait ScriptRunner: Send + Sync {
    fn run(&self, script: &str, timeout: Duration) -> Result<String, AttachError>;
}

/// Bounded production `osascript` runner that supplies the script on stdin.
#[derive(Clone, Debug)]
pub struct OsascriptRunner {
    program: PathBuf,
    max_output_bytes: usize,
}

impl Default for OsascriptRunner {
    fn default() -> Self {
        Self {
            program: PathBuf::from("osascript"),
            max_output_bytes: 1024 * 1024,
        }
    }
}

impl OsascriptRunner {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Overrides the executable for an integration test or a controlled host.
    #[must_use]
    pub fn with_program(mut self, program: impl Into<PathBuf>) -> Self {
        self.program = program.into();
        self
    }

    /// Bounds stdout retained from the child process.
    #[must_use]
    pub fn with_max_output_bytes(mut self, bytes: usize) -> Self {
        self.max_output_bytes = bytes;
        self
    }
}

impl ScriptRunner for OsascriptRunner {
    fn run(&self, script: &str, timeout: Duration) -> Result<String, AttachError> {
        if script.len() > MAX_SCRIPT_BYTES {
            return Err(AttachError::Runner {
                message: format!("script exceeded {MAX_SCRIPT_BYTES} bytes"),
            });
        }
        let mut command = Command::new(&self.program);
        command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null());
        #[cfg(unix)]
        {
            use std::os::unix::process::CommandExt;
            // The child becomes the process-group leader. This is a safe
            // std::process API and lets timeout cleanup include descendants.
            command.process_group(0);
        }
        let mut child = command.spawn().map_err(|error| AttachError::Runner {
            message: error.to_string(),
        })?;

        let Some(stdout) = child.stdout.take() else {
            terminate_process_group(&mut child);
            return Err(AttachError::Runner {
                message: "osascript stdout pipe was unavailable".to_owned(),
            });
        };
        let max_output_bytes = self.max_output_bytes;
        let (sender, receiver) = mpsc::sync_channel(1);
        thread::spawn(move || {
            let mut output = Vec::new();
            let result = stdout
                .take(max_output_bytes.saturating_add(1) as u64)
                .read_to_end(&mut output);
            let _ = sender.send((output, result));
        });

        let Some(mut stdin) = child.stdin.take() else {
            terminate_process_group(&mut child);
            return Err(AttachError::Runner {
                message: "osascript stdin pipe was unavailable".to_owned(),
            });
        };
        let script = script.as_bytes().to_vec();
        let (write_sender, write_receiver) = mpsc::sync_channel(1);
        thread::spawn(move || {
            let _ = write_sender.send(stdin.write_all(&script));
        });

        use wait_timeout::ChildExt;
        let deadline = Instant::now() + timeout;
        let status = match child.wait_timeout(timeout) {
            Ok(Some(status)) => status,
            Ok(None) => {
                terminate_process_group(&mut child);
                return Err(AttachError::TimedOut {
                    operation: "osascript".to_owned(),
                    timeout,
                });
            }
            Err(error) => {
                terminate_process_group(&mut child);
                return Err(AttachError::Runner {
                    message: error.to_string(),
                });
            }
        };

        match write_receiver.recv_timeout(deadline.saturating_duration_since(Instant::now())) {
            Ok(Ok(())) => {}
            Ok(Err(error)) => {
                terminate_process_group(&mut child);
                return Err(AttachError::Runner {
                    message: error.to_string(),
                });
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {
                terminate_process_group(&mut child);
                return Err(AttachError::TimedOut {
                    operation: "write osascript input".to_owned(),
                    timeout,
                });
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                terminate_process_group(&mut child);
                return Err(AttachError::Runner {
                    message: "stdin writer thread disconnected".to_owned(),
                });
            }
        }

        let (output, read_result) =
            match receiver.recv_timeout(deadline.saturating_duration_since(Instant::now())) {
                Ok(result) => result,
                Err(mpsc::RecvTimeoutError::Timeout) => {
                    // The direct child may have exited while a descendant still
                    // owns stdout. Never join that reader before killing the group.
                    terminate_process_group(&mut child);
                    return Err(AttachError::TimedOut {
                        operation: "read osascript output".to_owned(),
                        timeout,
                    });
                }
                Err(mpsc::RecvTimeoutError::Disconnected) => {
                    terminate_process_group(&mut child);
                    return Err(AttachError::Runner {
                        message: "stdout reader thread disconnected".to_owned(),
                    });
                }
            };
        if let Err(error) = read_result {
            terminate_process_group(&mut child);
            return Err(AttachError::Runner {
                message: error.to_string(),
            });
        }
        if output.len() > self.max_output_bytes {
            terminate_process_group(&mut child);
            return Err(AttachError::Runner {
                message: format!("stdout exceeded {} bytes", self.max_output_bytes),
            });
        }
        if !status.success() {
            terminate_process_group(&mut child);
            return Err(AttachError::Runner {
                message: format!("process exited with {status}"),
            });
        }
        Ok(String::from_utf8_lossy(&output).into_owned())
    }
}

fn terminate_process_group(child: &mut Child) {
    #[cfg(unix)]
    {
        use rustix::process::{Pid, Signal, kill_process_group};
        if let Some(pid) = Pid::from_raw(child.id() as i32) {
            let _ = kill_process_group(pid, Signal::KILL);
        }
    }
    #[cfg(not(unix))]
    {
        let _ = child.kill();
    }
    let _ = child.wait();
}

/// URL policy applied before any AppleScript reaches the live Safari tab.
///
/// Both adapters consume the same core policy so an inactive policy permits
/// normal web URLs, while an active allowlist or SSRF guard fails closed.
#[derive(Clone, Debug, Default)]
pub struct NavigationPolicy {
    allowlist: Option<Allowlist>,
    ssrf_guard: Option<SsrfGuard>,
    configuration_error: Option<String>,
}

/// Preserve the denying policy independently of its diagnostic text.
pub(crate) enum PolicyRejection {
    Configuration(String),
    Allowlist,
    Ssrf(String),
}

impl PolicyRejection {
    pub(crate) fn blocked_reason(&self) -> Option<&'static str> {
        match self {
            Self::Configuration(_) => None,
            Self::Allowlist => Some("domain allowlist"),
            Self::Ssrf(_) => Some("ssrf guard"),
        }
    }
}

impl std::fmt::Display for PolicyRejection {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Configuration(error) => write!(f, "URL policy is invalid: {error}"),
            Self::Allowlist => f.write_str("blocked by the domain allowlist"),
            Self::Ssrf(error) => write!(f, "blocked by the SSRF guard: {error}"),
        }
    }
}

impl NavigationPolicy {
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Parses the canonical domain allowlist. A malformed configuration is
    /// retained as an error so navigation cannot accidentally proceed.
    #[must_use]
    pub fn from_allowlist(patterns: &[String]) -> Self {
        match Allowlist::parse(patterns) {
            Ok(allowlist) => Self {
                allowlist: Some(allowlist),
                ..Self::default()
            },
            Err(error) => Self {
                configuration_error: Some(error.to_string()),
                ..Self::default()
            },
        }
    }

    #[must_use]
    pub fn with_allowlist(mut self, allowlist: Allowlist) -> Self {
        self.allowlist = Some(allowlist);
        self
    }

    #[must_use]
    pub fn with_ssrf_guard(mut self, guard: SsrfGuard) -> Self {
        self.ssrf_guard = Some(guard);
        self
    }

    #[must_use]
    pub fn is_active(&self) -> bool {
        self.allowlist.as_ref().is_some_and(Allowlist::active)
            || self.ssrf_guard.as_ref().is_some_and(SsrfGuard::enabled)
    }

    pub(crate) fn check(&self, target: &str) -> Result<(), String> {
        self.check_reported(target)
            .map_err(|error| error.to_string())
    }

    pub(crate) fn check_reported(&self, target: &str) -> Result<(), PolicyRejection> {
        if let Some(error) = &self.configuration_error {
            return Err(PolicyRejection::Configuration(error.clone()));
        }
        if self
            .allowlist
            .as_ref()
            .is_some_and(|allowlist| !allowlist.allows_url(target))
        {
            return Err(PolicyRejection::Allowlist);
        }
        if let Some(guard) = &self.ssrf_guard {
            guard
                .allows_url(target)
                .map_err(|error| PolicyRejection::Ssrf(error.to_string()))?;
        }
        Ok(())
    }
}

/// An adapter for the human's already-running Safari session.
pub struct AttachEngine<R = OsascriptRunner> {
    runner: R,
    tab_name: String,
    command_timeout: Duration,
    navigation_timeout: Duration,
    poll_interval: Duration,
    interactions_opt_in: bool,
    navigation_policy: NavigationPolicy,
    closed: bool,
}

impl AttachEngine<OsascriptRunner> {
    #[must_use]
    pub fn default_engine() -> Self {
        Self::new(OsascriptRunner::default())
    }
}

impl<R: ScriptRunner> AttachEngine<R> {
    #[must_use]
    pub fn new(runner: R) -> Self {
        Self {
            runner,
            tab_name: DEFAULT_TAB_NAME.to_owned(),
            command_timeout: DEFAULT_COMMAND_TIMEOUT,
            navigation_timeout: DEFAULT_NAVIGATION_TIMEOUT,
            poll_interval: Duration::from_millis(250),
            interactions_opt_in: false,
            navigation_policy: NavigationPolicy::default(),
            closed: false,
        }
    }

    #[must_use]
    pub fn with_tab_name(mut self, name: impl Into<String>) -> Self {
        self.tab_name = name.into();
        self
    }

    #[must_use]
    pub fn with_command_timeout(mut self, timeout: Duration) -> Self {
        self.command_timeout = timeout;
        self
    }

    #[must_use]
    pub fn with_navigation_timeout(mut self, timeout: Duration) -> Self {
        self.navigation_timeout = timeout;
        self
    }

    #[must_use]
    pub fn with_poll_interval(mut self, interval: Duration) -> Self {
        self.poll_interval = interval;
        self
    }

    pub fn set_interactions_opt_in(&mut self, enabled: bool) {
        self.interactions_opt_in = enabled;
    }

    #[must_use]
    pub fn with_navigation_policy(mut self, policy: NavigationPolicy) -> Self {
        self.navigation_policy = policy;
        self
    }

    pub fn set_navigation_policy(&mut self, policy: NavigationPolicy) {
        self.navigation_policy = policy;
    }

    /// Verify that the selected Safari application/window/tab can be addressed.
    /// This never enables Automation or Remote Automation permissions.
    pub fn check_prerequisites(&self) -> Result<(), AttachError> {
        self.ensure_open()?;
        let script = "try\n tell application \"Safari\"\n if not (exists) then return \"application_unavailable\"\n set window_count to count windows\n if window_count = 0 then return \"window_unavailable\"\n set tab_count to count tabs of window 1\n if tab_count = 0 then return \"tab_unavailable\"\n return \"ok\"\n end tell\non error\n return \"automation_permission_denied\"\nend try".to_owned();
        let result = self
            .runner
            .run(&script, self.command_timeout)?
            .trim()
            .to_owned();
        let check = match result.as_str() {
            "ok" => return Ok(()),
            "application_unavailable" => SafariPrerequisite::ApplicationUnavailable,
            "automation_permission_denied" => SafariPrerequisite::AutomationPermissionDenied,
            "window_unavailable" => SafariPrerequisite::WindowUnavailable,
            "tab_unavailable" => SafariPrerequisite::TabUnavailable,
            _other => SafariPrerequisite::AutomationPermissionDenied,
        };
        Err(AttachError::Prerequisite {
            check,
            message: if result == "ok" {
                String::new()
            } else {
                result
            },
        })
    }

    /// Attaching is deliberately a no-op: this engine never launches or quits Safari.
    pub fn launch(&self) -> Result<(), AttachError> {
        self.ensure_open()
    }

    pub fn new_context(&self) -> Result<Context, AttachError> {
        self.ensure_open()?;
        Ok(Context {
            id: ENGINE_KIND.to_owned(),
        })
    }

    pub fn new_page(&self, _context: &Context) -> Result<Page, AttachError> {
        self.ensure_open()?;
        Ok(Page {
            id: "safari-live".to_owned(),
            session_id: String::new(),
        })
    }

    pub fn navigate(&self, _page: &Page, target: &str) -> Result<NavigationResult, AttachError> {
        self.ensure_open()?;
        let target = validate_web_target(target)?;
        if let Err(reason) = self.navigation_policy.check(&target) {
            return Err(AttachError::InvalidTarget { target, reason });
        }
        let reference = self.tab_reference();
        let set_url = format!(
            "tell application \"Safari\"\ntell {reference}\nset URL of its document to {}\nend tell\nend tell",
            apple_string(&target)
        );
        self.runner.run(&set_url, self.command_timeout)?;

        let started = std::time::Instant::now();
        while started.elapsed() < self.navigation_timeout {
            let remaining = self.navigation_timeout.saturating_sub(started.elapsed());
            let command_timeout = self.command_timeout.min(remaining);
            if let Ok(current) = self.current_url_with_timeout(command_timeout)
                && current == target
            {
                // Match Go's target check: repeated reads can still describe
                // the old page, and a redirect alone does not settle navigation.
                return Ok(NavigationResult {
                    frame_id: "safari-live".to_owned(),
                    loader_id: "safari-live".to_owned(),
                    url: current,
                    error_text: String::new(),
                });
            }
            let sleep_for = self.poll_interval.min(remaining);
            if !sleep_for.is_zero() {
                std::thread::sleep(sleep_for);
            }
        }
        Err(AttachError::NavigationDidNotSettle { target })
    }

    /// Evaluate fixed inspection expressions without enabling interaction scripts.
    pub fn evaluate(&self, expression: &str) -> Result<serde_json::Value, AttachError> {
        let expression = expression.trim();
        if !matches!(
            expression,
            "document.title" | "location.href" | "window.location.href"
        ) {
            return Err(AttachError::Unsupported {
                operation: "arbitrary evaluation".to_owned(),
            });
        }
        self.evaluate_script(expression)
    }

    /// Evaluate a bounded JSON-producing script after the explicit interaction
    /// opt-in. This is the shared safe seam used for storage and DOM-backed
    /// operations; callers cannot bypass the script-size and lifecycle guards.
    pub fn evaluate_script(&self, expression: &str) -> Result<serde_json::Value, AttachError> {
        self.ensure_open()?;
        if !self.interactions_opt_in {
            return Err(AttachError::Unsupported {
                operation: "evaluation (opt-in required)".to_owned(),
            });
        }
        if expression.len() > MAX_SCRIPT_BYTES {
            return Err(AttachError::Runner {
                message: format!("script exceeded {MAX_SCRIPT_BYTES} bytes"),
            });
        }
        let value = self.evaluate_raw(expression)?;
        let value = value.trim().to_owned();
        Ok(serde_json::from_str(&value).unwrap_or(serde_json::Value::String(value)))
    }

    #[must_use]
    pub fn capabilities(&self) -> Capabilities {
        let mut implemented = vec!["InspectionEngine", "NavigationStateProvider"];
        if self.interactions_opt_in && self.navigation_policy.is_active() {
            implemented.push("InteractionEngine");
        }
        let mut result = capabilities_for(ENGINE_KIND, implemented);
        result.launch_mode = "attach".to_owned();
        result
    }

    /// Closing detaches only; the human's Safari process is never touched.
    pub fn close(&mut self) -> Result<(), AttachError> {
        self.closed = true;
        Ok(())
    }

    fn ensure_open(&self) -> Result<(), AttachError> {
        if self.closed {
            Err(AttachError::Closed)
        } else {
            Ok(())
        }
    }

    fn tab_reference(&self) -> String {
        format!(
            "tab {} of window 1",
            apple_string(if self.tab_name.is_empty() {
                DEFAULT_TAB_NAME
            } else {
                &self.tab_name
            })
        )
    }

    fn evaluate_raw(&self, expression: &str) -> Result<String, AttachError> {
        self.evaluate_raw_with_timeout(expression, self.command_timeout)
    }

    fn evaluate_raw_with_timeout(
        &self,
        expression: &str,
        timeout: Duration,
    ) -> Result<String, AttachError> {
        let script = format!(
            "tell application \"Safari\"\ntell {}\ndo JavaScript {}\nend tell\nend tell",
            self.tab_reference(),
            apple_string(expression)
        );
        Ok(self
            .runner
            .run(&script, timeout)?
            .trim_end_matches('\n')
            .to_owned())
    }

    fn current_url_with_timeout(&self, timeout: Duration) -> Result<String, AttachError> {
        let raw = self.evaluate_raw_with_timeout("window.location.href", timeout)?;
        Ok(serde_json::from_str::<String>(&raw)
            .unwrap_or_else(|_| raw.trim_matches('"').to_owned()))
    }
}

fn validate_web_target(target: &str) -> Result<String, AttachError> {
    let trimmed = target.trim();
    let parsed = Url::parse(trimmed).map_err(|error| AttachError::InvalidTarget {
        target: target.to_owned(),
        reason: error.to_string(),
    })?;
    if !matches!(parsed.scheme(), "http" | "https") || parsed.host_str().is_none() {
        return Err(AttachError::InvalidTarget {
            target: target.to_owned(),
            reason: "http/https URL required".to_owned(),
        });
    }
    Ok(trimmed.to_owned())
}

fn apple_string(value: &str) -> String {
    format!("\"{}\"", value.replace('\\', "\\\\").replace('"', "\\\""))
}
