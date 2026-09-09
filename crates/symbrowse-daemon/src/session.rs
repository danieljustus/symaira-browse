use std::{
    collections::BTreeMap,
    fs, io,
    path::{Path, PathBuf},
    sync::RwLock,
};

pub const SESSION_SCHEMA_VERSION: u32 = 1;

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum SessionError {
    InvalidName(String),
    NotFound(String),
    Io(String),
    InvalidValue(String),
}

impl std::fmt::Display for SessionError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::InvalidName(name) => write!(f, "invalid session name: {name:?}"),
            Self::NotFound(name) => write!(f, "session not found: {name:?}"),
            Self::Io(message) | Self::InvalidValue(message) => f.write_str(message),
        }
    }
}
impl std::error::Error for SessionError {}

#[derive(Clone, Debug)]
pub struct SessionRegistryOptions {
    pub user_data_root: PathBuf,
    pub pid: u32,
    pub scope: String,
    pub origin_path: String,
}
impl Default for SessionRegistryOptions {
    fn default() -> Self {
        Self {
            user_data_root: default_user_data_root(),
            pid: std::process::id(),
            scope: String::new(),
            origin_path: String::new(),
        }
    }
}

#[derive(Clone, Debug, Default, serde::Serialize)]
pub struct SessionInfo {
    pub name: String,
    pub pid: u32,
    pub started_at: String,
    pub active_tabs: u32,
    pub last_activity: String,
    pub user_data_dir: String,
    pub browser_context_id: String,
    pub ref_count: usize,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub scope: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub origin_path: String,
}

#[derive(Clone, Debug, serde::Serialize)]
pub struct SessionListData {
    pub schema_version: u32,
    pub sessions: Vec<SessionInfo>,
}

#[derive(Clone, Debug)]
struct Session {
    info: SessionInfo,
    refs: BTreeMap<String, String>,
}

#[derive(Debug)]
pub struct SessionRegistry {
    user_data_root: PathBuf,
    pid: u32,
    scope: String,
    origin_path: String,
    sessions: RwLock<BTreeMap<String, Session>>,
}

impl SessionRegistry {
    pub fn new(options: SessionRegistryOptions) -> Self {
        Self {
            user_data_root: options.user_data_root,
            pid: if options.pid == 0 {
                std::process::id()
            } else {
                options.pid
            },
            scope: options.scope,
            origin_path: options.origin_path,
            sessions: RwLock::new(BTreeMap::new()),
        }
    }

    pub fn user_data_root(&self) -> &Path {
        &self.user_data_root
    }

    pub fn ensure(&self, name: &str) -> Result<SessionInfo, SessionError> {
        if !crate::validate_session(name) {
            return Err(SessionError::InvalidName(name.to_owned()));
        }
        if let Some(session) = self
            .sessions
            .read()
            .expect("session registry poisoned")
            .get(name)
        {
            return Ok(session.info.clone());
        }
        let mut sessions = self.sessions.write().expect("session registry poisoned");
        if let Some(session) = sessions.get(name) {
            return Ok(session.info.clone());
        }
        let user_data_dir = self.user_data_root.join(name);
        fs::create_dir_all(&user_data_dir)
            .map_err(|error| SessionError::Io(format!("create user data directory: {error}")))?;
        secure_directory(&user_data_dir)
            .map_err(|error| SessionError::Io(format!("secure user data directory: {error}")))?;
        let now = timestamp_now();
        let info = SessionInfo {
            name: name.to_owned(),
            pid: self.pid,
            started_at: now.clone(),
            active_tabs: 0,
            last_activity: now,
            user_data_dir: user_data_dir.display().to_string(),
            browser_context_id: format!("context-{name}"),
            ref_count: 0,
            scope: self.scope.clone(),
            origin_path: self.origin_path.clone(),
        };
        sessions.insert(
            name.to_owned(),
            Session {
                info: info.clone(),
                refs: BTreeMap::new(),
            },
        );
        Ok(info)
    }

    pub fn get(&self, name: &str) -> Result<SessionInfo, SessionError> {
        self.sessions
            .read()
            .expect("session registry poisoned")
            .get(name)
            .map(|session| session.info.clone())
            .ok_or_else(|| SessionError::NotFound(name.to_owned()))
    }

    pub fn list(&self) -> Vec<SessionInfo> {
        self.sessions
            .read()
            .expect("session registry poisoned")
            .values()
            .map(|session| session.info.clone())
            .collect()
    }

