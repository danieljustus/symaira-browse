#![deny(unsafe_code)]

//! Protocol-neutral network and action policy contracts.
//!
//! This module mirrors the deterministic parts of `internal/policy` in the Go
//! implementation. DNS lookup is injectable at the decision boundary so
//! policy tests never depend on the host network.

use std::{
    collections::BTreeMap,
    fmt, fs,
    net::{IpAddr, Ipv4Addr, Ipv6Addr, ToSocketAddrs},
    path::Path,
    sync::Arc,
};

use serde::Deserialize;

const ALLOWED_SCHEMES: [&str; 4] = ["http", "https", "ws", "wss"];

/// A validated, deny-by-default domain allowlist.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Allowlist {
    active: bool,
    exact: BTreeMap<String, ()>,
    suffixes: Vec<String>,
    patterns: Vec<String>,
}

/// Error returned for malformed allowlist patterns.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AllowlistError(String);

impl fmt::Display for AllowlistError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl std::error::Error for AllowlistError {}

impl Allowlist {
    /// Builds an allowlist from bare hostnames and `*.` wildcard patterns.
    pub fn parse(patterns: &[String]) -> Result<Self, AllowlistError> {
        let mut allowlist = Self {
            active: false,
            exact: BTreeMap::new(),
            suffixes: Vec::with_capacity(patterns.len()),
            patterns: Vec::with_capacity(patterns.len()),
        };
        for raw in patterns {
            let pattern = raw.trim();
            if pattern.is_empty() {
                continue;
            }
            let (normalized, wildcard) = normalize_pattern(pattern)?;
            allowlist.patterns.push(pattern.to_owned());
            if wildcard {
                allowlist.suffixes.push(normalized);
            } else {
                allowlist.exact.insert(normalized, ());
            }
        }
        allowlist.active = !allowlist.patterns.is_empty();
        Ok(allowlist)
    }

    /// Alias matching the Go constructor name used by port documentation.
    pub fn parse_allowlist(patterns: &[String]) -> Result<Self, AllowlistError> {
        Self::parse(patterns)
    }

    /// Returns whether this policy has at least one configured pattern.
    #[must_use]
    pub const fn active(&self) -> bool {
        self.active
    }

    /// Returns the original non-blank patterns in supplied order.
    #[must_use]
    pub fn patterns(&self) -> &[String] {
        &self.patterns
    }

    /// Applies the allowlist to a URL-like string.
    #[must_use]
    pub fn allows_url(&self, raw_url: &str) -> bool {
        if !self.active {
            return true;
        }
        let Some((scheme, host)) = url_scheme_host(raw_url) else {
            return false;
        };
        ALLOWED_SCHEMES.contains(&scheme.as_str()) && self.allows_host(&host)
    }

    /// Applies the allowlist to a hostname.
    #[must_use]
    pub fn allows_host(&self, hostname: &str) -> bool {
        if !self.active {
            return true;
        }
        let host = normalize_host(hostname);
        if host.is_empty() {
            return false;
        }
        if self.exact.contains_key(&host) {
            return true;
        }
        self.suffixes
            .iter()
            .any(|suffix| host == suffix[1..] || host.ends_with(suffix))
    }
}

/// Constructs an [`Allowlist`].
pub fn parse_allowlist(patterns: &[String]) -> Result<Allowlist, AllowlistError> {
    Allowlist::parse(patterns)
}

