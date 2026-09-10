//! Resumable browser-session ownership and hard-stop state machine.

use std::collections::HashMap;
use std::fmt;
use std::sync::{Arc, Mutex};

use serde::{Deserialize, Serialize};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

pub const CODE_SESSION_USER_CONTROL: &str = "session_user_control";
pub const CODE_SESSION_INACTIVE: &str = "session_inactive";
pub const CODE_HANDOFF_TIMEOUT: &str = "handoff_timeout";

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ControlState {
    #[serde(rename = "")]
    Initial,
    Agent,
    AgentDelegated,
    User,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct Completion {
    pub keep: bool,
    pub completed_at: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct Session {
    pub id: String,
    pub control_id: String,
    pub control_state: ControlState,
    pub active: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub handoff_reason: String,
    pub updated_at: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub completion: Option<Completion>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct Transition {
    pub session_id: String,
    pub action: String,
    pub from: ControlState,
    pub to: ControlState,
    pub control_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub reason: String,
    pub confirmed: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub keep: Option<bool>,
    pub at: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct HardStopError {
    pub code: String,
    pub message: String,
    pub retryable: bool,
    pub requires_user_confirmation: bool,
    pub resume_hint: String,
}

impl fmt::Display for HardStopError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}
impl std::error::Error for HardStopError {}

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum SessionError {
    Invalid(String),
    NotFound(String),
    HardStop(HardStopError),
    Journal(String),
}
impl fmt::Display for SessionError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Invalid(message) | Self::NotFound(message) | Self::Journal(message) => {
                formatter.write_str(message)
            }
            Self::HardStop(error) => error.fmt(formatter),
        }
    }
}
impl std::error::Error for SessionError {}

pub type Clock = Arc<dyn Fn() -> String + Send + Sync>;
pub type Journal = Arc<dyn Fn(&Transition) -> Result<(), String> + Send + Sync>;

pub struct Manager {
    now: Clock,
    journal: Option<Journal>,
    sessions: Mutex<HashMap<String, Session>>,
}

impl Default for Manager {
    fn default() -> Self {
        Self::new(None, None)
    }
}

impl Manager {
    #[must_use]
    pub fn new(now: Option<Clock>, journal: Option<Journal>) -> Self {
        Self {
            now: now.unwrap_or_else(|| {
                Arc::new(|| {
                    OffsetDateTime::now_utc()
                        .format(&Rfc3339)
                        .expect("RFC3339 formatting cannot fail")
                })
            }),
            journal,
            sessions: Mutex::new(HashMap::new()),
        }
    }

    pub fn create(&self, id: &str, control_id: &str) -> Result<Session, SessionError> {
        if id.trim().is_empty() || control_id.trim().is_empty() {
            return Err(SessionError::Invalid(
                "session id and control id are required".to_owned(),
            ));
        }
        let mut sessions = self.lock()?;
        if sessions.contains_key(id) {
            return Err(SessionError::Invalid(format!(
                "session {id:?} already exists"
            )));
        }
        let now = (self.now)();
        let snapshot = Session {
            id: id.to_owned(),
            control_id: control_id.to_owned(),
            control_state: ControlState::Agent,
            active: true,
            handoff_reason: String::new(),
            updated_at: now.clone(),
            completion: None,
        };
        self.record(&Transition {
            session_id: id.to_owned(),
            action: "create".to_owned(),
            from: ControlState::Initial,
            to: ControlState::Agent,
            control_id: control_id.to_owned(),
            reason: String::new(),
            confirmed: true,
            keep: None,
            at: now,
        })?;
        sessions.insert(id.to_owned(), snapshot.clone());
        Ok(snapshot)
    }

    pub fn snapshot(&self, id: &str) -> Result<Session, SessionError> {
        self.lock()?
            .get(id)
            .cloned()
            .ok_or_else(|| SessionError::NotFound(format!("session {id:?} not found")))
    }

    pub fn reconnect(&self, id: &str) -> Result<Session, SessionError> {
        self.snapshot(id)
    }

