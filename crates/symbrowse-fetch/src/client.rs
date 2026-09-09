use std::{
    collections::BTreeMap,
    error::Error,
    fmt,
    future::Future,
    pin::Pin,
    sync::{Arc, Mutex},
    time::Duration,
};

use symbrowse_core::policy::Allowlist;

use crate::{honest, rate_limit::HostRateLimiter, retry::BackoffConfig};

/// Honest is the only profile implemented by this transport slice.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Profile {
    Chrome,
    Firefox,
    Opera,
    Safari,
    Edge,
    Ios,
    Honest,
    Random,
}

impl Profile {
    #[must_use]
    pub fn parse(value: &str) -> Self {
        match value {
            "firefox" => Self::Firefox,
            "opera" => Self::Opera,
            "safari" => Self::Safari,
            "edge" => Self::Edge,
            "ios" => Self::Ios,
            "honest" => Self::Honest,
            "random" => Self::Random,
            "" | "chrome" => Self::Chrome,
            _ => Self::Chrome,
        }
    }

    #[must_use]
    pub const fn is_browser_profile(self) -> bool {
        !matches!(self, Self::Honest)
    }

    /// Returns the stable warning emitted for an unknown profile. The
    /// transport deliberately does not impersonate browser TLS; callers use
    /// the Go escape hatch for these profiles until FETCH-002 is ported.
    #[must_use]
    pub fn parse_warning(value: &str) -> Option<String> {
        let known = matches!(
            value,
            "" | "chrome" | "firefox" | "opera" | "safari" | "edge" | "ios" | "honest" | "random"
        );
        (!known).then(|| format!("unknown profile, defaulting to chrome: {value}"))
    }
}

/// A single HTTP operation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Request {
    pub url: String,
    pub method: String,
    pub headers: BTreeMap<String, String>,
    pub body: Vec<u8>,
    pub user_agent: Option<String>,
    pub timeout: Option<Duration>,
    pub proxy: Option<String>,
    pub session: Option<String>,
    pub max_body: Option<usize>,
    pub max_compressed_body: Option<usize>,
    pub allow_private: bool,
    pub allowlist: Option<Allowlist>,
}

impl Request {
    #[must_use]
    pub fn get(url: impl Into<String>) -> Self {
        Self {
            url: url.into(),
            ..Self::default()
        }
    }

    #[must_use]
    pub fn post(url: impl Into<String>, body: impl Into<Vec<u8>>) -> Self {
        Self {
            url: url.into(),
            method: "POST".to_owned(),
            body: body.into(),
            ..Self::default()
        }
    }
}

impl Default for Request {
    fn default() -> Self {
        Self {
            url: String::new(),
            method: "GET".to_owned(),
            headers: BTreeMap::new(),
            body: Vec::new(),
            user_agent: None,
            timeout: None,
            proxy: None,
            session: None,
            max_body: None,
            max_compressed_body: None,
            allow_private: false,
            allowlist: None,
        }
    }
}

/// The fetched response, with a UTF-8 normalized decoded body.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Response {
    pub final_url: String,
    pub status_code: u16,
    pub headers: BTreeMap<String, Vec<String>>,
    pub body: Vec<u8>,
    pub protocol: String,
    pub content_type: Option<String>,
    pub elapsed: Duration,
    pub from_cache: bool,
}

/// A bounded-body error. `compressed` identifies which limit was exceeded.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct BodyTooLarge {
    pub url: String,
    pub limit: usize,
    pub compressed: bool,
}

impl fmt::Display for BodyTooLarge {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            formatter,
            "too_large: response from {} exceeds {} {} bytes",
            self.url,
            self.limit,
            if self.compressed {
                "compressed"
            } else {
                "decompressed"
            }
        )
    }
}

impl Error for BodyTooLarge {}

/// Errors returned by the honest HTTP client.
#[derive(Debug)]
pub enum FetchError {
    UnsupportedProfile(Profile),
    InvalidProxy(String),
    BlockedDomain(String),
    BlockedPrivate(String),
    InvalidRequest(String),
    Request(reqwest::Error),
    Archive(String),
    BodyTooLarge(BodyTooLarge),
    BodyRead(String),
    Decode(String),
    UnsupportedEncoding(String),
    Timeout,
    CircuitOpen(String),
}