fn normalize_pattern(pattern: &str) -> Result<(String, bool), AllowlistError> {
    let lower = pattern.to_lowercase();
    if ["http://", "https://", "ws://", "wss://"]
        .iter()
        .any(|prefix| lower.starts_with(prefix))
    {
        return Err(AllowlistError(format!(
            "invalid allowlist pattern {pattern:?}: scheme prefixes are not allowed; pass a bare hostname"
        )));
    }
    let (wildcard, host) = if let Some(host) = lower.strip_prefix("*.") {
        (true, host)
    } else {
        if lower.contains('*') {
            return Err(AllowlistError(format!(
                "invalid allowlist pattern {pattern:?}: only a leading \"*.\" wildcard is supported"
            )));
        }
        (false, lower.as_str())
    };
    let host = host.strip_suffix('.').unwrap_or(host);
    if host.is_empty() {
        return Err(AllowlistError(format!(
            "invalid allowlist pattern {pattern:?}: empty host"
        )));
    }
    if host.chars().any(|character| "/:@?#".contains(character)) || host.contains("..") {
        return Err(AllowlistError(format!(
            "invalid allowlist pattern {pattern:?}: patterns must be bare hostnames without scheme, port, path, or userinfo"
        )));
    }
    if host.split('.').any(str::is_empty) {
        return Err(AllowlistError(format!(
            "invalid allowlist pattern {pattern:?}: empty host label"
        )));
    }
    Ok((
        if wildcard {
            format!(".{host}")
        } else {
            host.to_owned()
        },
        wildcard,
    ))
}

fn normalize_host(hostname: &str) -> String {
    let lowered = hostname.trim().to_lowercase();
    lowered.strip_suffix('.').unwrap_or(&lowered).to_owned()
}

/// Extracts the scheme and authority hostname with the same web-origin rules
/// used by the Go allowlist. Non-authority schemes such as `file:` are denied.
fn url_scheme_host(raw_url: &str) -> Option<(String, String)> {
    if raw_url != raw_url.trim() || raw_url.contains('\\') {
        return None;
    }
    let parsed = url::Url::parse(raw_url).ok()?;
    let scheme = parsed.scheme().to_lowercase();
    let colon = raw_url.find(':')?;
    let authority = raw_url[colon + 1..].strip_prefix("//")?;
    let authority = authority.split(['/', '?', '#']).next().unwrap_or_default();
    let host_port = authority.rsplit('@').next().unwrap_or_default();
    let host = if let Some(bracketed) = host_port.strip_prefix('[') {
        bracketed.split_once(']')?.0
    } else {
        host_port
            .rsplit_once(':')
            .map_or(host_port, |(host, _)| host)
    };
    let parsed_host = parsed.host_str()?;
    if host.is_empty() || parsed_host.is_empty() {
        return None;
    }
    let host = normalize_host(host);
    (!host.is_empty()).then_some((scheme, host))
}

/// An SSRF policy error.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum SsrfError {
    /// The raw URL cannot be parsed with the stricter Go-oracle syntax.
    InvalidUrl { message: String },
    /// The URL targets a private or otherwise non-public address.
    BlockedPrivate { url: String },
    /// Name resolution failed and the guard therefore denied the request.
    ResolutionFailed { host: String, message: String },
}

impl fmt::Display for SsrfError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidUrl { message } => write!(formatter, "invalid URL: {message}"),
            Self::BlockedPrivate { url } => write!(
                formatter,
                "blocked_private: {url} targets a private or loopback address"
            ),
            Self::ResolutionFailed { host, message } => {
                write!(formatter, "DNS resolution failed for {host}: {message}")
            }
        }
    }
}

impl std::error::Error for SsrfError {}

/// Host lookup function used by [`SsrfGuard`]. Invalid returned address text is
/// ignored, matching the Go implementation; resolution failures fail closed.
pub type LookupFn = dyn Fn(&str) -> Result<Vec<String>, String> + Send + Sync + 'static;

/// DNS/IP SSRF guard. It is enabled by default and can be explicitly relaxed
/// only through `allow_private`.
#[derive(Clone)]
pub struct SsrfGuard {
    enabled: bool,
    allow_private: bool,
    lookup: Arc<LookupFn>,
}

impl fmt::Debug for SsrfGuard {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("SsrfGuard")
            .field("enabled", &self.enabled)
            .field("allow_private", &self.allow_private)
            .finish_non_exhaustive()
    }
}

impl SsrfGuard {
    /// Creates an enabled guard using the system resolver.
    #[must_use]
    pub fn new(allow_private: bool) -> Self {
        Self::with_lookup(allow_private, default_lookup)
    }