    pub fn restore(&self, snapshot: Session) -> Result<(), SessionError> {
        validate_snapshot(&snapshot)?;
        self.lock()?.insert(snapshot.id.clone(), snapshot);
        Ok(())
    }

    pub fn check_agent_access(&self, id: &str, control_id: &str) -> Result<(), SessionError> {
        let sessions = self.lock()?;
        let Some(session) = sessions.get(id) else {
            return Err(inactive_error(id));
        };
        if !session.active {
            return Err(inactive_error(id));
        }
        if session.control_state != ControlState::Agent || session.control_id != control_id {
            return Err(user_control_error(id));
        }
        Ok(())
    }

    pub fn handoff(
        &self,
        id: &str,
        agent_control_id: &str,
        reason: &str,
    ) -> Result<Session, SessionError> {
        if reason.trim().is_empty() {
            return Err(SessionError::Invalid(
                "handoff reason is required".to_owned(),
            ));
        }
        let mut sessions = self.lock()?;
        let session = active_mut(&mut sessions, id)?;
        if session.control_state != ControlState::Agent || session.control_id != agent_control_id {
            return Err(user_control_error(id));
        }
        let transition = Transition {
            session_id: id.to_owned(),
            action: "handoff".to_owned(),
            from: session.control_state,
            to: ControlState::AgentDelegated,
            control_id: agent_control_id.to_owned(),
            reason: reason.to_owned(),
            confirmed: true,
            keep: None,
            at: (self.now)(),
        };
        self.record(&transition)?;
        session.control_state = ControlState::AgentDelegated;
        session.handoff_reason = reason.to_owned();
        session.updated_at = transition.at;
        Ok(session.clone())
    }

    pub fn claim(&self, id: &str, human_control_id: &str) -> Result<Session, SessionError> {
        if human_control_id.trim().is_empty() {
            return Err(SessionError::Invalid(
                "human control id is required".to_owned(),
            ));
        }
        let mut sessions = self.lock()?;
        let session = active_mut(&mut sessions, id)?;
        if session.control_state == ControlState::User {
            return Err(user_control_error(id));
        }
        if session.control_state != ControlState::AgentDelegated {
            return Err(SessionError::Invalid(format!(
                "session {id:?} is not awaiting a human claim"
            )));
        }
        let transition = Transition {
            session_id: id.to_owned(),
            action: "claim".to_owned(),
            from: session.control_state,
            to: ControlState::User,
            control_id: human_control_id.to_owned(),
            reason: String::new(),
            confirmed: true,
            keep: None,
            at: (self.now)(),
        };
        self.record(&transition)?;
        session.control_state = ControlState::User;
        session.control_id = human_control_id.to_owned();
        session.updated_at = transition.at;
        Ok(session.clone())
    }

    pub fn takeover(
        &self,
        id: &str,
        agent_control_id: &str,
        confirmed: bool,
    ) -> Result<Session, SessionError> {
        if agent_control_id.trim().is_empty() {
            return Err(SessionError::Invalid(
                "agent control id is required".to_owned(),
            ));
        }
        let mut sessions = self.lock()?;
        let session = active_mut(&mut sessions, id)?;
        if session.control_state == ControlState::Agent {
            return Ok(session.clone());
        }
        if !confirmed {
            return Err(user_control_error(id));
        }
        let transition = Transition {
            session_id: id.to_owned(),
            action: "takeover".to_owned(),
            from: session.control_state,
            to: ControlState::Agent,
            control_id: agent_control_id.to_owned(),
            reason: String::new(),
            confirmed: true,
            keep: None,
            at: (self.now)(),
        };
        self.record(&transition)?;
        session.control_state = ControlState::Agent;
        session.control_id = agent_control_id.to_owned();
        session.handoff_reason.clear();
        session.updated_at = transition.at;
        Ok(session.clone())
    }

