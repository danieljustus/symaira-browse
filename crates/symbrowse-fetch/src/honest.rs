use std::{
    collections::HashMap,
    io::{self, Cursor, Read},
    net::{SocketAddr, ToSocketAddrs},
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use brotli::Decompressor;
use encoding_rs::Encoding;
use flate2::read::GzDecoder;
use futures_util::StreamExt;
use reqwest::{
    Client as HttpClient, Method, Response as HttpResponse,
    cookie::Jar,
    dns::{Addrs, Name, Resolve, Resolving},
    redirect::{Attempt, Policy},
};
use symbrowse_core::policy::{Allowlist, SsrfGuard, is_private_ip};
use tokio::time::timeout;

use crate::{
    client::{BodyTooLarge, FetchClient, FetchError, Request, Response},
    retry::{is_transient_status, parse_retry_after},
};

const DEFAULT_USER_AGENT: &str = "symfetch/0.1 (+https://github.com/danieljustus/symaira-fetch)";
const MAX_REDIRECTS: usize = 10;

type ResolverLookup = dyn Fn(&str) -> Result<Vec<SocketAddr>, String> + Send + Sync;

/// DNS resolver that pins one validated address set for each host for the
/// lifetime of the HTTP client. This keeps policy validation and every
/// redirect hop on the same resolution result instead of permitting a second
/// lookup to rebind a host between the check and the connection.
#[derive(Clone)]
pub struct PinnedResolver {
    lookup: Arc<ResolverLookup>,
    pinned: Arc<Mutex<HashMap<String, Vec<SocketAddr>>>>,
}

impl std::fmt::Debug for PinnedResolver {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("PinnedResolver")
            .field(
                "pinned_hosts",
                &self.pinned.lock().map(|v| v.len()).unwrap_or(0),
            )
            .finish_non_exhaustive()
    }
}

impl PinnedResolver {
    #[must_use]
    pub fn system() -> Self {
        Self::with_lookup(|host| {
            (host, 0)
                .to_socket_addrs()
                .map(|addresses| addresses.collect())
                .map_err(|error| error.to_string())
        })
    }

    #[must_use]
    pub fn with_lookup<F>(lookup: F) -> Self
    where
        F: Fn(&str) -> Result<Vec<SocketAddr>, String> + Send + Sync + 'static,
    {
        Self {
            lookup: Arc::new(lookup),
            pinned: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    pub(crate) fn resolve_for_policy(&self, host: &str) -> Result<Vec<SocketAddr>, String> {
        self.resolve_once(host).map_err(|error| error.to_string())
    }

    fn resolve_once(&self, host: &str) -> Result<Vec<SocketAddr>, io::Error> {
        if host.eq_ignore_ascii_case("localhost") || host.ends_with(".local") {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "private hostname",
            ));
        }
        if let Some(addresses) = self
            .pinned
            .lock()
            .map_err(|_| io::Error::other("resolver mutex poisoned"))?
            .get(host)
            .cloned()
        {
            return Ok(addresses);
        }
        let addresses = (self.lookup)(host).map_err(io::Error::other)?;
        if addresses.is_empty() {
            return Err(io::Error::new(
                io::ErrorKind::AddrNotAvailable,
                "no resolved addresses",
            ));
        }
        if addresses.iter().any(|address| is_private_ip(address.ip())) {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "private resolved address",
            ));
        }
        self.pinned
            .lock()
            .map_err(|_| io::Error::other("resolver mutex poisoned"))?
            .insert(host.to_owned(), addresses.clone());
        Ok(addresses)
    }
}

impl Resolve for PinnedResolver {
    fn resolve(&self, name: Name) -> Resolving {
        let resolver = self.clone();
        let host = name.as_str().to_owned();
        Box::pin(async move {
            let addresses = resolver.resolve_once(&host)?;
            let addrs: Addrs = Box::new(addresses.into_iter());
            Ok(addrs)
        })
    }
}