    pub fn list_data(&self) -> SessionListData {
        SessionListData {
            schema_version: SESSION_SCHEMA_VERSION,
            sessions: self.list(),
        }
    }

    pub fn touch(&self, name: &str) -> Result<(), SessionError> {
        let mut sessions = self.sessions.write().expect("session registry poisoned");
        let session = sessions
            .get_mut(name)
            .ok_or_else(|| SessionError::NotFound(name.to_owned()))?;
        session.info.last_activity = timestamp_now();
        Ok(())
    }

    pub fn set_active_tabs(&self, name: &str, count: u32) -> Result<(), SessionError> {
        let mut sessions = self.sessions.write().expect("session registry poisoned");
        let session = sessions
            .get_mut(name)
            .ok_or_else(|| SessionError::NotFound(name.to_owned()))?;
        session.info.active_tabs = count;
        Ok(())
    }

    pub fn set_ref(&self, name: &str, key: &str, reference: &str) -> Result<(), SessionError> {
        if key.is_empty() || reference.is_empty() {
            return Err(SessionError::InvalidValue(
                "ref key and ref are required".into(),
            ));
        }
        let mut sessions = self.sessions.write().expect("session registry poisoned");
        let session = sessions
            .get_mut(name)
            .ok_or_else(|| SessionError::NotFound(name.to_owned()))?;
        session.refs.insert(key.to_owned(), reference.to_owned());
        session.info.ref_count = session.refs.len();
        Ok(())
    }

    pub fn reference(&self, name: &str, key: &str) -> Result<String, SessionError> {
        self.sessions
            .read()
            .expect("session registry poisoned")
            .get(name)
            .ok_or_else(|| SessionError::NotFound(name.to_owned()))?
            .refs
            .get(key)
            .cloned()
            .ok_or_else(|| {
                SessionError::InvalidValue(format!("ref {key:?} not found in session {name:?}"))
            })
    }

    pub fn clear(&self) {
        self.sessions
            .write()
            .expect("session registry poisoned")
            .clear();
    }
}

fn default_user_data_root() -> PathBuf {
    if let Ok(path) = std::env::var("SYMBROWSE_USER_DATA_DIR") {
        return PathBuf::from(path);
    }
    if cfg!(target_os = "macos")
        && let Ok(home) = std::env::var("HOME")
    {
        return PathBuf::from(home).join("Library/Caches/symbrowse/sessions");
    }
    if cfg!(windows)
        && let Ok(local_app_data) = std::env::var("LOCALAPPDATA")
    {
        return PathBuf::from(local_app_data).join("symbrowse/sessions");
    }
    if let Ok(path) = std::env::var("XDG_CACHE_HOME") {
        return PathBuf::from(path).join("symbrowse/sessions");
    }
    if let Ok(home) = std::env::var("HOME") {
        return PathBuf::from(home).join(".cache/symbrowse/sessions");
    }
    std::env::temp_dir().join("symbrowse/sessions")
}

#[cfg(unix)]
fn secure_directory(path: &Path) -> io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
}
#[cfg(not(unix))]
fn secure_directory(_path: &Path) -> io::Result<()> {
    Ok(())
}

fn timestamp_now() -> String {
    time::OffsetDateTime::now_utc()
        .format(&time::format_description::well_known::Rfc3339)
        .unwrap_or_else(|_| "1970-01-01T00:00:00Z".into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn registry_isolates_profiles_and_orders_sessions() {
        let registry = SessionRegistry::new(SessionRegistryOptions {
            user_data_root: std::env::temp_dir()
                .join(format!("symbrowse-session-test-{}", std::process::id())),
            pid: 4242,
            scope: "worktree".into(),
            origin_path: "/tmp/project".into(),
        });
        let alpha = registry.ensure("alpha").unwrap();
        let beta = registry.ensure("beta").unwrap();
        assert_ne!(alpha.user_data_dir, beta.user_data_dir);
        assert_eq!(alpha.scope, "worktree");
        assert_eq!(alpha.origin_path, "/tmp/project");
        assert!(
            time::OffsetDateTime::parse(
                &alpha.started_at,
                &time::format_description::well_known::Rfc3339,
            )
            .is_ok()
        );
        registry.set_ref("alpha", "save", "@e1").unwrap();
        assert!(registry.reference("beta", "save").is_err());
        assert_eq!(registry.list_data().sessions[0].name, "alpha");
        assert_eq!(registry.get("alpha").unwrap().ref_count, 1);
        registry.clear();
        assert!(registry.list().is_empty());
        let _ = fs::remove_dir_all(registry.user_data_root());
    }
}
