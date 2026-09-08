#![deny(unsafe_code)]

//! Deterministic TOML/XDG/environment/flag configuration resolution.

use std::{
    collections::{BTreeMap, HashMap},
    fmt, fs,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};

const APP_NAME: &str = "symbrowse";
const FIELDS: [&str; 26] = [
    "log_level",
    "log_format",
    "config_dir",
    "cache_dir",
    "state_dir",
    "executable_path",
    "cdp_endpoint",
    "engine",
    "allowed_domains",
    "ssrf_enabled",
    "allow_private",
    "headless",
    "cache_ttl_hours",
    "fetch_robots",
    "fetch_user_agent",
    "fetch_no_cache",
    "idle_timeout",
    "operation_timeout",
    "read_timeout",
    "state_expire_days",
    "autosave",
    "autosave_interval",
    "autosave_key",
    "upload_dirs",
    "daemon_log",
    "approval_timeout",
];

/// Explicit transport selection; browser engines never describe static/compat.
#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, PartialEq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum TransportMode {
    Static,
    Browser,
    Compat,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, PartialEq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum BrowserEngine {
    Chrome,
    Safari,
    Firefox,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct TransportSelection {
    pub mode: TransportMode,
    pub engine: Option<BrowserEngine>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SelectionError {
    pub code: &'static str,
    pub message: String,
}
impl fmt::Display for SelectionError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}
impl std::error::Error for SelectionError {}

/// Resolve a mode and engine exhaustively, with no fallback.
pub fn resolve_selection(
    mode: Option<&str>,
    engine: Option<&str>,
) -> std::result::Result<TransportSelection, SelectionError> {
    let mode = match mode.unwrap_or("browser") {
        "static" => TransportMode::Static,
        "browser" => TransportMode::Browser,
        "compat" => TransportMode::Compat,
        value => {
            return Err(SelectionError {
                code: "invalid_transport_mode",
                message: format!(
                    "unknown transport mode {value:?}: use static, browser, or compat"
                ),
            });
        }
    };
    let engine = engine
        .filter(|value| !value.is_empty())
        .map(|value| match value {
            "chrome" => Ok(BrowserEngine::Chrome),
            "safari" | "safari-attach" | "safari-bidi" => Ok(BrowserEngine::Safari),
            "firefox" => Ok(BrowserEngine::Firefox),
            value => Err(SelectionError {
                code: "invalid_browser_engine",
                message: format!(
                    "unknown browser engine {value:?}: use chrome, safari, or firefox"
                ),
            }),
        })
        .transpose()?;
    match (mode, engine) {
        (TransportMode::Browser, None) => Err(SelectionError {
            code: "browser_engine_required",
            message: "browser mode requires an explicit engine".into(),
        }),
        (TransportMode::Browser, Some(engine)) => Ok(TransportSelection {
            mode,
            engine: Some(engine),
        }),
        (mode, Some(_)) => Err(SelectionError {
            code: "engine_not_allowed",
            message: format!("browser engine is only valid in browser mode, not {mode:?}"),
        }),
        (mode, None) => Ok(TransportSelection { mode, engine: None }),
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct Config {
    pub log_level: String,
    pub log_format: String,
    pub config_dir: String,
    pub cache_dir: String,
    pub state_dir: String,
    pub executable_path: String,
    pub cdp_endpoint: String,
    pub engine: String,
    #[serde(default = "default_mode")]
    pub mode: String,
    pub allowed_domains: Vec<String>,
    pub ssrf_enabled: bool,
    pub allow_private: bool,
    pub headless: bool,
    pub cache_ttl_hours: i64,
    pub fetch_robots: bool,
    pub fetch_user_agent: String,
    pub fetch_no_cache: bool,
    pub idle_timeout: i64,
    pub operation_timeout: i64,
    pub read_timeout: i64,
    pub state_expire_days: i64,
    pub autosave: String,
    pub autosave_interval: i64,
    pub autosave_key: String,
    pub upload_dirs: Vec<String>,
    pub daemon_log: String,
    pub approval_timeout: i64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Result {
    pub config: Config,
    pub sources: BTreeMap<String, String>,
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct FlagOverrides {
    pub log_level: Option<String>,
    pub log_format: Option<String>,
    pub config_dir: Option<String>,
    pub cache_dir: Option<String>,
    pub state_dir: Option<String>,
    pub executable_path: Option<String>,
    pub mode: Option<String>,
    pub engine: Option<String>,
}

#[derive(Clone, Debug)]
pub struct LoadContext {
    pub home: PathBuf,
    pub cwd: PathBuf,
    pub xdg_config_home: Option<PathBuf>,
    pub xdg_cache_home: Option<PathBuf>,
    pub xdg_state_home: Option<PathBuf>,
    pub env: HashMap<String, String>,
    pub flags: FlagOverrides,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct Field {
    pub value: String,
    pub source: String,
}

#[derive(Debug)]
pub struct ConfigError(String);

impl fmt::Display for ConfigError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl std::error::Error for ConfigError {}

impl LoadContext {
    /// Captures the process environment without reading configuration files.
    pub fn from_process(flags: FlagOverrides) -> std::result::Result<Self, ConfigError> {
        let home = std::env::var_os("HOME")
            .or_else(|| std::env::var_os("USERPROFILE"))
            .map(PathBuf::from)
            .ok_or_else(|| ConfigError("cannot determine home directory".to_owned()))?;
        let cwd = std::env::current_dir().map_err(|error| ConfigError(error.to_string()))?;
        Ok(Self {
            home,
            cwd,
            xdg_config_home: std::env::var_os("XDG_CONFIG_HOME").map(PathBuf::from),
            xdg_cache_home: std::env::var_os("XDG_CACHE_HOME").map(PathBuf::from),
            xdg_state_home: std::env::var_os("XDG_STATE_HOME").map(PathBuf::from),
            env: std::env::vars().collect(),
            flags,
        })
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
struct PartialConfig {
    log_level: Option<String>,
    log_format: Option<String>,
    config_dir: Option<String>,
    cache_dir: Option<String>,
    state_dir: Option<String>,
    executable_path: Option<String>,
    cdp_endpoint: Option<String>,
    engine: Option<String>,
    mode: Option<String>,
    allowed_domains: Option<Vec<String>>,
    ssrf_enabled: Option<bool>,
    allow_private: Option<bool>,
    headless: Option<bool>,
    cache_ttl_hours: Option<i64>,
    fetch_robots: Option<bool>,
    fetch_user_agent: Option<String>,
    fetch_no_cache: Option<bool>,
    idle_timeout: Option<i64>,
    operation_timeout: Option<i64>,
    read_timeout: Option<i64>,
    state_expire_days: Option<i64>,
    autosave: Option<String>,
    autosave_interval: Option<i64>,
    autosave_key: Option<String>,
    upload_dirs: Option<Vec<String>>,
    daemon_log: Option<String>,
    approval_timeout: Option<i64>,
}

/// Resolves defaults < global TOML < project TOML < environment < flags.
pub fn load(context: &LoadContext) -> std::result::Result<Result, ConfigError> {
    let config_home = context
        .xdg_config_home
        .clone()
        .unwrap_or_else(|| context.home.join(".config"));
    let cache_home = context
        .xdg_cache_home
        .clone()
        .unwrap_or_else(|| context.home.join(".cache"));
    let state_home = context
        .xdg_state_home
        .clone()
        .unwrap_or_else(|| context.home.join(".local/state"));
    let mut config = Config {
        log_level: "warn".to_owned(),
        log_format: "text".to_owned(),
        config_dir: display(config_home.join(APP_NAME)),
        cache_dir: display(cache_home.join(APP_NAME)),
        state_dir: display(state_home.join(APP_NAME)),
        executable_path: String::new(),
        cdp_endpoint: String::new(),
        engine: "chrome".to_owned(),
        mode: "browser".to_owned(),
        allowed_domains: Vec::new(),
        ssrf_enabled: false,
        allow_private: false,
        headless: false,
        cache_ttl_hours: 24,
        fetch_robots: true,
        fetch_user_agent: "symbrowse/1.0".to_owned(),
        fetch_no_cache: false,
        idle_timeout: 1800,
        operation_timeout: 25,
        read_timeout: 30,
        state_expire_days: 30,
        autosave: "auto".to_owned(),
        autosave_interval: 30,
        autosave_key: String::new(),
        upload_dirs: vec![display(&context.cwd)],
        daemon_log: display(state_home.join(APP_NAME).join("daemon.log")),
        approval_timeout: 60,
    };
    let mut sources = FIELDS
        .iter()
        .map(|field| ((*field).to_owned(), "default".to_owned()))
        .collect();
    apply_file(
        &mut config,
        &mut sources,
        &config_home.join(APP_NAME).join("config.toml"),
        "global",
    )
    .map_err(|error| ConfigError(format!("failed to load global configuration: {error}")))?;
    apply_file(
        &mut config,
        &mut sources,
        &context.cwd.join(".symbrowse.toml"),
        "project",
    )
    .map_err(|error| ConfigError(format!("failed to load project configuration: {error}")))?;
    apply_env(&mut config, &mut sources, &context.env).map_err(|error| {
        ConfigError(format!("failed to load environment configuration: {error}"))
    })?;
    apply_flags(&mut config, &mut sources, &context.flags);
    if sources["daemon_log"] == "default" {
        config.daemon_log = display(Path::new(&config.state_dir).join("daemon.log"));
    }
    validate(&config).map_err(|error| ConfigError(format!("invalid configuration: {error}")))?;
    Ok(Result { config, sources })
}

/// Produces the stable string-valued `config show` field table.
#[must_use]
pub fn show_fields(result: &Result) -> BTreeMap<String, Field> {
    let config = &result.config;
    let values = [
        ("allow_private", config.allow_private.to_string()),
        ("allowed_domains", config.allowed_domains.join(",")),
        ("approval_timeout", config.approval_timeout.to_string()),
        ("autosave", config.autosave.clone()),
        ("autosave_interval", config.autosave_interval.to_string()),
        ("autosave_key", config.autosave_key.clone()),
        ("cache_dir", config.cache_dir.clone()),
        ("cache_ttl_hours", config.cache_ttl_hours.to_string()),
        ("cdp_endpoint", config.cdp_endpoint.clone()),
        ("config_dir", config.config_dir.clone()),
        ("daemon_log", config.daemon_log.clone()),
        ("engine", config.engine.clone()),
        ("executable_path", config.executable_path.clone()),
        ("fetch_no_cache", config.fetch_no_cache.to_string()),
        ("fetch_robots", config.fetch_robots.to_string()),
        ("fetch_user_agent", config.fetch_user_agent.clone()),
        ("headless", config.headless.to_string()),
        ("idle_timeout", config.idle_timeout.to_string()),
        ("log_format", config.log_format.clone()),
        ("log_level", config.log_level.clone()),
        ("operation_timeout", config.operation_timeout.to_string()),
        ("read_timeout", config.read_timeout.to_string()),
        ("ssrf_enabled", config.ssrf_enabled.to_string()),
        ("state_dir", config.state_dir.clone()),
        ("state_expire_days", config.state_expire_days.to_string()),
        ("upload_dirs", config.upload_dirs.join(",")),
    ];
    values
        .into_iter()
        .map(|(name, value)| {
            (
                name.to_owned(),
                Field {
                    value,
                    source: result.sources[name].clone(),
                },
            )
        })
        .collect()
}

/// Renders `config show` text in stable lexical field order.
#[must_use]
pub fn render_show_text(result: &Result) -> String {
    let mut output = String::new();
    for (name, field) in show_fields(result) {
        output.push_str(&format!(
            "{name}={} (source: {})\n",
            field.value, field.source
        ));
    }
    output
}

/// Renders the Go `yaml.v3` shape used by `config show --output yaml`.
#[must_use]
pub fn render_show_yaml(result: &Result) -> String {
    let mut output = String::from("success: true\ndata:\n    fields:\n");
    for (name, field) in show_fields(result) {
        output.push_str(&format!(
            "        {name}:\n            value: {}\n            source: {}\n",
            yaml_config_string(&field.value),
            yaml_config_string(&field.source)
        ));
    }
    output.push_str("warnings: []\nerror: null\n");
    output
}

fn yaml_config_string(value: &str) -> String {
    let lower = value.to_ascii_lowercase();
    let reserved = matches!(
        lower.as_str(),
        "y" | "yes" | "n" | "no" | "true" | "false" | "on" | "off" | "null" | "~"
    );
    let numeric = value.parse::<i64>().is_ok() || value.parse::<f64>().is_ok();
    let special_start = value
        .chars()
        .next()
        .is_some_and(|character| "-?:,[]{}#&*!|>'\"%@`".contains(character));
    if value.is_empty()
        || reserved
        || numeric
        || special_start
        || value.contains(['\n', '\r', '\t'])
    {
        serde_json::to_string(value).expect("string serialization cannot fail")
    } else {
        value.to_owned()
    }
}

fn apply_file(
    config: &mut Config,
    sources: &mut BTreeMap<String, String>,
    path: &Path,
    source: &str,
) -> std::result::Result<(), ConfigError> {
    let content = match fs::read_to_string(path) {
        Ok(content) => content,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(ConfigError(error.to_string())),
    };
    let patch: PartialConfig =
        toml::from_str(&content).map_err(|error| ConfigError(error.to_string()))?;
    apply_partial(config, sources, patch, source);
    Ok(())
}

fn apply_partial(
    config: &mut Config,
    sources: &mut BTreeMap<String, String>,
    patch: PartialConfig,
    source: &str,
) {
    macro_rules! set {
        ($field:ident) => {
            if let Some(value) = patch.$field {
                config.$field = value;
                sources.insert(stringify!($field).to_owned(), source.to_owned());
            }
        };
    }
    set!(log_level);
    set!(log_format);
    set!(config_dir);
    set!(cache_dir);
    set!(state_dir);
    set!(executable_path);
    set!(cdp_endpoint);
    set!(engine);
    set!(mode);
    set!(allowed_domains);
    set!(ssrf_enabled);
    set!(allow_private);
    set!(headless);
    set!(cache_ttl_hours);
    set!(fetch_robots);
    set!(fetch_user_agent);
    set!(fetch_no_cache);
    set!(idle_timeout);
    set!(operation_timeout);
    set!(read_timeout);
    set!(state_expire_days);
    set!(autosave);
    set!(autosave_interval);
    set!(autosave_key);
    set!(upload_dirs);
    set!(daemon_log);
    set!(approval_timeout);
}

fn apply_env(
    config: &mut Config,
    sources: &mut BTreeMap<String, String>,
    env: &HashMap<String, String>,
) -> std::result::Result<(), ConfigError> {
    macro_rules! string_env {
        ($field:ident, $name:literal) => {
            if let Some(value) = nonempty(env, $name) {
                config.$field = value.to_owned();
                sources.insert(stringify!($field).to_owned(), "env".to_owned());
            }
        };
    }
    macro_rules! bool_env {
        ($field:ident, $name:literal) => {
            if let Some(value) = nonempty(env, $name) {
                config.$field = parse_go_bool($name, value)?;
                sources.insert(stringify!($field).to_owned(), "env".to_owned());
            }
        };
    }
    macro_rules! int_env {
        ($field:ident, $name:literal, $valid:expr) => {
            if let Some(value) = nonempty(env, $name) {
                let parsed = value
                    .parse()
                    .map_err(|_| ConfigError(format!("invalid {} {:?}", $name, value)))?;
                if !$valid(parsed) {
                    return Err(ConfigError(format!("invalid {} {:?}", $name, value)));
                }
                config.$field = parsed;
                sources.insert(stringify!($field).to_owned(), "env".to_owned());
            }
        };
    }
    string_env!(log_level, "SYMBROWSE_LOG_LEVEL");
    string_env!(log_format, "SYMBROWSE_LOG_FORMAT");
    string_env!(config_dir, "SYMBROWSE_CONFIG_DIR");
    string_env!(cache_dir, "SYMBROWSE_CACHE_DIR");
    string_env!(state_dir, "SYMBROWSE_STATE_DIR");
    string_env!(executable_path, "SYMBROWSE_EXECUTABLE_PATH");
    string_env!(cdp_endpoint, "SYMBROWSE_CDP_ENDPOINT");
    string_env!(engine, "SYMBROWSE_ENGINE");
    string_env!(mode, "SYMBROWSE_MODE");
    string_env!(fetch_user_agent, "SYMBROWSE_FETCH_USER_AGENT");
    string_env!(autosave, "SYMBROWSE_AUTOSAVE");
    string_env!(autosave_key, "SYMBROWSE_AUTOSAVE_KEY");
    string_env!(daemon_log, "SYMBROWSE_DAEMON_LOG");
    bool_env!(ssrf_enabled, "SYMBROWSE_SSRF");
    bool_env!(allow_private, "SYMBROWSE_ALLOW_PRIVATE");
    bool_env!(headless, "SYMBROWSE_HEADLESS");
    bool_env!(fetch_robots, "SYMBROWSE_FETCH_ROBOTS");
    bool_env!(fetch_no_cache, "SYMBROWSE_FETCH_NO_CACHE");
    int_env!(cache_ttl_hours, "SYMBROWSE_CACHE_TTL_HOURS", |_| true);
    int_env!(idle_timeout, "SYMBROWSE_IDLE_TIMEOUT", |value| value >= 0);
    int_env!(
        operation_timeout,
        "SYMBROWSE_OPERATION_TIMEOUT",
        |value| value > 0
    );
    int_env!(read_timeout, "SYMBROWSE_READ_TIMEOUT", |value| value > 0);
    int_env!(
        state_expire_days,
        "SYMBROWSE_STATE_EXPIRE_DAYS",
        |value| value >= 0
    );
    int_env!(
        autosave_interval,
        "SYMBROWSE_AUTOSAVE_INTERVAL",
        |value| value >= 0
    );
    int_env!(
        approval_timeout,
        "SYMBROWSE_APPROVAL_TIMEOUT",
        |value| value > 0
    );
    if let Some(value) = nonempty(env, "SYMBROWSE_ALLOWED_DOMAINS") {
        config.allowed_domains = split_list(value);
        sources.insert("allowed_domains".to_owned(), "env".to_owned());
    }
    if let Some(value) = nonempty(env, "SYMBROWSE_UPLOAD_DIRS") {
        config.upload_dirs = split_list(value);
        sources.insert("upload_dirs".to_owned(), "env".to_owned());
    }
    Ok(())
}

fn apply_flags(config: &mut Config, sources: &mut BTreeMap<String, String>, flags: &FlagOverrides) {
    macro_rules! flag {
        ($field:ident) => {
            if let Some(value) = &flags.$field {
                config.$field.clone_from(value);
                sources.insert(stringify!($field).to_owned(), "flag".to_owned());
            }
        };
    }
    flag!(log_level);
    flag!(log_format);
    flag!(config_dir);
    flag!(cache_dir);
    flag!(state_dir);
    flag!(executable_path);
    flag!(mode);
    flag!(engine);
}

fn validate(config: &Config) -> std::result::Result<(), ConfigError> {
    if config.idle_timeout < 0
        || config.operation_timeout <= 0
        || config.read_timeout <= 0
        || config.state_expire_days < 0
        || config.autosave_interval < 0
        || config.approval_timeout <= 0
    {
        return Err(ConfigError("timeout and retention values must be non-negative, with operation, read, and approval timeouts positive".to_owned()));
    }
    if !matches!(config.autosave.as_str(), "auto" | "always" | "never") {
        return Err(ConfigError(format!(
            "invalid autosave policy {:?}",
            config.autosave
        )));
    }
    if config.engine == "static" {
        return Ok(());
    }
    if !matches!(
        config.engine.as_str(),
        "" | "chrome" | "safari" | "safari-attach" | "safari-bidi" | "firefox"
    ) {
        return Err(ConfigError(format!(
            "invalid engine {:?}: use one of chrome, static, safari-attach, safari-bidi",
            config.engine
        )));
    }
    resolve_selection(Some(&config.mode), Some(&config.engine))
        .map_err(|error| ConfigError(format!("{}: {}", error.code, error.message)))?;
    Ok(())
}

fn default_mode() -> String {
    "browser".to_owned()
}

fn nonempty<'a>(env: &'a HashMap<String, String>, name: &str) -> Option<&'a str> {
    env.get(name)
        .map(String::as_str)
        .filter(|value| !value.is_empty())
}

fn parse_go_bool(name: &str, value: &str) -> std::result::Result<bool, ConfigError> {
    match value {
        "1" | "t" | "T" | "true" | "TRUE" | "True" => Ok(true),
        "0" | "f" | "F" | "false" | "FALSE" | "False" => Ok(false),
        _ => Err(ConfigError(format!(
            "invalid {name} {value:?}: parsing {value:?}: invalid syntax"
        ))),
    }
}

fn split_list(value: &str) -> Vec<String> {
    value
        .split(',')
        .map(str::trim)
        .filter(|item| !item.is_empty())
        .map(str::to_owned)
        .collect()
}

fn display(path: impl AsRef<Path>) -> String {
    path.as_ref().to_string_lossy().into_owned()
}

#[cfg(test)]
mod selection_tests {
    use super::*;

    #[test]
    fn selection_is_exhaustive_and_never_falls_back() {
        assert_eq!(
            resolve_selection(Some("static"), None).unwrap().mode,
            TransportMode::Static
        );
        assert_eq!(
            resolve_selection(Some("browser"), Some("firefox"))
                .unwrap()
                .engine,
            Some(BrowserEngine::Firefox)
        );
        assert_eq!(
            resolve_selection(Some("browser"), Some("safari"))
                .unwrap()
                .engine,
            Some(BrowserEngine::Safari)
        );
        assert_eq!(
            resolve_selection(Some("browser"), None).unwrap_err().code,
            "browser_engine_required"
        );
        assert_eq!(
            resolve_selection(Some("static"), Some("chrome"))
                .unwrap_err()
                .code,
            "engine_not_allowed"
        );
        assert_eq!(
            resolve_selection(Some("browser"), Some("wat"))
                .unwrap_err()
                .code,
            "invalid_browser_engine"
        );
    }
}
