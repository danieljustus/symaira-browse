//! Wayback URL rewriting, snapshot parsing, and bounded recovery helpers.
use std::collections::BTreeSet;

use crate::client::{FetchClient, FetchError, Request, Response};
use crate::dom::{self, Node};

pub const WAYBACK_BASE: &str = "https://web.archive.org/web";

pub fn rewrite_url(url: &str, timestamp: Option<&str>) -> String {
    format!("{}/{}/{}", WAYBACK_BASE, timestamp.unwrap_or("*"), url)
}

pub fn parse_wayback_url(url: &str) -> Option<String> {
    let prefix = format!("{WAYBACK_BASE}/");
    let remainder = url.strip_prefix(&prefix)?;
    let (_, original) = remainder.split_once('/')?;
    (!original.is_empty()).then_some(original.into())
}

/// Remove injected Wayback controls before normal static DOM rendering.
pub fn strip_toolbar(root: &mut Node) {
    strip_toolbar_inner(root);
}
fn strip_toolbar_inner(node: &mut Node) {
    if let Node::Document { children } | Node::Element { children, .. } = node {
        children.retain(|child| {
            let id = dom::attr(child, "id").unwrap_or_default();
            let class = dom::attr(child, "class").unwrap_or_default();
            let is_wayback_script = matches!(child, Node::Element { tag, children, .. }
            if tag == "script"
                && {
                    let text = dom::text_content(children).to_ascii_lowercase();
                    text.contains("archive.org")
                        || text.contains("wombat.js")
                        || text.contains("wm-ipp")
                });
            !(id == "wm-ipp-base"
                || id == "wm-ipp"
                || class
                    .split_whitespace()
                    .any(|v| v == "wb-autocomplete-suggestions")
                || is_wayback_script)
        });
        for child in children {
            strip_toolbar_inner(child);
        }
    }
}

/// Parse and remove Wayback-injected toolbar/scripts from an HTML document.
/// Invalid HTML is returned unchanged so recovery never hides source bytes.
pub fn strip_toolbar_html(html: &[u8]) -> Result<Vec<u8>, dom::ParseError> {
    let mut tree = dom::parse(html)?;
    strip_toolbar(&mut tree.root);
    Ok(dom::serialize(&tree.root).into_bytes())
}

#[derive(Clone, Debug, PartialEq)]
pub struct Candidate {
    pub url: String,
    pub title: String,
    pub source: String,
    pub score: f64,
}

/// A CDX snapshot row. The parser accepts the header-row JSON form emitted by
/// the Wayback API but performs no network access.
#[derive(Clone, Debug, Eq, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct Snapshot {
    pub timestamp: String,
    pub original: String,
    pub mimetype: String,
    pub statuscode: String,
    pub digest: String,
    pub length: String,
}

pub fn parse_cdx_response(value: &str) -> Result<Vec<Snapshot>, String> {
    let rows: Vec<Vec<String>> =
        serde_json::from_str(value).map_err(|e| format!("decode cdx response: {e}"))?;
    if rows.len() < 2 {
        return Ok(Vec::new());
    }
    Ok(rows
        .into_iter()
        .skip(1)
        .filter_map(|row| {
            (row.len() >= 6).then(|| Snapshot {
                timestamp: row[0].clone(),
                original: row[1].clone(),
                mimetype: row[2].clone(),
                statuscode: row[3].clone(),
                digest: row[4].clone(),
                length: row[5].clone(),
            })
        })
        .collect())
}