    /// Creates an enabled guard with a deterministic lookup function.
    #[must_use]
    pub fn with_lookup<F>(allow_private: bool, lookup: F) -> Self
    where
        F: Fn(&str) -> Result<Vec<String>, String> + Send + Sync + 'static,
    {
        Self {
            enabled: true,
            allow_private,
            lookup: Arc::new(lookup),
        }
    }

    /// Creates an inactive guard. This is useful for explicit daemon opt-in
    /// modes while retaining a single policy implementation.
    #[must_use]
    pub fn disabled() -> Self {
        Self {
            enabled: false,
            allow_private: false,
            lookup: Arc::new(default_lookup),
        }
    }

    #[must_use]
    pub const fn enabled(&self) -> bool {
        self.enabled
    }

    /// Checks an HTTP(S) URL and fails closed on malformed/non-web input.
    pub fn allows_url(&self, raw_url: &str) -> Result<(), SsrfError> {
        if !self.enabled || self.allow_private {
            return Ok(());
        }
        if raw_url != raw_url.trim() || raw_url.contains('\\') || url::Url::parse(raw_url).is_err()
        {
            return Err(SsrfError::InvalidUrl {
                message: "malformed URL".to_owned(),
            });
        }
        let Some((scheme, host)) = url_scheme_host(raw_url) else {
            return Err(SsrfError::BlockedPrivate {
                url: raw_url.to_owned(),
            });
        };
        if scheme != "http" && scheme != "https" {
            return Err(SsrfError::BlockedPrivate {
                url: raw_url.to_owned(),
            });
        }
        self.allows_host(&host, raw_url)
    }

    /// Checks a hostname, retaining `raw_url` for stable error reporting.
    pub fn allows_host(&self, hostname: &str, raw_url: &str) -> Result<(), SsrfError> {
        if !self.enabled || self.allow_private {
            return Ok(());
        }
        let host = normalize_host(hostname);
        if host.is_empty() || host == "localhost" || host.ends_with(".local") {
            return Err(SsrfError::BlockedPrivate {
                url: raw_url.to_owned(),
            });
        }
        let addresses = (self.lookup)(&host).map_err(|message| SsrfError::ResolutionFailed {
            host: host.clone(),
            message,
        })?;
        for address in addresses {
            match address.parse::<IpAddr>() {
                Ok(ip) if is_private_ip(ip) => {
                    return Err(SsrfError::BlockedPrivate {
                        url: raw_url.to_owned(),
                    });
                }
                _ => {}
            }
        }
        Ok(())
    }
}

/// Alias with the acronym spelling used in the Go policy documentation.
#[allow(clippy::upper_case_acronyms)]
pub type SSRFGuard = SsrfGuard;

fn default_lookup(host: &str) -> Result<Vec<String>, String> {
    if host.parse::<IpAddr>().is_ok() {
        return Ok(vec![host.to_owned()]);
    }
    (host, 0)
        .to_socket_addrs()
        .map(|addresses| addresses.map(|address| address.ip().to_string()).collect())
        .map_err(|error| error.to_string())
}

/// Returns whether an address belongs to a blocked SSRF range.
#[must_use]
pub fn is_private_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(address) => private_v4(address),
        IpAddr::V6(address) => {
            address.to_ipv4_mapped().is_some_and(private_v4)
                || address == Ipv6Addr::UNSPECIFIED
                || address == Ipv6Addr::LOCALHOST
                || (address.segments()[0] & 0xfe00) == 0xfc00
                || (address.segments()[0] & 0xffc0) == 0xfe80
        }
    }
}

/// Alias matching the Go helper's exported name.
#[must_use]
pub fn is_private_ip_addr(ip: IpAddr) -> bool {
    is_private_ip(ip)
}

fn private_v4(address: Ipv4Addr) -> bool {
    let octets = address.octets();
    octets[0] == 0
        || octets[0] == 127
        || octets[0] == 10
        || (octets[0] == 172 && (16..=31).contains(&octets[1]))
        || (octets[0] == 192 && octets[1] == 168)
        || (octets[0] == 169 && octets[1] == 254)
        || (octets[0] == 100 && (64..=127).contains(&octets[1]))
}