impl fmt::Display for FetchError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::UnsupportedProfile(profile) => {
                write!(formatter, "profile {profile:?} is not implemented")
            }
            Self::InvalidProxy(message) => write!(formatter, "invalid proxy: {message}"),
            Self::BlockedDomain(url) => write!(formatter, "blocked_domain: {url}"),
            Self::BlockedPrivate(url) => write!(formatter, "blocked_private: {url}"),
            Self::InvalidRequest(message) => write!(formatter, "invalid request: {message}"),
            Self::Request(error) => write!(formatter, "request failed: {error}"),
            Self::Archive(message) => write!(formatter, "archive request failed: {message}"),
            Self::BodyTooLarge(error) => error.fmt(formatter),
            Self::BodyRead(message) => write!(formatter, "read body: {message}"),
            Self::Decode(message) => write!(formatter, "decode body: {message}"),
            Self::UnsupportedEncoding(value) => {
                write!(formatter, "unsupported content encoding: {value}")
            }
            Self::Timeout => formatter.write_str("request timed out"),
            Self::CircuitOpen(host) => write!(formatter, "circuit breaker open for {host}"),
        }
    }
}

impl Error for FetchError {
    fn source(&self) -> Option<&(dyn Error + 'static)> {
        match self {
            Self::Request(error) => Some(error),
            Self::BodyTooLarge(error) => Some(error),
            _ => None,
        }
    }
}

/// Construction options for [`FetchClient`].
#[derive(Clone, Debug)]
pub struct ClientOptions {
    pub timeout: Duration,
    pub max_body: usize,
    pub max_compressed_body: usize,
    pub retry: bool,
    pub backoff: BackoffConfig,
    pub rate: crate::RateLimitConfig,
}

impl Default for ClientOptions {
    fn default() -> Self {
        Self {
            timeout: Duration::from_secs(30),
            max_body: 10 * 1024 * 1024,
            max_compressed_body: 10 * 1024 * 1024,
            retry: false,
            backoff: BackoffConfig::default(),
            rate: crate::RateLimitConfig::default(),
        }
    }
}

impl ClientOptions {
    #[must_use]
    pub fn timeout(mut self, timeout: Duration) -> Self {
        self.timeout = timeout;
        self
    }
    #[must_use]
    pub fn max_body(mut self, limit: usize) -> Self {
        self.max_body = limit;
        self
    }
    #[must_use]
    pub fn max_compressed_body(mut self, limit: usize) -> Self {
        self.max_compressed_body = limit;
        self
    }
    #[must_use]
    pub fn retry(mut self, enabled: bool) -> Self {
        self.retry = enabled;
        self
    }
    #[must_use]
    pub fn backoff(mut self, config: BackoffConfig) -> Self {
        self.backoff = config;
        self
    }
    #[must_use]
    pub fn rate(mut self, config: crate::RateLimitConfig) -> Self {
        self.rate = config;
        self
    }
}

/// Honest HTTP client with named cookie jars and fetch controls.
#[derive(Clone)]
pub struct FetchClient {
    pub(crate) options: ClientOptions,
    pub(crate) limiter: Arc<HostRateLimiter>,
    pub(crate) sessions: Arc<Mutex<std::collections::HashMap<String, Arc<reqwest::cookie::Jar>>>>,
    pub(crate) ssrf: symbrowse_core::policy::SsrfGuard,
    pub(crate) resolver: Option<honest::PinnedResolver>,
    http_clients: Arc<Mutex<std::collections::HashMap<String, reqwest::Client>>>,
}

impl fmt::Debug for FetchClient {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("FetchClient")
            .field("options", &self.options)
            .finish_non_exhaustive()
    }
}

impl FetchClient {
    pub fn new(profile: Profile, options: ClientOptions) -> Result<Self, FetchError> {
        if profile != Profile::Honest {
            return Err(FetchError::UnsupportedProfile(profile));
        }
        let resolver = honest::PinnedResolver::system();
        let policy_resolver = resolver.clone();
        Ok(Self {
            limiter: Arc::new(HostRateLimiter::with_rate(
                crate::CircuitBreakerConfig::default(),
                options.rate,
            )),
            options,
            sessions: Arc::new(Mutex::new(std::collections::HashMap::new())),
            ssrf: symbrowse_core::policy::SsrfGuard::with_lookup(false, move |host| {
                policy_resolver.resolve_for_policy(host).map(|addresses| {
                    addresses
                        .into_iter()
                        .map(|address| address.ip().to_string())
                        .collect()
                })
            }),
            resolver: Some(resolver),
            http_clients: Arc::new(Mutex::new(std::collections::HashMap::new())),
        })
    }

    pub fn honest() -> Result<Self, FetchError> {
        Self::new(Profile::Honest, ClientOptions::default())
    }

    #[must_use]
    pub fn with_ssrf_guard(mut self, guard: symbrowse_core::policy::SsrfGuard) -> Self {
        self.ssrf = guard;
        self
    }