fn policy_error(
    attempt: Attempt<'_>,
    guard: &SsrfGuard,
    allowlist: &Option<Allowlist>,
) -> reqwest::redirect::Action {
    if attempt.previous().len() >= MAX_REDIRECTS {
        return attempt.error("too many redirects");
    }
    let url = attempt.url().as_str();
    if allowlist.as_ref().is_some_and(|list| !list.allows_url(url)) {
        return attempt.error("blocked_domain: redirect target is not allowlisted");
    }
    if let Err(error) = guard.allows_url(url) {
        return attempt.error(error.to_string());
    }
    attempt.follow()
}

pub(crate) fn build_http_client(
    jar: Arc<Jar>,
    proxy: Option<&str>,
    guard: SsrfGuard,
    allowlist: Option<Allowlist>,
    allow_private: bool,
    resolver: Option<PinnedResolver>,
) -> Result<HttpClient, FetchError> {
    let redirect_guard = guard.clone();
    let redirect_allowlist = allowlist.clone();
    let guard_enabled = guard.enabled();
    let mut builder = HttpClient::builder()
        .cookie_provider(jar)
        .redirect(Policy::custom(move |attempt| {
            policy_error(attempt, &redirect_guard, &redirect_allowlist)
        }))
        .user_agent(DEFAULT_USER_AGENT)
        .danger_accept_invalid_certs(false);

    if let Some(proxy) = proxy {
        let parsed = reqwest::Proxy::all(proxy)
            .map_err(|error| FetchError::InvalidProxy(error.to_string()))?;
        builder = builder.proxy(parsed);
    }
    if !allow_private && guard_enabled {
        builder = builder.dns_resolver(resolver.unwrap_or_else(PinnedResolver::system));
    }
    builder.build().map_err(FetchError::Request)
}

pub(crate) async fn fetch(client: &FetchClient, request: Request) -> Result<Response, FetchError> {
    let parsed = url::Url::parse(&request.url)
        .map_err(|error| FetchError::InvalidRequest(format!("URL: {error}")))?;
    client.validate_request_policy(&request)?;
    let guard = if request.allow_private {
        SsrfGuard::new(true)
    } else {
        client.ssrf.clone()
    };

    let _permit = client.limiter.acquire(&request.url).await;
    let host = request.url.clone();

    let method = Method::from_bytes(request.method.as_bytes())
        .map_err(|error| FetchError::InvalidRequest(format!("method: {error}")))?;
    let timeout_duration = request.timeout.unwrap_or(client.options.timeout);
    let max_body = request.max_body.unwrap_or(client.options.max_body);
    let max_compressed = request
        .max_compressed_body
        .unwrap_or(client.options.max_compressed_body);
    let http_client = client.http_client(&request, guard)?;

    let attempts = if client.options.retry {
        client.options.backoff.max_retries
    } else {
        0
    };
    let deadline = Instant::now() + timeout_duration;
    let mut last_error: Option<FetchError> = None;
    for attempt in 0..=attempts {
        if !client.limiter.allow(&host) {
            return Err(FetchError::CircuitOpen(host));
        }
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return Err(FetchError::Timeout);
        }
        let started = Instant::now();
        let mut builder = http_client
            .request(method.clone(), parsed.clone())
            .header(
                "Accept",
                "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
            )
            .header("Accept-Language", "en-US,en;q=0.5")
            .header("Accept-Encoding", "gzip, br, zstd");
        let user_agent = request.user_agent.as_deref().unwrap_or(DEFAULT_USER_AGENT);
        builder = builder.header("User-Agent", user_agent);
        for (name, value) in &request.headers {
            builder = builder.header(name, value);
        }
        if !request.body.is_empty() {
            builder = builder.body(request.body.clone());
        }

        let result = timeout(remaining, builder.send()).await;
        let response = match result {
            Ok(Ok(response)) => response,
            Ok(Err(error)) => {
                let retryable = retryable_request_error(&error);
                last_error = Some(FetchError::Request(error));
                // The Go oracle retries every transport error when retry is
                // enabled.  Keep the classification for circuit accounting,
                // but do not silently turn an otherwise retryable operation
                // into a one-shot request merely because reqwest classified a
                // platform-specific error differently.
                if retryable {
                    client.limiter.record_failure(&host);
                }
                if attempt >= attempts {
                    break;
                }
                let delay = client.options.backoff.delay(attempt);
                let remaining = deadline.saturating_duration_since(Instant::now());
                if remaining.is_zero() {
                    break;
                }
                tokio::time::sleep(delay.min(remaining)).await;
                continue;
            }
            Err(_) => {
                last_error = Some(FetchError::Timeout);
                client.limiter.record_failure(&host);
                if attempt >= attempts {
                    break;
                }
                let delay = client.options.backoff.delay(attempt);
                let remaining = deadline.saturating_duration_since(Instant::now());
                if remaining.is_zero() {
                    break;
                }
                tokio::time::sleep(delay.min(remaining)).await;
                continue;
            }
        };

        let status = response.status().as_u16();
        if is_transient_status(status) && attempt < attempts {
            let retry_after = response
                .headers()
                .get("retry-after")
                .and_then(|value| value.to_str().ok())
                .map(parse_retry_after)
                .unwrap_or(Duration::ZERO);
            let delay = retry_after.max(client.options.backoff.delay(attempt));
            client.limiter.record_failure(&host);
            drop(response);
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                break;
            }
            tokio::time::sleep(delay.min(remaining)).await;
            continue;
        }

        let remaining = deadline.saturating_duration_since(Instant::now());
        let body = match timeout(
            remaining,
            read_response(response, &request.url, max_compressed, max_body),
        )
        .await
        {
            Ok(body) => body?,
            Err(_) => return Err(FetchError::Timeout),
        };
        client.limiter.record_success(&host);
        return Ok(Response {
            final_url: body.final_url,
            status_code: status,
            headers: body.headers,
            body: body.body,
            protocol: body.protocol,
            content_type: body.content_type,
            elapsed: started.elapsed(),
            from_cache: false,
        });
    }
    match last_error {
        Some(error) => Err(error),
        None => Err(FetchError::InvalidRequest("request failed".to_owned())),
    }
}