/// Rank safe HTTP(S) links from an ancestor page as recovery candidates.
pub fn candidates_from_ancestor(root: &Node, failed_segment: &str, top_k: usize) -> Vec<Candidate> {
    let mut links = Vec::new();
    collect_links(root, &mut links);
    let target = failed_segment.to_ascii_lowercase();
    let mut seen = BTreeSet::new();
    let mut candidates = links
        .into_iter()
        .filter_map(|(url, title)| {
            if !is_safe_url(&url) || !seen.insert(url.clone()) {
                return None;
            }
            let slug = url
                .rsplit('/')
                .next()
                .unwrap_or_default()
                .split('?')
                .next()
                .unwrap_or_default()
                .to_ascii_lowercase();
            let score = fuzzy_score(&target, &slug, &title.to_ascii_lowercase());
            (score > 0.0).then_some(Candidate {
                url,
                title,
                source: "ancestor-links".into(),
                score,
            })
        })
        .collect::<Vec<_>>();
    candidates.sort_by(|a, b| b.score.total_cmp(&a.score).then_with(|| a.url.cmp(&b.url)));
    candidates.truncate(top_k);
    candidates
}
fn collect_links(node: &Node, out: &mut Vec<(String, String)>) {
    if let Node::Element {
        tag,
        attrs,
        children,
        ..
    } = node
        && tag == "a"
        && let Some(href) = attrs.get("href")
    {
        out.push((href.clone(), dom::text_content(children).trim().into()));
    }
    if let Node::Document { children } | Node::Element { children, .. } = node {
        for child in children {
            collect_links(child, out);
        }
    }
}
fn is_safe_url(url: &str) -> bool {
    let lower = url.to_ascii_lowercase();
    (lower.starts_with("http://") || lower.starts_with("https://"))
        && !lower.contains("javascript:")
        && !lower.contains("data:")
        && !lower.contains("vbscript:")
}
fn fuzzy_score(target: &str, slug: &str, title: &str) -> f64 {
    if target.is_empty() || slug.is_empty() {
        return 0.0;
    }
    if target == slug {
        return 1.0;
    }
    if slug.starts_with(target) || slug.ends_with(target) {
        return 0.8;
    }
    if target.len() >= 3 && slug.contains(target) {
        return 0.7;
    }
    if target.len() >= 3 && title.contains(target) {
        return 0.6;
    }
    let distance = levenshtein(target, slug);
    let max_len = target.len().max(slug.len());
    let similarity = 1.0 - distance as f64 / max_len as f64;
    if similarity >= 0.6 {
        similarity * 0.9
    } else {
        0.0
    }
}
fn levenshtein(a: &str, b: &str) -> usize {
    let mut previous: Vec<usize> = (0..=b.len()).collect();
    for (i, left) in a.bytes().enumerate() {
        let mut current = vec![i + 1; b.len() + 1];
        for (j, right) in b.bytes().enumerate() {
            current[j + 1] = (current[j] + 1)
                .min(previous[j + 1] + 1)
                .min(previous[j] + usize::from(left != right));
        }
        previous = current;
    }
    previous[b.len()]
}

/// Query parameters accepted by the Wayback CDX endpoint.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct CdxQuery {
    pub url: String,
    pub from: Option<String>,
    pub to: Option<String>,
    pub limit: Option<usize>,
    pub match_type: Option<String>,
}

impl CdxQuery {
    #[must_use]
    pub fn new(url: impl Into<String>) -> Self {
        Self {
            url: url.into(),
            ..Self::default()
        }
    }
}

#[derive(Debug)]
pub enum ArchiveError {
    InvalidUrl(String),
    Request(FetchError),
    Http { status: u16, body: String },
    Decode(String),
    BodyTooLarge { limit: usize },
}

impl std::fmt::Display for ArchiveError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::InvalidUrl(error) => write!(f, "archive URL: {error}"),
            Self::Request(error) => write!(f, "archive request: {error}"),
            Self::Http { status, body } => write!(f, "archive returned HTTP {status}: {body}"),
            Self::Decode(error) => write!(f, "decode archive response: {error}"),
            Self::BodyTooLarge { limit } => write!(f, "archive response exceeds {limit} bytes"),
        }
    }
}
impl std::error::Error for ArchiveError {}

/// HTTP client for the Wayback CDX API.  The endpoint is injectable so all
/// integration tests stay hermetic and can exercise real query encoding and
/// response handling through a local HTTP server.
#[derive(Clone, Debug)]
pub struct CdxClient {
    base_url: String,
    fetch: FetchClient,
    max_body: usize,
}

impl CdxClient {
    pub fn new(base_url: impl Into<String>) -> Result<Self, ArchiveError> {
        Self::with_fetch_client(
            base_url,
            FetchClient::honest().map_err(ArchiveError::Request)?,
        )
    }

    pub fn with_fetch_client(
        base_url: impl Into<String>,
        fetch: FetchClient,
    ) -> Result<Self, ArchiveError> {
        let base_url = base_url.into();
        let parsed = url::Url::parse(&base_url)
            .map_err(|error| ArchiveError::InvalidUrl(error.to_string()))?;
        if !matches!(parsed.scheme(), "http" | "https") || parsed.host_str().is_none() {
            return Err(ArchiveError::InvalidUrl(base_url));
        }
        Ok(Self {
            base_url,
            fetch,
            max_body: 2 * 1024 * 1024,
        })
    }

    #[must_use]
    pub fn default_client() -> Self {
        Self::new("https://web.archive.org/cdx/search/cdx")
            .expect("static Wayback CDX URL is valid")
    }

    #[must_use]
    pub fn max_body(mut self, limit: usize) -> Self {
        self.max_body = limit.max(1);
        self
    }

    pub async fn lookup(&self, query: &CdxQuery) -> Result<Vec<Snapshot>, ArchiveError> {
        self.lookup_with_policy(query, false, None).await
    }

