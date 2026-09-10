//! Bounded out-of-band human-control prompts.

use serde::{Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    sync::{Condvar, Mutex},
    time::{Duration, SystemTime},
};

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Kind {
    Handoff,
    Approval,
    Watch,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Status {
    Pending,
    Completed,
    Cancelled,
    Timeout,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Prompt {
    pub id: String,
    pub kind: Kind,
    pub title: String,
    pub reason: String,
    pub status: Status,
    pub created_at: String,
    pub timeout_ms: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub result: Option<serde_json::Value>,
}

#[derive(Default)]
struct State {
    next: u64,
    prompts: BTreeMap<String, Prompt>,
}
/// Thread-safe prompt manager. Waits always have a finite deadline when the
/// prompt requests one; timeout resolves to deny, never to allow.
pub struct Manager {
    state: Mutex<State>,
    wake: Condvar,
}
impl Default for Manager {
    fn default() -> Self {
        Self::new()
    }
}
impl Manager {
    #[must_use]
    pub fn new() -> Self {
        Self {
            state: Mutex::new(State::default()),
            wake: Condvar::new(),
        }
    }
    pub fn create(
        &self,
        kind: Kind,
        title: impl Into<String>,
        reason: impl Into<String>,
        timeout: Duration,
        created_at: impl Into<String>,
    ) -> Prompt {
        let mut state = self.state.lock().expect("prompt lock");
        state.next += 1;
        let prompt = Prompt {
            id: format!("oob-{}", state.next),
            kind,
            title: title.into(),
            reason: reason.into(),
            status: Status::Pending,
            created_at: created_at.into(),
            timeout_ms: timeout.as_millis().try_into().unwrap_or(u64::MAX),
            result: None,
        };
        state.prompts.insert(prompt.id.clone(), prompt.clone());
        prompt
    }
    pub fn get(&self, id: &str) -> Option<Prompt> {
        self.state.lock().ok()?.prompts.get(id).cloned()
    }
    pub fn complete(&self, id: &str, result: Option<serde_json::Value>) -> Option<Prompt> {
        self.finish(id, Status::Completed, result)
    }
    pub fn cancel(&self, id: &str, reason: impl Into<String>) -> Option<Prompt> {
        self.finish(
            id,
            Status::Cancelled,
            Some(serde_json::json!({"reason": reason.into()})),
        )
    }
    pub fn expire(&self, id: &str) -> Option<Prompt> {
        self.finish(id, Status::Timeout, None)
    }
    fn finish(
        &self,
        id: &str,
        status: Status,
        result: Option<serde_json::Value>,
    ) -> Option<Prompt> {
        let mut state = self.state.lock().ok()?;
        let prompt = state.prompts.get_mut(id)?;
        if prompt.status != Status::Pending {
            return None;
        }
        prompt.status = status;
        prompt.result = result;
        let result = prompt.clone();
        self.wake.notify_all();
        Some(result)
    }
    pub fn wait(&self, id: &str, timeout: Duration) -> Option<Prompt> {
        let deadline = SystemTime::now().checked_add(timeout);
        let mut state = self.state.lock().ok()?;
        loop {
            let prompt = state.prompts.get(id)?.clone();
            if prompt.status != Status::Pending {
                return Some(prompt);
            }
            let remaining = deadline.and_then(|d| d.duration_since(SystemTime::now()).ok());
            let Some(remaining) = remaining else {
                drop(state);
                return self.expire(id);
            };
            let (next, result) = self.wake.wait_timeout(state, remaining).ok()?;
            state = next;
            if result.timed_out() {
                drop(state);
                return self.expire(id);
            }
        }
    }
    /// Approval semantics: only an explicit completed decision allows.
    pub fn resolve(&self, id: &str, timeout: Duration) -> Option<(bool, Prompt)> {
        let prompt = self.wait(id, timeout)?;
        Some((prompt.status == Status::Completed, prompt))
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct NotificationCommand {
    pub program: String,
    pub args: Vec<String>,
}
/// Build a notification command without placing credentials in argv. Execution
/// belongs to the platform adapter, which must enforce its own process bound.
#[must_use]
pub fn notification_command(prompt: &Prompt) -> NotificationCommand {
    let title = format!("Symaira Browse: {:?}", prompt.kind).to_ascii_lowercase();
    let safe_title = safe_text(&prompt.title);
    let safe_reason = safe_text(&prompt.reason);
    let message = if safe_reason.is_empty() {
        safe_title.clone()
    } else {
        format!("{} — {}", safe_title, safe_reason)
    };
    NotificationCommand {
        program: "osascript".to_owned(),
        args: vec![
            "-e".to_owned(),
            format!(
                "display notification {} with title {}",
                quote_applescript(&message),
                quote_applescript(&title)
            ),
        ],
    }
}
fn quote_applescript(value: &str) -> String {
    format!("\"{}\"", value.replace('\\', "\\\\").replace('"', "\\\""))
}

fn safe_text(value: &str) -> String {
    let lower = value.to_ascii_lowercase();
    let sensitive = [
        "authorization",
        "bearer ",
        "cookie",
        "password",
        "secret",
        "token",
        "api_key",
        "apikey",
        "op://",
    ];
    if sensitive.iter().any(|marker| lower.contains(marker)) {
        "[REDACTED]".to_owned()
    } else {
        value.to_owned()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn timeout_is_explicit_deny() {
        let manager = Manager::new();
        let prompt = manager.create(
            Kind::Approval,
            "Approve",
            "danger",
            Duration::from_millis(1),
            "fixed",
        );
        let (allowed, result) = manager
            .resolve(&prompt.id, Duration::from_millis(10))
            .unwrap();
        assert!(!allowed);
        assert_eq!(result.status, Status::Timeout);
    }
    #[test]
    fn notification_argv_contains_no_secret_marker() {
        let prompt = Prompt {
            id: "oob-1".into(),
            kind: Kind::Handoff,
            title: "Handoff".into(),
            reason: "2FA".into(),
            status: Status::Pending,
            created_at: "fixed".into(),
            timeout_ms: 0,
            result: None,
        };
        let command = notification_command(&prompt);
        assert_eq!(command.program, "osascript");
        assert!(!command.args.join(" ").contains("password"));
    }
    #[test]
    fn notification_reason_is_redacted_before_argv() {
        let prompt = Prompt {
            id: "oob-2".into(),
            kind: Kind::Approval,
            title: "Approve".into(),
            reason: "password=do-not-leak".into(),
            status: Status::Pending,
            created_at: "fixed".into(),
            timeout_ms: 0,
            result: None,
        };
        assert!(
            !notification_command(&prompt)
                .args
                .join(" ")
                .contains("do-not-leak")
        );
    }
}