fn retryable_request_error(_error: &reqwest::Error) -> bool {
    true
}

struct ReadResponse {
    final_url: String,
    headers: std::collections::BTreeMap<String, Vec<String>>,
    body: Vec<u8>,
    protocol: String,
    content_type: Option<String>,
}

async fn read_response(
    response: HttpResponse,
    requested_url: &str,
    max_compressed: usize,
    max_body: usize,
) -> Result<ReadResponse, FetchError> {
    let final_url = response.url().to_string();
    let protocol = match response.version() {
        reqwest::Version::HTTP_2 => "HTTP/2.0",
        reqwest::Version::HTTP_3 => "HTTP/3.0",
        _ => "HTTP/1.1",
    }
    .to_owned();
    let content_type = response
        .headers()
        .get("content-type")
        .and_then(|value| value.to_str().ok())
        .map(str::to_owned);
    let encoding = response
        .headers()
        .get("content-encoding")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_owned();
    let mut headers = std::collections::BTreeMap::new();
    for (name, values) in response.headers().iter() {
        headers
            .entry(name.to_string())
            .or_insert_with(Vec::new)
            .push(values.to_str().unwrap_or("").to_owned());
    }
    if response
        .content_length()
        .is_some_and(|length| length > max_compressed as u64)
    {
        return Err(FetchError::BodyTooLarge(BodyTooLarge {
            url: requested_url.to_owned(),
            limit: max_compressed,
            compressed: true,
        }));
    }
    let mut stream = response.bytes_stream();
    let mut compressed = Vec::new();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|error| FetchError::BodyRead(error.to_string()))?;
        if compressed.len().saturating_add(chunk.len()) > max_compressed {
            return Err(FetchError::BodyTooLarge(BodyTooLarge {
                url: requested_url.to_owned(),
                limit: max_compressed,
                compressed: true,
            }));
        }
        compressed.extend_from_slice(&chunk);
    }
    let decoded = decode_content(&compressed, &encoding, max_body, requested_url)?;
    let body = normalize_charset(decoded, content_type.as_deref());
    Ok(ReadResponse {
        final_url,
        headers,
        body,
        protocol,
        content_type,
    })
}