    #[must_use]
    pub fn with_resolver(mut self, resolver: honest::PinnedResolver) -> Self {
        let policy_resolver = resolver.clone();
        self.ssrf = symbrowse_core::policy::SsrfGuard::with_lookup(false, move |host| {
            policy_resolver.resolve_for_policy(host).map(|addresses| {
                addresses
                    .into_iter()
                    .map(|address| address.ip().to_string())
                    .collect()
            })
        });
        self.resolver = Some(resolver);
        self
    }

    pub(crate) fn jar(&self, session: Option<&str>) -> Arc<reqwest::cookie::Jar> {
        let Some(session) = session.filter(|value| !value.is_empty()) else {
            return Arc::new(reqwest::cookie::Jar::default());
        };
        let mut sessions = self.sessions.lock().expect("session mutex poisoned");
        Arc::clone(
            sessions
                .entry(session.to_owned())
                .or_insert_with(|| Arc::new(reqwest::cookie::Jar::default())),
        )
    }

    pub(crate) fn http_client(
        &self,
        request: &Request,
        guard: symbrowse_core::policy::SsrfGuard,
    ) -> Result<reqwest::Client, FetchError> {
        let key = format!(
            "session={};proxy={};private={};allowlist={:?}",
            request.session.as_deref().unwrap_or_default(),
            request.proxy.as_deref().unwrap_or_default(),
            request.allow_private,
            request.allowlist
        );
        if let Some(cached) = self
            .http_clients
            .lock()
            .expect("HTTP client mutex poisoned")
            .get(&key)
            .cloned()
        {
            return Ok(cached);
        }
        let built = honest::build_http_client(
            self.jar(request.session.as_deref()),
            request.proxy.as_deref(),
            guard,
            request.allowlist.clone(),
            request.allow_private,
            self.resolver.clone(),
        )?;
        self.http_clients
            .lock()
            .expect("HTTP client mutex poisoned")
            .insert(key, built.clone());
        Ok(built)
    }

    pub fn close(&self) {
        self.sessions
            .lock()
            .expect("session mutex poisoned")
            .clear();
        self.http_clients
            .lock()
            .expect("HTTP client mutex poisoned")
            .clear();
    }

    /// Apply the current request allowlist and SSRF policy without performing I/O.
    pub fn validate_request_policy(&self, request: &Request) -> Result<(), FetchError> {
        let parsed = url::Url::parse(&request.url)
            .map_err(|error| FetchError::InvalidRequest(format!("URL: {error}")))?;
        if !matches!(parsed.scheme(), "http" | "https") || parsed.host_str().is_none() {
            return Err(FetchError::InvalidRequest(
                "only HTTP(S) URLs with a host are supported".to_owned(),
            ));
        }
        if request
            .allowlist
            .as_ref()
            .is_some_and(|list| !list.allows_url(&request.url))
        {
            return Err(FetchError::BlockedDomain(request.url.clone()));
        }
        let guard = if request.allow_private {
            symbrowse_core::policy::SsrfGuard::new(true)
        } else {
            self.ssrf.clone()
        };
        guard
            .allows_url(&request.url)
            .map_err(|error| FetchError::BlockedPrivate(error.to_string()))
    }

    pub async fn fetch(&self, request: Request) -> Result<Response, FetchError> {
        honest::fetch(self, request).await
    }

    pub fn fetch_blocking(&self, request: Request) -> Result<Response, FetchError> {
        tokio::runtime::Handle::try_current().map_or_else(
            |_| {
                tokio::runtime::Runtime::new()
                    .expect("tokio runtime")
                    .block_on(self.fetch(request))
            },
            |_| {
                Err(FetchError::InvalidRequest(
                    "fetch_blocking cannot run inside a Tokio runtime".to_owned(),
                ))
            },
        )
    }
}

/// Object-safe async transport boundary for callers that do not need internals.
pub trait Client: Send + Sync {
    fn fetch<'a>(
        &'a self,
        request: Request,
    ) -> Pin<Box<dyn Future<Output = Result<Response, FetchError>> + Send + 'a>>;
    fn close(&self);
}

impl Client for FetchClient {
    fn fetch<'a>(
        &'a self,
        request: Request,
    ) -> Pin<Box<dyn Future<Output = Result<Response, FetchError>> + Send + 'a>> {
        Box::pin(FetchClient::fetch(self, request))
    }

    fn close(&self) {
        FetchClient::close(self);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_honest_client_shares_a_pinned_resolver_with_policy() {
        let client = FetchClient::honest().unwrap();
        assert!(client.resolver.is_some());
    }
}
