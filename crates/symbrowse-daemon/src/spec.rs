use std::{path::PathBuf, time::Duration};

use symbrowse_core::config::Config;

/// Effective configuration owned by one daemon session.
///
/// The daemon, its clients, MCP autostart, state store, and status handshake
/// all use this value. Keeping it typed prevents a caller from silently
/// falling back to process-global defaults after startup.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SessionSpec {
    pub session: String,
    pub state_dir: PathBuf,
    pub cache_dir: PathBuf,
    pub engine: String,
    pub executable_path: PathBuf,
    pub cdp_endpoint: String,
    pub allowed_domains: Vec<String>,
    pub ssrf_enabled: bool,
    pub allow_private: bool,
    pub fetch_robots: bool,
    pub fetch_user_agent: String,
    pub operation_timeout: Duration,
    pub read_timeout: Duration,
    pub idle_timeout: Option<Duration>,
    pub state_expire_days: i64,
    pub daemon_log: PathBuf,
    pub socket_path: PathBuf,
}

impl SessionSpec {
    #[must_use]
    pub fn for_session(session: impl Into<String>) -> Self {
        let session = session.into();
        let state_dir = default_state_dir();
        Self {
            socket_path: default_socket_path(&session),
            daemon_log: default_log_path(),
            session,
            cache_dir: default_cache_dir(),
            state_dir,
            engine: "chrome".into(),
            executable_path: PathBuf::new(),
            cdp_endpoint: String::new(),
            allowed_domains: Vec::new(),
            ssrf_enabled: false,
            allow_private: false,
            fetch_robots: true,
            fetch_user_agent: "symbrowse/1.0".into(),
            operation_timeout: Duration::from_secs(25),
            read_timeout: Duration::from_secs(30),
            idle_timeout: Some(Duration::from_secs(1800)),
            state_expire_days: 30,
        }
    }

    pub fn from_config(config: &Config, session: impl Into<String>) -> Self {
        let mut spec = Self::for_session(session);
        spec.state_dir = PathBuf::from(&config.state_dir);
        spec.cache_dir = PathBuf::from(&config.cache_dir);
        spec.engine = if config.engine.is_empty() {
            "chrome"
        } else {
            &config.engine
        }
        .into();
        spec.executable_path = PathBuf::from(&config.executable_path);
        spec.cdp_endpoint = config.cdp_endpoint.clone();
        spec.allowed_domains = config.allowed_domains.clone();
        spec.ssrf_enabled = config.ssrf_enabled;
        spec.allow_private = config.allow_private;
        spec.fetch_robots = config.fetch_robots;
        spec.fetch_user_agent = config.fetch_user_agent.clone();
        spec.operation_timeout = Duration::from_secs(config.operation_timeout.max(1) as u64);
        spec.read_timeout = Duration::from_secs(config.read_timeout.max(1) as u64);
        spec.idle_timeout =
            (config.idle_timeout > 0).then_some(Duration::from_secs(config.idle_timeout as u64));
        spec.state_expire_days = config.state_expire_days.max(1);
        spec.daemon_log = if config.daemon_log.is_empty() {
            default_log_path()
        } else {
            PathBuf::from(&config.daemon_log)
        };
        spec.socket_path = default_socket_path(&spec.session);
        spec
    }

    #[must_use]
    pub fn state_store_dir(&self) -> PathBuf {
        self.state_dir.join("states")
    }

    #[must_use]
    pub fn user_data_dir(&self) -> PathBuf {
        self.state_dir.join("sessions").join(&self.session)
    }

    #[must_use]
    pub fn output_cache_dir(&self) -> PathBuf {
        self.cache_dir.join("out")
    }
}

#[must_use]
pub fn default_state_dir() -> PathBuf {
    if let Some(path) = std::env::var_os("SYMBROWSE_STATE_DIR") {
        return PathBuf::from(path);
    }
    if let Some(path) = std::env::var_os("XDG_STATE_HOME") {
        return PathBuf::from(path).join("symbrowse");
    }
    std::env::var_os("HOME").map_or_else(
        || std::env::temp_dir().join("symbrowse/state"),
        |home| PathBuf::from(home).join(".local/state/symbrowse"),
    )
}

#[must_use]
pub fn default_cache_dir() -> PathBuf {
    if let Some(path) = std::env::var_os("SYMBROWSE_CACHE_DIR") {
        return PathBuf::from(path);
    }
    if let Some(path) = std::env::var_os("XDG_CACHE_HOME") {
        return PathBuf::from(path).join("symbrowse");
    }
    std::env::var_os("HOME").map_or_else(
        || std::env::temp_dir().join("symbrowse/cache"),
        |home| PathBuf::from(home).join(".cache/symbrowse"),
    )
}

#[must_use]
pub fn default_log_path() -> PathBuf {
    std::env::var_os("SYMBROWSE_DAEMON_LOG")
        .map(PathBuf::from)
        .unwrap_or_else(|| default_state_dir().join("daemon.log"))
}

#[must_use]
pub fn default_socket_path(session: &str) -> PathBuf {
    if cfg!(windows) {
        return PathBuf::from(format!(r"\\.\pipe\symbrowse-{session}"));
    }
    if let Some(runtime) = std::env::var_os("XDG_RUNTIME_DIR") {
        return PathBuf::from(runtime)
            .join("symbrowse")
            .join(format!("{session}.sock"));
    }
    if cfg!(target_os = "macos") {
        return std::env::var_os("HOME").map_or_else(
            || std::env::temp_dir().join(format!("symbrowse-{session}.sock")),
            |home| {
                PathBuf::from(home)
                    .join("Library/Caches/symbrowse/run")
                    .join(format!("{session}.sock"))
            },
        );
    }
    default_state_dir()
        .join("run")
        .join(format!("{session}.sock"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn spec_paths_are_one_configured_tree() {
        let spec = SessionSpec::for_session("alpha");
        assert!(spec.state_store_dir().ends_with("states"));
        assert!(spec.user_data_dir().ends_with("sessions/alpha"));
        assert!(spec.output_cache_dir().ends_with("out"));
    }
}