fn decode_content(
    input: &[u8],
    encoding: &str,
    max: usize,
    url: &str,
) -> Result<Vec<u8>, FetchError> {
    let mut decoded = input.to_vec();
    let encodings: Vec<_> = encoding
        .split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .collect();
    for encoding in encodings.iter().rev() {
        let mut reader: Box<dyn Read> = match encoding.to_ascii_lowercase().as_str() {
            "identity" => Box::new(Cursor::new(decoded)),
            "gzip" | "x-gzip" => Box::new(GzDecoder::new(Cursor::new(decoded))),
            "br" => Box::new(Decompressor::new(Cursor::new(decoded), 4096)),
            "zstd" => Box::new(
                ruzstd::decoding::StreamingDecoder::new_with_max_window_size(
                    Cursor::new(decoded),
                    max.max(1) as u64,
                )
                .map_err(|error| FetchError::Decode(error.to_string()))?,
            ),
            other => return Err(FetchError::UnsupportedEncoding(other.to_owned())),
        };
        decoded = read_limited(&mut reader, max).map_err(|error| match error {
            LimitError::TooLarge => FetchError::BodyTooLarge(BodyTooLarge {
                url: url.to_owned(),
                limit: max,
                compressed: false,
            }),
            LimitError::Read(error) => FetchError::Decode(error),
        })?;
    }
    if encodings.is_empty() && decoded.len() > max {
        return Err(FetchError::BodyTooLarge(BodyTooLarge {
            url: url.to_owned(),
            limit: max,
            compressed: false,
        }));
    }
    Ok(decoded)
}

enum LimitError {
    TooLarge,
    Read(String),
}

fn read_limited(reader: &mut dyn Read, max: usize) -> Result<Vec<u8>, LimitError> {
    let mut output = Vec::new();
    let mut buffer = [0_u8; 16 * 1024];
    loop {
        let read = reader
            .read(&mut buffer)
            .map_err(|error| LimitError::Read(error.to_string()))?;
        if read == 0 {
            break;
        }
        if output.len().saturating_add(read) > max {
            return Err(LimitError::TooLarge);
        }
        output.extend_from_slice(&buffer[..read]);
    }
    Ok(output)
}

fn normalize_charset(body: Vec<u8>, content_type: Option<&str>) -> Vec<u8> {
    let label = content_type
        .and_then(charset_from_content_type)
        .map(str::to_owned)
        .or_else(|| sniff_meta_charset(&body));
    let Some(label) = label else {
        return String::from_utf8_lossy(&body).into_owned().into_bytes();
    };
    let Some(encoding) = Encoding::for_label(label.as_bytes()) else {
        return String::from_utf8_lossy(&body).into_owned().into_bytes();
    };
    let (text, _, _) = encoding.decode(&body);
    text.into_owned().into_bytes()
}

fn charset_from_content_type(content_type: &str) -> Option<&str> {
    content_type.split(';').find_map(|part| {
        let (name, value) = part.split_once('=')?;
        name.trim()
            .eq_ignore_ascii_case("charset")
            .then_some(value.trim().trim_matches(['"', '\'']))
    })
}

fn sniff_meta_charset(body: &[u8]) -> Option<String> {
    let prefix = String::from_utf8_lossy(&body[..body.len().min(8192)]);
    let lower = prefix.to_ascii_lowercase();
    let start = lower.find("charset")? + "charset".len();
    let remainder = prefix.get(start..)?.trim_start();
    let remainder = remainder.strip_prefix('=')?.trim_start();
    let remainder = remainder.strip_prefix(['"', '\'']).unwrap_or(remainder);
    let end = remainder
        .find(|character: char| {
            character.is_ascii_whitespace() || matches!(character, '"' | '\'' | '>' | ';')
        })
        .unwrap_or(remainder.len());
    let label = remainder.get(..end)?.trim();
    (!label.is_empty()).then_some(label.to_owned())
}