    pub async fn lookup_with_policy(
        &self,
        query: &CdxQuery,
        allow_private: bool,
        allowlist: Option<symbrowse_core::policy::Allowlist>,
    ) -> Result<Vec<Snapshot>, ArchiveError> {
        if query.url.is_empty() {
            return Err(ArchiveError::InvalidUrl("target URL is empty".into()));
        }
        let mut endpoint = url::Url::parse(&self.base_url)
            .map_err(|error| ArchiveError::InvalidUrl(error.to_string()))?;
        {
            let mut pairs = endpoint.query_pairs_mut();
            pairs.append_pair("url", &query.url);
            pairs.append_pair("output", "json");
            pairs.append_pair("fl", "timestamp,original,mimetype,statuscode,digest,length");
            if let Some(from) = &query.from {
                pairs.append_pair("from", from);
            }
            if let Some(to) = &query.to {
                pairs.append_pair("to", to);
            }
            if let Some(limit) = query.limit {
                pairs.append_pair("limit", &limit.to_string());
            }
            if let Some(match_type) = &query.match_type {
                pairs.append_pair("matchType", match_type);
            }
        }
        let mut request = Request::get(endpoint.to_string());
        request.allow_private = allow_private;
        request.allowlist = allowlist;
        request.max_body = Some(self.max_body);
        request.max_compressed_body = Some(self.max_body);
        let response = self
            .fetch
            .fetch(request)
            .await
            .map_err(ArchiveError::Request)?;
        if response.status_code != 200 {
            return Err(ArchiveError::Http {
                status: response.status_code,
                body: String::from_utf8_lossy(&response.body).trim().into(),
            });
        }
        parse_cdx_response(&String::from_utf8_lossy(&response.body)).map_err(ArchiveError::Decode)
    }
}

/// A bounded HTTP client for archived page bodies.
#[derive(Clone, Debug)]
pub struct WaybackClient {
    base_url: String,
    fetch: FetchClient,
    max_body: usize,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ArchivedResponse {
    pub final_url: String,
    pub status_code: u16,
    pub headers: std::collections::BTreeMap<String, Vec<String>>,
    pub body: Vec<u8>,
}

impl WaybackClient {
    pub fn new(base_url: impl Into<String>) -> Result<Self, ArchiveError> {
        Self::with_fetch_client(
            base_url,
            FetchClient::honest().map_err(ArchiveError::Request)?,
        )
    }

    pub fn with_fetch_client(
        base_url: impl Into<String>,
        fetch: FetchClient,
    ) -> Result<Self, ArchiveError> {
        let base_url = base_url.into();
        let parsed = url::Url::parse(&base_url)
            .map_err(|error| ArchiveError::InvalidUrl(error.to_string()))?;
        if !matches!(parsed.scheme(), "http" | "https") || parsed.host_str().is_none() {
            return Err(ArchiveError::InvalidUrl(base_url));
        }
        Ok(Self {
            base_url,
            fetch,
            max_body: 10 * 1024 * 1024,
        })
    }

    #[must_use]
    pub fn default_client() -> Self {
        Self::new(WAYBACK_BASE).expect("static Wayback URL is valid")
    }

    #[must_use]
    pub fn max_body(mut self, limit: usize) -> Self {
        self.max_body = limit.max(1);
        self
    }

    #[must_use]
    pub fn archive_url(&self, original: &str, timestamp: Option<&str>) -> String {
        format!(
            "{}/{}/{}",
            self.base_url.trim_end_matches('/'),
            timestamp.unwrap_or("*"),
            original
        )
    }

    pub async fn fetch(
        &self,
        original: &str,
        timestamp: Option<&str>,
    ) -> Result<ArchivedResponse, ArchiveError> {
        self.fetch_with_policy(original, timestamp, false, None)
            .await
    }

    pub async fn fetch_with_policy(
        &self,
        original: &str,
        timestamp: Option<&str>,
        allow_private: bool,
        allowlist: Option<symbrowse_core::policy::Allowlist>,
    ) -> Result<ArchivedResponse, ArchiveError> {
        let url = self.archive_url(original, timestamp);
        let mut request = Request::get(url);
        request.allow_private = allow_private;
        request.allowlist = allowlist;
        request.max_body = Some(self.max_body);
        request.max_compressed_body = Some(self.max_body);
        let response = self
            .fetch
            .fetch(request)
            .await
            .map_err(ArchiveError::Request)?;
        Ok(ArchivedResponse {
            final_url: response.final_url,
            status_code: response.status_code,
            headers: response.headers,
            body: response.body,
        })
    }
}

/// Fetch the original URL and use Wayback only for a genuine 404/410.
/// `FETCH-002` intentionally remains on the Go transport; this helper only
/// owns the archive/recovery semantics.
pub async fn fetch_with_wayback(
    client: &FetchClient,
    request: Request,
    archive: &WaybackClient,
    timestamp: Option<&str>,
) -> Result<Response, FetchError> {
    let original = client.fetch(request.clone()).await?;
    if !matches!(original.status_code, 404 | 410) {
        return Ok(original);
    }
    let archived = match archive
        .fetch_with_policy(
            &request.url,
            timestamp,
            request.allow_private,
            request.allowlist.clone(),
        )
        .await
    {
        Ok(value) if (200..400).contains(&value.status_code) => value,
        _ => return Ok(original),
    };
    Ok(Response {
        final_url: archived.final_url,
        status_code: archived.status_code,
        headers: archived.headers,
        body: archived.body,
        protocol: "HTTP/1.1".into(),
        content_type: None,
        elapsed: original.elapsed,
        from_cache: false,
    })
}