/// Checks a URL using an injected resolver without constructing a guard.
pub fn check_ssrf_with_lookup<F>(raw_url: &str, lookup: F) -> Result<(), SsrfError>
where
    F: Fn(&str) -> Result<Vec<String>, String> + Send + Sync + 'static,
{
    SsrfGuard::with_lookup(false, lookup).allows_url(raw_url)
}

/// Fixed risk classes shared by command classification and policy defaults.
#[derive(Clone, Copy, Debug, Eq, Hash, Ord, PartialEq, PartialOrd)]
pub enum RiskClass {
    Read,
    Navigate,
    Interact,
    Submit,
    Eval,
    Credential,
    Download,
    Upload,
    NetworkMock,
}

impl RiskClass {
    pub const ALL: [Self; 9] = [
        Self::Read,
        Self::Navigate,
        Self::Interact,
        Self::Submit,
        Self::Eval,
        Self::Credential,
        Self::Download,
        Self::Upload,
        Self::NetworkMock,
    ];

    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Read => "read",
            Self::Navigate => "navigate",
            Self::Interact => "interact",
            Self::Submit => "submit",
            Self::Eval => "eval",
            Self::Credential => "credential",
            Self::Download => "download",
            Self::Upload => "upload",
            Self::NetworkMock => "network-mock",
        }
    }
}

impl fmt::Display for RiskClass {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

/// Effective action decision.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Decision {
    Allow,
    Confirm,
    Deny,
}

impl Decision {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Allow => "allow",
            Self::Confirm => "confirm",
            Self::Deny => "deny",
        }
    }
}

impl fmt::Display for Decision {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

/// Policy evaluation mode.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Mode {
    Mcp,
    Tty,
}

impl Mode {
    #[must_use]
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Mcp => "mcp",
            Self::Tty => "tty",
        }
    }
}

impl fmt::Display for Mode {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

/// Looks up the fixed command classification table.
pub fn classify(command: &str) -> Result<RiskClass, PolicyError> {
    let class = match command {
        "snapshot" | "screenshot" | "a11y" | "read" | "console.list" | "console.clear"
        | "errors.list" | "errors.clear" | "get.text" | "get.html" | "get.value" | "get.attr"
        | "get.title" | "get.url" | "get.count" | "get.box" | "get.styles" | "is.visible"
        | "is.enabled" | "is.checked" | "find" | "session.list" | "session.info"
        | "daemon.status" | "state.list" | "state.show" | "journal.tail" | "journal.show"
        | "policy.explain" | "oob.status" | "cookies.list" | "storage.list" | "trace.replay"
        | "watch" | "cache.get" | "fetch.url" | "fetch.batch" | "wayback.snapshots"
        | "downloads.list" | "network.requests" | "network.request" | "network.har" => {
            RiskClass::Read
        }
        "open" | "goto" | "back" | "forward" | "reload" => RiskClass::Navigate,
        "click" | "dblclick" | "fill" | "type" | "press" | "hover" | "focus" | "select"
        | "check" | "uncheck" | "scroll" | "scrollintoview" | "wait" | "state.save"
        | "state.load" | "state.clear" | "state.clean" | "cookies.set" | "cookies.clear"
        | "storage.set" | "storage.clear" | "set.viewport" | "set.device" | "set.geo"
        | "set.media" | "set.user-agent" => RiskClass::Interact,
        "submit" => RiskClass::Submit,
        "eval" => RiskClass::Eval,
        "auth.login" => RiskClass::Credential,
        "download" | "download.setdir" => RiskClass::Download,
        "upload" => RiskClass::Upload,
        "network.route" | "network.unroute" | "set.headers" | "set.offline" => {
            RiskClass::NetworkMock
        }
        _ => {
            return Err(PolicyError::UnknownCommand(command.to_owned()));
        }
    };
    Ok(class)
}

/// Returns a conservative unknown marker for callers preparing risk metadata.
#[must_use]
pub fn class_for_command(command: &str) -> Option<RiskClass> {
    classify(command).ok()
}

/// Returns the built-in mode-specific default.
#[must_use]
pub const fn defaults(class: RiskClass, mode: Mode) -> Decision {
    match mode {
        Mode::Mcp => match class {
            RiskClass::Submit
            | RiskClass::Eval
            | RiskClass::Credential
            | RiskClass::Download
            | RiskClass::Upload => Decision::Confirm,
            RiskClass::NetworkMock => Decision::Deny,
            _ => Decision::Allow,
        },
        Mode::Tty => match class {
            RiskClass::Eval
            | RiskClass::Credential
            | RiskClass::Download
            | RiskClass::Upload
            | RiskClass::NetworkMock => Decision::Confirm,
            _ => Decision::Allow,
        },
    }
}

/// One explicit policy.toml rule.
#[derive(Clone, Debug, Eq, PartialEq, Deserialize)]
pub struct Rule {
    pub class: String,
    pub domain: String,
    pub decision: String,
}

/// Loaded explicit rules plus mode defaults.
#[derive(Clone, Debug, Eq, PartialEq, Default)]
pub struct Policy {
    pub rules: Vec<Rule>,
    pub source: String,
}

/// Policy errors, including fail-closed malformed configuration.
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum PolicyError {
    UnknownCommand(String),
    InvalidRule(String),
    Load(String),
}

impl fmt::Display for PolicyError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::UnknownCommand(command) => {
                write!(formatter, "command {command:?} has no risk classification")
            }
            Self::InvalidRule(message) | Self::Load(message) => formatter.write_str(message),
        }
    }
}

