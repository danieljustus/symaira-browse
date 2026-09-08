//! The hermetic static fetch/render composition boundary.
use std::fmt;

use crate::{
    FetchClient, FetchError, Request, archive::WaybackClient, cache, dom, relevance, render,
    semantic,
};

/// Output format accepted by the static renderer.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub enum Format {
    #[default]
    Markdown,
    Json,
    Text,
    Html,
}

/// Rendering controls that affect output or cache identity.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Options {
    pub format: Format,
    pub selector: Option<String>,
    pub raw: bool,
    pub include_links: bool,
    pub frontmatter: bool,
    pub query: String,
    pub top_k: usize,
    pub max_chars: usize,
    pub char_threshold: usize,
    pub max_island_bytes: usize,
    pub char_limit: usize,
    pub store_full_text: bool,
    pub no_cache: bool,
    pub fetched_at: String,
}

impl Default for Options {
    fn default() -> Self {
        Self {
            format: Format::Markdown,
            selector: None,
            raw: false,
            include_links: false,
            frontmatter: false,
            query: String::new(),
            top_k: 0,
            max_chars: 20_000,
            char_threshold: 500,
            max_island_bytes: 5_000,
            char_limit: 0,
            store_full_text: false,
            no_cache: false,
            fetched_at: String::new(),
        }
    }
}

/// Result of one deterministic static render.
#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct Output {
    pub document: render::Document,
    pub meta: render::Meta,
    pub body: String,
    pub cache_id: Option<String>,
    pub recovered_url: Option<String>,
}

#[derive(Debug)]
pub enum Error {
    Parse(dom::ParseError),
    Selector(dom::SelectorError),
    Cache(cache::CacheError),
    Fetch(FetchError),
    Json(serde_json::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Parse(error) => error.fmt(f),
            Self::Selector(error) => error.fmt(f),
            Self::Cache(error) => error.fmt(f),
            Self::Fetch(error) => error.fmt(f),
            Self::Json(error) => write!(f, "render JSON: {error}"),
        }
    }
}
impl std::error::Error for Error {}
impl From<dom::ParseError> for Error {
    fn from(error: dom::ParseError) -> Self {
        Self::Parse(error)
    }
}
impl From<dom::SelectorError> for Error {
    fn from(error: dom::SelectorError) -> Self {
        Self::Selector(error)
    }
}
impl From<cache::CacheError> for Error {
    fn from(error: cache::CacheError) -> Self {
        Self::Cache(error)
    }
}
impl From<FetchError> for Error {
    fn from(error: FetchError) -> Self {
        Self::Fetch(error)
    }
}
impl From<serde_json::Error> for Error {
    fn from(error: serde_json::Error) -> Self {
        Self::Json(error)
    }
}

/// Render one fetched response without any network or filesystem side effect.
/// `store` is only touched when `store_full_text` is enabled.
pub fn render_html(
    html: &[u8],
    url: &str,
    final_url: &str,
    status_code: u16,
    options: &Options,
    store: Option<&cache::OutputCache>,
) -> Result<Output, Error> {
    let mut tree = dom::parse(html)?;
    let raw = String::from_utf8_lossy(html).into_owned();
    let selected = if let Some(selector) = options.selector.as_deref() {
        let nodes = dom::select(&tree.root, selector)?;
        let mut fragment = dom::Node::Document { children: nodes };
        dom::cleanup(&mut fragment);
        Some(fragment)
    } else {
        None
    };
    dom::cleanup(&mut tree.root);
    let content: &dom::Node = match selected.as_ref() {
        Some(fragment) => fragment,
        None => semantic::best_block(&tree.root, options.char_threshold),
    };
    let build_limit = if options.store_full_text {
        0
    } else {
        options.max_chars
    };
    let built = render::build_document(&tree, content, url, build_limit);
    let mut document = built.document;
    document
        .islands
        .retain(|island| island.raw_json.len() <= options.max_island_bytes);
    document.final_url = final_url.to_owned();
    let mut meta = render::Meta {
        final_url: final_url.to_owned(),
        status_code,
        title: document.title.clone(),
        lang: document.lang.clone(),
        truncated: built.truncated,
        protocol: "HTTP/1.1".into(),
        ..Default::default()
    };

    let mut body = if options.raw {
        raw
    } else {
        match options.format {
            Format::Markdown => render::markdown(&document, Some(content), options.include_links),
            Format::Json => render::json(&document)?,
            Format::Html => dom::serialize(content),
            Format::Text => document
                .content
                .iter()
                .map(|element| element.text.as_str())
                .filter(|text| !text.is_empty())
                .collect::<Vec<_>>()
                .join("\n"),
        }
    };

    if !options.raw && !options.query.is_empty() {
        match options.format {
            Format::Markdown => {
                let sections = relevance::split_markdown_sections(&body);
                let total = sections.len();
                body = relevance::reassemble_markdown(
                    &relevance::rank_sections(&options.query, &sections, options.top_k),
                    total,
                );
            }
            Format::Json => {
                document.content = relevance::filter_json(
                    &options.query,
                    &document.content,
                    |element| element.text.clone(),
                    options.top_k,
                );
                body = render::json(&document)?;
            }
            Format::Text | Format::Html => {}
        }
    }

    let mut cache_id = None;
    if options.store_full_text && !options.raw && !options.no_cache {
        if let Some(store) = store {
            let result = cache::truncate_and_store(
                &body,
                cache::StoreOptions {
                    char_limit: if options.char_limit == 0 {
                        options.max_chars
                    } else {
                        options.char_limit
                    },
                    ..Default::default()
                },
                Some(store),
            )?;
            body = result.output;
            cache_id = result.cache_id;
            meta.truncated |= result.stored;
        } else {
            body = render::bounded_markdown(
                &mut meta,
                &document,
                &body,
                options.max_chars,
                options.frontmatter,
                &options.fetched_at,
            );
        }
    } else if !options.raw {
        body = render::bounded_markdown(
            &mut meta,
            &document,
            &body,
            options.max_chars,
            options.frontmatter,
            &options.fetched_at,
        );
    }

    Ok(Output {
        document,
        meta,
        body,
        cache_id,
        recovered_url: None,
    })
}