    pub fn complete(
        &self,
        id: &str,
        control_id: &str,
        keep: bool,
    ) -> Result<Session, SessionError> {
        let mut sessions = self.lock()?;
        let session = active_mut(&mut sessions, id)?;
        if session.control_id != control_id {
            return Err(user_control_error(id));
        }
        let now = (self.now)();
        let transition = Transition {
            session_id: id.to_owned(),
            action: "complete".to_owned(),
            from: session.control_state,
            to: session.control_state,
            control_id: control_id.to_owned(),
            reason: String::new(),
            confirmed: true,
            keep: Some(keep),
            at: now.clone(),
        };
        self.record(&transition)?;
        session.active = false;
        session.completion = Some(Completion {
            keep,
            completed_at: now,
        });
        session.updated_at = transition.at;
        Ok(session.clone())
    }

    pub fn timeout(&self, id: &str) -> Result<(Session, HardStopError), SessionError> {
        let mut sessions = self.lock()?;
        let session = active_mut(&mut sessions, id)?;
        if session.control_state != ControlState::AgentDelegated {
            return Err(SessionError::Invalid(format!(
                "session {id:?} has no pending handoff"
            )));
        }
        let transition = Transition {
            session_id: id.to_owned(),
            action: "timeout".to_owned(),
            from: session.control_state,
            to: session.control_state,
            control_id: session.control_id.clone(),
            reason: String::new(),
            confirmed: false,
            keep: None,
            at: (self.now)(),
        };
        self.record(&transition)?;
        session.active = false;
        session.updated_at = transition.at;
        Ok((session.clone(), handoff_timeout_error(id)))
    }

    fn record(&self, transition: &Transition) -> Result<(), SessionError> {
        if let Some(journal) = &self.journal {
            journal(transition).map_err(SessionError::Journal)?;
        }
        Ok(())
    }

    fn lock(&self) -> Result<std::sync::MutexGuard<'_, HashMap<String, Session>>, SessionError> {
        self.sessions
            .lock()
            .map_err(|_| SessionError::Invalid("session manager lock poisoned".to_owned()))
    }
}

fn active_mut<'a>(
    sessions: &'a mut HashMap<String, Session>,
    id: &str,
) -> Result<&'a mut Session, SessionError> {
    let Some(session) = sessions.get_mut(id) else {
        return Err(inactive_error(id));
    };
    if !session.active {
        return Err(inactive_error(id));
    }
    Ok(session)
}

fn validate_snapshot(snapshot: &Session) -> Result<(), SessionError> {
    if snapshot.id.trim().is_empty() || snapshot.control_id.trim().is_empty() {
        return Err(SessionError::Invalid(
            "session snapshot requires id and control id".to_owned(),
        ));
    }
    if snapshot.updated_at.is_empty() {
        return Err(SessionError::Invalid(
            "session snapshot requires updated_at".to_owned(),
        ));
    }
    if snapshot.control_state == ControlState::Initial {
        return Err(SessionError::Invalid(
            "invalid control state \"\"".to_owned(),
        ));
    }
    Ok(())
}

fn user_control_error(id: &str) -> SessionError {
    SessionError::HardStop(HardStopError {
        code: CODE_SESSION_USER_CONTROL.to_owned(),
        message: format!("session {id:?} is controlled by a human"),
        retryable: false,
        requires_user_confirmation: true,
        resume_hint: "request explicit confirmation before taking control back".to_owned(),
    })
}

fn inactive_error(id: &str) -> SessionError {
    SessionError::HardStop(HardStopError {
        code: CODE_SESSION_INACTIVE.to_owned(),
        message: format!("session {id:?} is inactive"),
        retryable: false,
        requires_user_confirmation: false,
        resume_hint: "reopen the session before retrying".to_owned(),
    })
}

fn handoff_timeout_error(id: &str) -> HardStopError {
    HardStopError {
        code: CODE_HANDOFF_TIMEOUT.to_owned(),
        message: format!("handoff for session {id:?} timed out and was denied"),
        retryable: false,
        requires_user_confirmation: true,
        resume_hint: "start a new handoff after explicit human confirmation".to_owned(),
    }
}