impl std::error::Error for PolicyError {}

impl Policy {
    /// Loads policy.toml. A missing file produces built-in defaults.
    pub fn load(path: impl AsRef<Path>) -> Result<Self, PolicyError> {
        let path = path.as_ref();
        let source = path.display().to_string();
        let content = match fs::read_to_string(path) {
            Ok(content) => content,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                return Ok(Self {
                    rules: Vec::new(),
                    source,
                });
            }
            Err(error) => {
                return Err(PolicyError::Load(format!("load policy {source}: {error}")));
            }
        };
        let parsed: PolicyFile = toml::from_str(&content)
            .map_err(|error| PolicyError::Load(format!("load policy {source}: {error}")))?;
        let policy = Self {
            rules: parsed.rules,
            source,
        };
        policy.validate()?;
        Ok(policy)
    }

    /// Checks every explicit rule's class, domain, and decision.
    pub fn validate(&self) -> Result<(), PolicyError> {
        for rule in &self.rules {
            let class = parse_class(&rule.class).ok_or_else(|| {
                PolicyError::InvalidRule(format!(
                    "policy rule references unknown risk class {:?}",
                    rule.class
                ))
            })?;
            let _ = class;
            if rule.domain.is_empty() {
                return Err(PolicyError::InvalidRule(
                    "policy rule with empty domain".to_owned(),
                ));
            }
            if !matches!(rule.decision.as_str(), "allow" | "confirm" | "deny") {
                return Err(PolicyError::InvalidRule(format!(
                    "policy rule has invalid decision {:?}",
                    rule.decision
                )));
            }
        }
        Ok(())
    }

    /// Resolves the longest matching explicit domain rule or a mode default.
    #[must_use]
    pub fn decide(&self, class: RiskClass, host: &str, mode: Mode) -> (Decision, String) {
        let host = host.trim().to_lowercase();
        let mut best: Option<&Rule> = None;
        for rule in &self.rules {
            if parse_class(&rule.class) != Some(class) || !domain_matches(&rule.domain, &host) {
                continue;
            }
            if best.is_none_or(|current| rule.domain.len() > current.domain.len()) {
                best = Some(rule);
            }
        }
        best.map_or_else(
            || (defaults(class, mode), "default".to_owned()),
            |rule| {
                (
                    parse_decision(&rule.decision).expect("validated policy rule"),
                    format!("rule:{}", rule.domain),
                )
            },
        )
    }

    /// Produces the stable human-readable explanation used by policy explain.
    pub fn explain(&self, command: &str, url: &str, mode: Mode) -> Result<String, PolicyError> {
        let class = classify(command)?;
        let host = host_of(url);
        let (decision, origin) = self.decide(class, &host, mode);
        Ok(format!(
            "command:  {command}\nclass:    {class}\nurl:      {url}\nhost:     {host}\nmode:     {mode}\ndecision: {decision}\norigin:   {origin}"
        ))
    }
}