/// Fetch and render through the persistent response/output cache boundary.
#[allow(clippy::too_many_arguments)]
pub async fn fetch_and_render_cached(
    client: &FetchClient,
    request: Request,
    options: &Options,
    store: Option<&cache::OutputCache>,
    response_cache: &cache::ResponseCache,
    profile: &str,
    session: &str,
    wayback: Option<&WaybackClient>,
) -> Result<Output, Error> {
    client.validate_request_policy(&request)?;
    let format = match options.format {
        Format::Markdown => "markdown",
        Format::Json => "json",
        Format::Text => "text",
        Format::Html => "html",
    };
    let key =
        cache::ResponseCache::key(&request.url, profile, format, session, &cache_key(options));
    if !options.no_cache
        && let Ok((body, raw_meta)) = response_cache.get(&key)
        && let Ok(meta) = serde_json::from_value::<render::Meta>(raw_meta)
        && valid_cached_meta(&meta)
    {
        let mut cached_target = request.clone();
        cached_target.url.clone_from(&meta.final_url);
        client.validate_request_policy(&cached_target)?;
        return Ok(Output {
            document: render::Document {
                url: request.url,
                final_url: meta.final_url.clone(),
                title: meta.title.clone(),
                lang: meta.lang.clone(),
                ..Default::default()
            },
            meta,
            body: String::from_utf8_lossy(&body).into_owned(),
            cache_id: None,
            recovered_url: None,
        });
    }
    let response = if let Some(wayback) = wayback {
        crate::archive::fetch_with_wayback(client, request.clone(), wayback, None).await?
    } else {
        client.fetch(request.clone()).await?
    };
    let mut output = render_html(
        &response.body,
        &request.url,
        &response.final_url,
        response.status_code,
        options,
        store,
    )?;
    if response.final_url != request.url {
        output.recovered_url = Some(response.final_url);
    }
    if !options.no_cache {
        response_cache.put(
            &key,
            output.body.as_bytes(),
            &serde_json::to_value(&output.meta).map_err(Error::Json)?,
        )?;
    }
    Ok(output)
}

fn valid_cached_meta(meta: &render::Meta) -> bool {
    (100..=599).contains(&meta.status_code)
        && url::Url::parse(&meta.final_url)
            .is_ok_and(|url| matches!(url.scheme(), "http" | "https") && url.host_str().is_some())
}

/// Render through the persistent response/output cache boundary.  Cache keys
/// include the URL, profile, session and every rendering option, so a selector
/// or format change can never return an incompatible document.  The cache is
/// consulted only when `no_cache` is false; writes are atomic in `ResponseCache`.
#[allow(clippy::too_many_arguments)]
pub fn render_html_cached(
    html: &[u8],
    url: &str,
    final_url: &str,
    status_code: u16,
    options: &Options,
    store: Option<&cache::OutputCache>,
    response_cache: &cache::ResponseCache,
    profile: &str,
    session: &str,
) -> Result<Output, Error> {
    let format = match options.format {
        Format::Markdown => "markdown",
        Format::Json => "json",
        Format::Text => "text",
        Format::Html => "html",
    };
    let key = cache::ResponseCache::key(url, profile, format, session, &cache_key(options));
    if !options.no_cache
        && let Ok((body, raw_meta)) = response_cache.get(&key)
        && let Ok(meta) = serde_json::from_value::<render::Meta>(raw_meta)
        && valid_cached_meta(&meta)
    {
        return Ok(Output {
            document: render::Document {
                url: url.to_owned(),
                final_url: meta.final_url.clone(),
                title: meta.title.clone(),
                lang: meta.lang.clone(),
                ..Default::default()
            },
            meta,
            body: String::from_utf8_lossy(&body).into_owned(),
            cache_id: None,
            recovered_url: None,
        });
    }
    let output = render_html(html, url, final_url, status_code, options, store)?;
    if !options.no_cache {
        let _ = response_cache.put(
            &key,
            output.body.as_bytes(),
            &serde_json::to_value(&output.meta).map_err(Error::Json)?,
        );
    }
    Ok(output)
}

fn cache_key(options: &Options) -> String {
    format!(
        "format={:?};selector={};raw={};links={};frontmatter={};query={};top_k={};max_chars={};char_threshold={};max_island_bytes={};char_limit={};store_full_text={}",
        options.format,
        options.selector.as_deref().unwrap_or_default(),
        options.raw,
        options.include_links,
        options.frontmatter,
        options.query,
        options.top_k,
        options.max_chars,
        options.char_threshold,
        options.max_island_bytes,
        options.char_limit,
        options.store_full_text,
    )
}

/// Whether the response status enters the archive/recovery path.
#[must_use]
pub const fn needs_recovery(status_code: u16) -> bool {
    matches!(status_code, 404 | 410)
}

/// Build the wildcard Wayback URL used by the recovery path.
#[must_use]
pub fn wayback_url(url: &str) -> String {
    crate::archive::rewrite_url(url, None)
}