#[derive(Debug, Deserialize)]
struct PolicyFile {
    #[serde(default)]
    rules: Vec<Rule>,
}

fn parse_class(value: &str) -> Option<RiskClass> {
    RiskClass::ALL
        .into_iter()
        .find(|class| class.as_str() == value)
}

fn parse_decision(value: &str) -> Option<Decision> {
    match value {
        "allow" => Some(Decision::Allow),
        "confirm" => Some(Decision::Confirm),
        "deny" => Some(Decision::Deny),
        _ => None,
    }
}

fn domain_matches(rule_domain: &str, host: &str) -> bool {
    let rule_domain = rule_domain.trim().to_lowercase();
    if rule_domain.is_empty() || host.is_empty() {
        return false;
    }
    rule_domain == host
        || if let Some(suffix) = rule_domain.strip_prefix('.') {
            host.ends_with(&rule_domain) && host != suffix
        } else {
            host.ends_with(&format!(".{rule_domain}"))
        }
}

fn host_of(url: &str) -> String {
    let mut rest = url.trim().to_owned();
    if let Some(index) = rest.find("://") {
        rest = rest[index + 3..].to_owned();
    }
    if let Some(index) = rest.find(['/', '?', '#']) {
        rest.truncate(index);
    }
    if let Some(index) = rest.find('@') {
        rest = rest[index + 1..].to_owned();
    }
    if let Some(index) = rest.find(':') {
        rest.truncate(index);
    }
    rest
}

/// Extracts a URL host using the same tolerant helper as policy explanations.
#[must_use]
pub fn policy_host(url: &str) -> String {
    host_of(url)
}

/// Returns all classified commands in lexical order.
#[must_use]
pub fn sorted_commands() -> Vec<&'static str> {
    let mut commands = vec![
        "a11y",
        "auth.login",
        "back",
        "cache.get",
        "check",
        "click",
        "console.clear",
        "console.list",
        "cookies.clear",
        "cookies.list",
        "cookies.set",
        "daemon.status",
        "dblclick",
        "download",
        "download.setdir",
        "downloads.list",
        "errors.clear",
        "errors.list",
        "eval",
        "fetch.batch",
        "fetch.url",
        "fill",
        "find",
        "focus",
        "forward",
        "get.attr",
        "get.box",
        "get.count",
        "get.html",
        "get.styles",
        "get.text",
        "get.title",
        "get.url",
        "get.value",
        "goto",
        "hover",
        "is.checked",
        "is.enabled",
        "is.visible",
        "journal.show",
        "journal.tail",
        "network.har",
        "network.request",
        "network.requests",
        "network.route",
        "network.unroute",
        "oob.status",
        "open",
        "policy.explain",
        "press",
        "read",
        "reload",
        "screenshot",
        "scroll",
        "scrollintoview",
        "select",
        "session.info",
        "session.list",
        "set.device",
        "set.geo",
        "set.headers",
        "set.media",
        "set.offline",
        "set.user-agent",
        "set.viewport",
        "snapshot",
        "state.clear",
        "state.clean",
        "state.list",
        "state.load",
        "state.save",
        "state.show",
        "storage.clear",
        "storage.list",
        "storage.set",
        "submit",
        "trace.replay",
        "type",
        "uncheck",
        "upload",
        "wait",
        "watch",
        "wayback.snapshots",
    ];
    commands.sort_unstable();
    commands
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mapped_ipv4_is_classified_as_private() {
        assert!(is_private_ip("::ffff:127.0.0.1".parse().unwrap()));
        assert!(!is_private_ip("::ffff:8.8.8.8".parse().unwrap()));
    }

    #[test]
    fn wildcard_is_boundary_safe() {
        let list = Allowlist::parse(&["*.example.com".to_owned()]).unwrap();
        assert!(list.allows_host("www.example.com"));
        assert!(!list.allows_host("notexample.com"));
    }
}
