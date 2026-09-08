use std::sync::{Arc, Mutex};

#[cfg(target_os = "macos")]
use crate::safari_runtime::SafariRuntime;
use serde_json::{Value, json};
use symbrowse_compat::{CompatClient, Request as CompatRequest};
use symbrowse_core::{
    flows,
    policy::Allowlist,
    runner::{self, ExecutionError, RunOptions},
    state::{Cookie, OriginState},
    state_store::Store,
};
use symbrowse_engine_chrome::{BrowserMode, ChromePage, ChromeSession, resolve_chrome_executable};
use symbrowse_engine_firefox::{FirefoxSession, resolve_firefox_executable};
use symbrowse_fetch::{
    FetchClient, Request,
    archive::{CdxClient, CdxQuery},
    cache::{self, OutputCache},
    pipeline,
};
use tokio::runtime::Runtime;
use tokio::sync::Mutex as AsyncMutex;

use crate::{
    DaemonError, Frame, HandlerResult, OperationContext, SessionSpec, Warning, codes, redact_str,
};

/// The daemon-owned typed runtime. It is deliberately composed from the Rust
/// engine, fetch, and core crates; it never shells back into the CLI binary.
pub struct DispatchRuntime {
    spec: SessionSpec,
    fetch: FetchClient,
    allowlist: Option<Allowlist>,
    output_cache: OutputCache,
    wayback_cdx_url: String,
    runtime: Runtime,
    compat: AsyncMutex<Option<CompatClient>>,
    browser: Mutex<Option<BrowserState>>,
    firefox: AsyncMutex<Option<FirefoxSession>>,
    #[cfg(target_os = "macos")]
    safari: AsyncMutex<Option<SafariRuntime>>,
}

struct BrowserState {
    session: Arc<ChromeSession>,
    page: ChromePage,
    tabs: Vec<BrowserTab>,
}

#[derive(Clone)]
struct BrowserTab {
    label: String,
    page: ChromePage,
}

impl DispatchRuntime {
    pub fn new(spec: SessionSpec) -> Result<Arc<Self>, DaemonError> {
        Self::new_with_wayback_url(spec, "https://web.archive.org/cdx/search/cdx")
    }

    pub(crate) fn new_with_wayback_url(
        spec: SessionSpec,
        wayback_cdx_url: impl Into<String>,
    ) -> Result<Arc<Self>, DaemonError> {
        spec.validate_selection().map_err(|message| DaemonError {
            code: "invalid_transport_selection".into(),
            message,
            ..Default::default()
        })?;
        let allowlist = Allowlist::parse(&spec.allowed_domains).map_err(|error| DaemonError {
            code: codes::OPERATION_FAILED.into(),
            message: format!("invalid domain allowlist: {error}"),
            ..Default::default()
        })?;
        let allowlist = allowlist.active().then_some(allowlist);
        let fetch = FetchClient::honest().map_err(runtime_error)?;
        let runtime = Runtime::new().map_err(runtime_error)?;
        let output_cache = OutputCache::new(
            spec.output_cache_dir(),
            Some(std::time::Duration::from_secs(24 * 60 * 60)),
        );
        Ok(Arc::new(Self {
            spec,
            fetch,
            allowlist,
            output_cache,
            wayback_cdx_url: wayback_cdx_url.into(),
            runtime,
            compat: AsyncMutex::new(None),
            browser: Mutex::new(None),
            firefox: AsyncMutex::new(None),
            #[cfg(target_os = "macos")]
            safari: AsyncMutex::new(None),
        }))
    }

    pub fn handle(&self, frame: Frame, operation: OperationContext) -> HandlerResult {
        if operation.is_cancelled() {
            return Err(DaemonError {
                code: codes::OPERATION_TIMEOUT.into(),
                message: "daemon operation was cancelled".into(),
                ..Default::default()
            });
        }
        self.runtime.block_on(self.dispatch(frame))
    }

    async fn dispatch(&self, frame: Frame) -> HandlerResult {
        if self.spec.mode == "compat" && !matches!(frame.cmd.as_str(), "fetch.url" | "fetch.batch")
        {
            return Err(DaemonError {
                code: "compat_unsupported_command".into(),
                message: "compat transport only supports explicit fetch commands".into(),
                ..Default::default()
            });
        }
        match frame.cmd.as_str() {
            "fetch.url" => self.fetch_url(&frame).await,
            "fetch.batch" => self.fetch_batch(&frame).await,
            "cache.get" => self.cache_get(&frame),
            "wayback.snapshots" => self.wayback_snapshots(&frame).await,
            "flow.run" => self.flow_run(&frame),
            "capabilities" => Ok((
                Some(
                    serde_json::to_value(if self.spec.engine == "firefox" {
                        symbrowse_engine_firefox::canonical_capabilities()
                    } else {
                        symbrowse_engine_chrome::canonical_capabilities()
                    })
                    .map_err(runtime_error)?,
                ),
                Vec::new(),
            )),
            "open" | "goto" | "read" | "snapshot" | "click" | "dblclick" | "fill" | "type"
            | "press" | "focus" | "hover" | "select" | "check" | "uncheck" | "wait" | "back"
            | "forward" | "reload" | "scrollintoview" | "get.text" | "get.html" | "get.title"
            | "get.url" | "get.count" | "get.value" | "get.attr" | "get.box" | "get.styles"
            | "is.visible" | "is.enabled" | "is.checked" | "find" | "tabs.list" | "tab.list"
            | "tab.new" | "tab.switch" | "tab.close" | "frames.list" | "frame.tree" | "dialog"
            | "network.capture" | "network.offline" | "network.block" | "screenshot" | "pdf"
            | "upload" | "a11y" | "cookies.get" | "cookies.set" | "storage.get" | "storage.set"
            | "download" => self.browser_command(&frame).await,
            "network.har" | "axe.audit" => Err(DaemonError {
                code: "unsupported".into(),
                message: format!("Chrome daemon does not implement {:?}", frame.cmd),
                hint: "the operation is explicitly unsupported by this engine".into(),
                ..Default::default()
            }),
            "state.save" | "state.load" => self.state_browser_command(&frame).await,
            "state.list" | "state.show" | "state.clear" | "state.clean" => {
                self.state_command(&frame)
            }
            _ => Err(DaemonError {
                code: codes::UNKNOWN_COMMAND.into(),
                message: format!(
                    "command {:?} is not implemented by the Rust daemon",
                    frame.cmd
                ),
                hint: "use a registered MCP tool or daemon command".into(),
                ..Default::default()
            }),
        }
    }

    async fn fetch_url(&self, frame: &Frame) -> HandlerResult {
        let args = object_args(frame)?;
        let url = required_string(args, "url")?;
        if self.spec.mode == "compat" {
            return self.compat_fetch(args, url).await;
        }
        let format = match args
            .get("format")
            .and_then(Value::as_str)
            .unwrap_or("markdown")
        {
            "markdown" | "" => pipeline::Format::Markdown,
            "json" => pipeline::Format::Json,
            "text" => pipeline::Format::Text,
            other => return Err(malformed(format!("unsupported fetch format {other:?}"))),
        };
        let request = Request {
            url: url.to_owned(),
            allow_private: self.spec.allow_private,
            allowlist: self.allowlist.clone(),
            user_agent: Some(self.spec.fetch_user_agent.clone()),
            timeout: Some(self.spec.operation_timeout),
            session: Some(self.spec.session.clone()),
            ..Request::default()
        };
        let response = self.fetch.fetch(request).await.map_err(fetch_error)?;
        let options = pipeline::Options {
            format,
            selector: args
                .get("css_selector")
                .and_then(Value::as_str)
                .map(str::to_owned),
            include_links: args
                .get("include_links")
                .and_then(Value::as_bool)
                .unwrap_or(false),
            frontmatter: args
                .get("frontmatter")
                .and_then(Value::as_bool)
                .unwrap_or(false),
            query: args
                .get("query")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            top_k: args.get("top_k").and_then(Value::as_u64).unwrap_or(0) as usize,
            max_chars: args
                .get("max_chars")
                .and_then(Value::as_u64)
                .unwrap_or(20_000) as usize,
            char_threshold: args
                .get("char_threshold")
                .and_then(Value::as_u64)
                .unwrap_or(500) as usize,
            max_island_bytes: args
                .get("max_island_bytes")
                .and_then(Value::as_u64)
                .unwrap_or(5_000) as usize,
            char_limit: args.get("char_limit").and_then(Value::as_u64).unwrap_or(0) as usize,
            raw: args.get("raw").and_then(Value::as_bool).unwrap_or(false),
            store_full_text: args
                .get("store_full_text")
                .and_then(Value::as_bool)
                .unwrap_or(false),
            no_cache: args
                .get("no_cache")
                .and_then(Value::as_bool)
                .unwrap_or(false),
            fetched_at: String::new(),
        };
        let output = pipeline::render_html(
            &response.body,
            url,
            &response.final_url,
            response.status_code,
            &options,
            Some(&self.output_cache),
        )
        .map_err(runtime_error)?;
        let mut data = json!({
            "url": url,
            "final_url": response.final_url,
            "status_code": response.status_code,
            "title": output.document.title,
            "content": output.body,
            "cache_id": output.cache_id,
            "meta": output.meta,
        });
        if self.spec.engine == "static" {
            data["transport"] = json!({
                "mode": "static",
                "browser_identity": Value::Null,
                "tls_profile": Value::Null,
            });
        }
        Ok((Some(data), Vec::new()))
    }

    async fn compat_fetch(
        &self,
        args: &serde_json::Map<String, Value>,
        url: &str,
    ) -> HandlerResult {
        let mut policy_request = Request::get(url);
        policy_request.allow_private = self.spec.allow_private;
        policy_request.allowlist = self.allowlist.clone();
        self.fetch
            .validate_request_policy(&policy_request)
            .map_err(fetch_error)?;
        let mut guard = self.compat.lock().await;
        if guard.is_none() {
            *guard = Some(CompatClient::from_environment(self.spec.operation_timeout));
        }
        let request = CompatRequest {
            id: 0,
            method: args
                .get("method")
                .and_then(Value::as_str)
                .unwrap_or("GET")
                .to_owned(),
            url: url.to_owned(),
            profile: args
                .get("profile")
                .and_then(Value::as_str)
                .unwrap_or("chrome")
                .to_owned(),
            headers: args
                .get("headers")
                .and_then(Value::as_object)
                .map(|headers| {
                    headers
                        .iter()
                        .filter_map(|(key, value)| {
                            value.as_str().map(|value| (key.clone(), value.to_owned()))
                        })
                        .collect()
                })
                .unwrap_or_default(),
            body: args
                .get("body")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .to_owned(),
            timeout_ms: self
                .spec
                .operation_timeout
                .as_millis()
                .min(u64::MAX as u128) as u64,
            max_body_bytes: args
                .get("max_body_bytes")
                .and_then(Value::as_u64)
                .unwrap_or(10 * 1024 * 1024) as usize,
        };
        let max_body_bytes = request.max_body_bytes;
        let response = guard
            .as_mut()
            .ok_or_else(|| runtime_error("compat sidecar was not initialized"))?
            .request_with_timeout(request)
            .await
            .map_err(|error| DaemonError {
                code: match error {
                    symbrowse_compat::CompatError::Timeout => codes::OPERATION_TIMEOUT,
                    symbrowse_compat::CompatError::Integrity(_) => "compat_integrity_error",
                    _ => "compat_unavailable",
                }
                .into(),
                message: format!("compat sidecar request failed: {error}"),
                retryable: Some(false),
                ..Default::default()
            })?;
        if !response.ok {
            let error = response.error.unwrap_or(symbrowse_compat::TypedError {
                code: "compat_request_failed".into(),
                message: "compat sidecar rejected request".into(),
                retryable: false,
            });
            return Err(DaemonError {
                code: error.code,
                message: error.message,
                retryable: Some(error.retryable),
                ..Default::default()
            });
        }
        let body = response.body;
        if body.len() > max_body_bytes {
            return Err(DaemonError {
                code: "compat_response_too_large".into(),
                message: "compat sidecar response exceeded the configured bound".into(),
                ..Default::default()
            });
        }
        Ok((
            Some(
                json!({"url": url, "final_url": response.final_url, "status_code": response.status, "headers": response.headers, "content": body, "transport": {"mode":"compat", "browser_identity": Value::Null, "tls_profile": "azuretls-legacy"}}),
            ),
            Vec::new(),
        ))
    }

    async fn fetch_batch(&self, frame: &Frame) -> HandlerResult {
        let args = object_args(frame)?;
        let urls = args
            .get("urls")
            .and_then(Value::as_array)
            .ok_or_else(|| malformed("fetch.batch requires a urls array"))?;
        if urls.is_empty() || urls.len() > 20 || urls.iter().any(|url| !url.is_string()) {
            return Err(malformed("fetch.batch urls must contain 1-20 strings"));
        }
        let mut result = Vec::with_capacity(urls.len());
        for url in urls.iter().filter_map(Value::as_str) {
            let mut child_args = args.clone();
            child_args.remove("urls");
            child_args.insert("url".into(), Value::String(url.to_owned()));
            let frame = Frame {
                cmd: "fetch.url".into(),
                args: Some(Value::Object(child_args)),
                session: self.spec.session.clone(),
                ..Frame::default()
            };
            match self.fetch_url(&frame).await {
                Ok((Some(data), warnings)) => {
                    result.push(json!({"url":url,"ok":true,"content":data,"warnings":warnings}))
                }
                Ok((None, warnings)) => {
                    result.push(json!({"url":url,"ok":true,"warnings":warnings}))
                }
                Err(error) => result
                    .push(json!({"url":url,"ok":false,"code":error.code,"error":error.message})),
            }
        }
        Ok((Some(Value::Array(result)), Vec::new()))
    }

    fn cache_get(&self, frame: &Frame) -> HandlerResult {
        let args = object_args(frame)?;
        let cache_id = required_string(args, "cache_id")?;
        let content = self
            .output_cache
            .load(cache_id)
            .map_err(|error| DaemonError {
                code: codes::OPERATION_FAILED.into(),
                message: redact_str(&error.to_string()),
                ..Default::default()
            })?;
        let mut result =
            json!({"cache_id": cache_id, "content": String::from_utf8_lossy(&content)});
        if let Some(range) = args
            .get("range")
            .and_then(Value::as_str)
            .filter(|value| !value.trim().is_empty())
        {
            let (start, end) = parse_cache_range(range).map_err(malformed)?;
            result["range"] = Value::String(range.to_owned());
            result["content"] = Value::String(cache::line_range(&content, start, end));
        }
        Ok((Some(result), Vec::new()))
    }

    async fn wayback_snapshots(&self, frame: &Frame) -> HandlerResult {
        let args = object_args(frame)?;
        let url = required_string(args, "url")?;
        let match_type = args
            .get("match_type")
            .and_then(Value::as_str)
            .unwrap_or("exact");
        if !matches!(match_type, "exact" | "prefix" | "host") {
            return Err(malformed("match_type must be exact, prefix, or host"));
        }
        let mut target = Request::get(url);
        target.allow_private = self.spec.allow_private;
        target.allowlist = self.allowlist.clone();
        self.fetch
            .validate_request_policy(&target)
            .map_err(fetch_error)?;
        let limit = args
            .get("limit")
            .and_then(Value::as_u64)
            .unwrap_or(100)
            .min(1000) as usize;
        let client = CdxClient::with_fetch_client(&self.wayback_cdx_url, self.fetch.clone())
            .map_err(runtime_error)?;
        let snapshots = client
            .lookup_with_policy(
                &CdxQuery {
                    url: url.to_owned(),
                    from: args.get("from").and_then(Value::as_str).map(str::to_owned),
                    to: args.get("to").and_then(Value::as_str).map(str::to_owned),
                    limit: Some(limit),
                    match_type: Some(match_type.to_owned()),
                },
                self.spec.allow_private,
                self.allowlist.clone(),
            )
            .await
            .map_err(|error| runtime_error(error.to_string()))?;
        let entries = snapshots
            .into_iter()
            .map(|snapshot| {
                json!({
                    "timestamp": snapshot.timestamp,
                    "url": snapshot.original,
                    "status": snapshot.statuscode,
                    "mime_type": snapshot.mimetype,
                    "digest": snapshot.digest,
                })
            })
            .collect::<Vec<_>>();
        Ok((Some(Value::Array(entries)), Vec::new()))
    }

    async fn browser_command(&self, frame: &Frame) -> HandlerResult {
        if self.spec.mode == "browser" && self.spec.engine == "firefox" {
            return self.firefox_command(frame).await;
        }
        if self.spec.mode == "static" || self.spec.engine == "static" {
            return match frame.cmd.as_str() {
                "open" | "goto" | "read" => {
                    self.fetch_url(&Frame {
                        cmd: "fetch.url".into(),
                        args: frame.args.clone(),
                        session: frame.session.clone(),
                        ..Frame::default()
                    })
                    .await
                }
                _ => Err(DaemonError {
                    code: codes::OPERATION_FAILED.into(),
                    message: "the static engine supports open, goto, and read only".into(),
                    ..Default::default()
                }),
            };
        }
        #[cfg(target_os = "macos")]
        if matches!(self.spec.engine.as_str(), "safari-attach" | "safari-bidi") {
            self.ensure_safari().await?;
            let mut guard = self.safari.lock().await;
            let runtime = guard
                .as_mut()
                .ok_or_else(|| runtime_error("Safari runtime was not initialized"))?;
            return runtime.command(frame, self.spec.operation_timeout).await;
        }
        #[cfg(not(target_os = "macos"))]
        if matches!(self.spec.engine.as_str(), "safari-attach" | "safari-bidi") {
            return Err(DaemonError {
                code: codes::OPERATION_FAILED.into(),
                message: "Safari engines are only available on macOS".into(),
                ..Default::default()
            });
        }
        let page = self.ensure_browser().await?;
        let args = object_args(frame)?;
        let data = match frame.cmd.as_str() {
            "tabs.list" | "tab.list" => {
                let (tabs, active_id) = {
                    let guard = self
                        .browser
                        .lock()
                        .map_err(|_| runtime_error("browser lock poisoned"))?;
                    let browser = guard
                        .as_ref()
                        .ok_or_else(|| runtime_error("browser was not initialized"))?;
                    (browser.tabs.clone(), browser.page.target_id())
                };
                let mut listed = Vec::with_capacity(tabs.len());
                let mut active = String::new();
                for (index, tab) in tabs.into_iter().enumerate() {
                    let id = format!("t{}", index + 1);
                    let is_active = tab.page.target_id() == active_id;
                    if is_active {
                        active = id.clone();
                    }
                    let url = tab
                        .page
                        .inspect("body", "url")
                        .await
                        .map_err(runtime_error)?;
                    listed.push(
                        json!({"id": id, "label": tab.label, "url": url, "active": is_active}),
                    );
                }
                json!({"tabs": listed, "active": active})
            }
            "tab.new" => {
                let session = self.chrome_session()?;
                let url = args
                    .get("url")
                    .and_then(Value::as_str)
                    .unwrap_or("about:blank");
                let page = session.new_page(url).await.map_err(runtime_error)?;
                let label = args.get("label").and_then(Value::as_str).unwrap_or("");
                let mut guard = self
                    .browser
                    .lock()
                    .map_err(|_| runtime_error("browser lock poisoned"))?;
                let browser = guard
                    .as_mut()
                    .ok_or_else(|| runtime_error("browser was not initialized"))?;
                let index = browser.tabs.len() + 1;
                let label = if label.is_empty() {
                    format!("t{index}")
                } else {
                    label.to_owned()
                };
                browser.tabs.push(BrowserTab {
                    label: label.clone(),
                    page: page.clone(),
                });
                browser.page = page;
                json!({"tab": format!("t{index}"), "label": label})
            }
            "tab.switch" => {
                let target = required_string(args, "tab")?;
                let (index, tab) = self.chrome_tab(target)?;
                tab.page
                    .raw()
                    .bring_to_front()
                    .await
                    .map_err(runtime_error)?;
                let mut guard = self
                    .browser
                    .lock()
                    .map_err(|_| runtime_error("browser lock poisoned"))?;
                guard
                    .as_mut()
                    .ok_or_else(|| runtime_error("browser was not initialized"))?
                    .page = tab.page;
                json!({"tab": format!("t{}", index + 1), "label": tab.label})
            }
            "tab.close" => {
                let (index, closing) = match args.get("tab").and_then(Value::as_str) {
                    Some(target) if !target.trim().is_empty() => self.chrome_tab(target)?,
                    _ => self.chrome_active_tab()?,
                };
                let closing_id = closing.page.target_id();
                let next = {
                    let browser = self
                        .browser
                        .lock()
                        .map_err(|_| runtime_error("browser lock poisoned"))?;
                    let browser = browser
                        .as_ref()
                        .ok_or_else(|| runtime_error("browser was not initialized"))?;
                    if browser.tabs.len() == 1 {
                        return Err(runtime_error("cannot close the last tab of a session"));
                    }
                    browser
                        .tabs
                        .get(if index + 1 < browser.tabs.len() {
                            index + 1
                        } else {
                            index - 1
                        })
                        .cloned()
                        .ok_or_else(|| runtime_error("next tab was not available"))?
                };
                closing
                    .page
                    .raw()
                    .clone()
                    .close()
                    .await
                    .map_err(runtime_error)?;
                let mut guard = self
                    .browser
                    .lock()
                    .map_err(|_| runtime_error("browser lock poisoned"))?;
                let browser = guard
                    .as_mut()
                    .ok_or_else(|| runtime_error("browser was not initialized"))?;
                let closed_index = browser
                    .tabs
                    .iter()
                    .position(|tab| tab.page.target_id() == closing_id)
                    .ok_or_else(|| runtime_error("tab disappeared before it could be closed"))?;
                browser.tabs.remove(closed_index);
                let active_index = browser
                    .tabs
                    .iter()
                    .position(|tab| tab.page.target_id() == next.page.target_id())
                    .ok_or_else(|| runtime_error("next tab disappeared while closing tab"))?;
                browser.page = next.page;
                json!({"closed": format!("t{}", closed_index + 1), "active": format!("t{}", active_index + 1)})
            }
            "frames.list" | "frame.tree" => {
                json!({"frames": page.frames().await.map_err(runtime_error)?})
            }
            "a11y" => json!({"nodes": page.accessibility_tree().await.map_err(runtime_error)?}),
            "dialog" => {
                let accept = args.get("accept").and_then(Value::as_bool).unwrap_or(false);
                let prompt = args
                    .get("prompt_text")
                    .and_then(Value::as_str)
                    .map(str::to_owned);
                serde_json::to_value(
                    page.dialog(accept, prompt, self.spec.operation_timeout)
                        .await
                        .map_err(runtime_error)?,
                )
                .map_err(runtime_error)?
            }
            "network.capture" => {
                let capture = page.start_network_capture().await.map_err(runtime_error)?;
                json!({"events": capture.collect(self.spec.operation_timeout).await})
            }
            "network.offline" => {
                page.set_offline(args.get("offline").and_then(Value::as_bool).unwrap_or(true))
                    .await
                    .map_err(runtime_error)?;
                json!({"offline": args.get("offline").and_then(Value::as_bool).unwrap_or(true)})
            }
            "network.block" => {
                let urls = args
                    .get("urls")
                    .and_then(Value::as_array)
                    .ok_or_else(|| malformed("network.block requires urls"))?
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect();
                page.block_urls(urls).await.map_err(runtime_error)?;
                json!({"blocked": true})
            }
            "screenshot" => serde_json::to_value(
                page.screenshot(
                    serde_json::from_value(args.clone().into()).map_err(runtime_error)?,
                )
                .await
                .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "pdf" => serde_json::to_value(page.pdf().await.map_err(runtime_error)?)
                .map_err(runtime_error)?,
            "upload" => {
                let files = args
                    .get("files")
                    .and_then(Value::as_array)
                    .ok_or_else(|| malformed("upload requires files"))?
                    .iter()
                    .filter_map(Value::as_str)
                    .map(str::to_owned)
                    .collect::<Vec<_>>();
                let allowed = args
                    .get("allowed_dirs")
                    .and_then(Value::as_array)
                    .map(|v| {
                        v.iter()
                            .filter_map(Value::as_str)
                            .map(str::to_owned)
                            .collect::<Vec<_>>()
                    })
                    .unwrap_or_default();
                page.upload_files(required_string(args, "selector")?, &files, &allowed)
                    .await
                    .map_err(runtime_error)?;
                json!({"uploaded": files})
            }
            "open" | "goto" => page
                .open(required_string(args, "url")?)
                .await
                .map_err(runtime_error)?,
            "read" => {
                if let Some(url) = args
                    .get("url")
                    .and_then(Value::as_str)
                    .filter(|url| !url.is_empty())
                {
                    page.open(url).await.map_err(runtime_error)?;
                }
                page.read().await.map_err(runtime_error)?
            }
            "snapshot" => json!({"nodes": page.snapshot().await.map_err(runtime_error)?}),
            "click" => serde_json::to_value(
                page.click(required_string(args, "selector")?)
                    .await
                    .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "dblclick" => serde_json::to_value(
                page.double_click(required_string(args, "selector")?)
                    .await
                    .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "fill" => serde_json::to_value(
                page.fill(
                    required_string(args, "selector")?,
                    required_string(args, "value")?,
                )
                .await
                .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "type" => serde_json::to_value(
                page.type_text(
                    args.get("selector")
                        .and_then(Value::as_str)
                        .unwrap_or("body"),
                    required_string(args, "value")?,
                )
                .await
                .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "press" => serde_json::to_value(
                page.press(
                    args.get("selector")
                        .and_then(Value::as_str)
                        .unwrap_or("body"),
                    required_string(args, "key")?,
                )
                .await
                .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "focus" => serde_json::to_value(
                page.focus(required_string(args, "selector")?)
                    .await
                    .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "hover" => serde_json::to_value(
                page.hover(required_string(args, "selector")?)
                    .await
                    .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "select" => serde_json::to_value(
                page.select(
                    required_string(args, "selector")?,
                    required_string(args, "value")?,
                )
                .await
                .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "check" => serde_json::to_value(
                page.check(required_string(args, "selector")?)
                    .await
                    .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "uncheck" => serde_json::to_value(
                page.uncheck(required_string(args, "selector")?)
                    .await
                    .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "get.text" | "get.html" | "get.title" | "get.url" | "get.count" | "get.value"
            | "get.attr" | "get.box" | "get.styles" | "is.visible" | "is.enabled"
            | "is.checked" => {
                let selector = args
                    .get("selector")
                    .and_then(Value::as_str)
                    .unwrap_or("body");
                let kind = frame
                    .cmd
                    .strip_prefix("get.")
                    .or_else(|| frame.cmd.strip_prefix("is."))
                    .unwrap_or("text");
                let value = page.inspect(selector, kind).await.map_err(runtime_error)?;
                if frame.cmd == "get.attr" {
                    let attribute = required_string(args, "attribute")?;
                    value.get(attribute).cloned().unwrap_or(Value::Null)
                } else {
                    value
                }
            }
            "find" => {
                let kind = args.get("kind").and_then(Value::as_str).unwrap_or("text");
                let query = args
                    .get("query")
                    .and_then(Value::as_str)
                    .ok_or_else(|| malformed("find requires query"))?;
                let action = args.get("action").and_then(Value::as_str).unwrap_or("ref");
                let name = args.get("name").and_then(Value::as_str).unwrap_or("");
                let exact = args.get("exact").and_then(Value::as_bool).unwrap_or(false);
                let index = args
                    .get("index")
                    .and_then(Value::as_u64)
                    .map(|value| value as usize);
                let value = args.get("value").and_then(Value::as_str).unwrap_or("");
                page.find(symbrowse_engine_chrome::FindOptions {
                    kind: kind.to_owned(),
                    query: query.to_owned(),
                    action: action.to_owned(),
                    name: name.to_owned(),
                    exact,
                    index,
                    value: value.to_owned(),
                })
                .await
                .map_err(runtime_error)?
            }
            "scrollintoview" => serde_json::to_value(
                page.scroll_into_view(required_string(args, "selector")?)
                    .await
                    .map_err(runtime_error)?,
            )
            .map_err(runtime_error)?,
            "wait" => {
                let kind = args
                    .get("kind")
                    .and_then(Value::as_str)
                    .unwrap_or("selector");
                let timeout = self.spec.operation_timeout;
                match kind {
                    "ms" => {
                        let duration = args
                            .get("duration")
                            .and_then(Value::as_u64)
                            .or_else(|| {
                                args.get("ms")
                                    .and_then(Value::as_u64)
                                    .map(|value| value.saturating_mul(1_000_000))
                            })
                            .unwrap_or(0);
                        let duration = std::time::Duration::from_nanos(duration);
                        if duration > timeout {
                            return Err(DaemonError {
                                code: codes::OPERATION_TIMEOUT.into(),
                                message: "wait duration exceeds operation timeout".into(),
                                ..Default::default()
                            });
                        }
                        tokio::time::sleep(duration).await;
                    }
                    "load" => page
                        .wait_for_navigation(timeout)
                        .await
                        .map_err(runtime_error)?,
                    "selector" => {
                        let selector = args
                            .get("value")
                            .and_then(Value::as_str)
                            .or_else(|| args.get("selector").and_then(Value::as_str))
                            .ok_or_else(|| malformed("wait selector requires value"))?;
                        let state = args
                            .get("state")
                            .and_then(Value::as_str)
                            .unwrap_or("visible");
                        if matches!(state, "visible" | "attached") {
                            page.wait_for_selector(selector, state == "visible", timeout)
                                .await
                                .map_err(runtime_error)?;
                        } else {
                            let started = std::time::Instant::now();
                            while started.elapsed() < timeout {
                                let present = page
                                    .inspect(selector, "find")
                                    .await
                                    .map_err(runtime_error)?;
                                if present.as_bool() == Some(false) {
                                    break;
                                }
                                tokio::time::sleep(std::time::Duration::from_millis(20)).await;
                            }
                            if started.elapsed() >= timeout {
                                return Err(DaemonError {
                                    code: codes::OPERATION_TIMEOUT.into(),
                                    message: format!(
                                        "timeout waiting for selector {selector:?} to be {state}"
                                    ),
                                    ..Default::default()
                                });
                            }
                        }
                    }
                    "text" => {
                        let text = args
                            .get("value")
                            .and_then(Value::as_str)
                            .ok_or_else(|| malformed("wait text requires value"))?;
                        let started = std::time::Instant::now();
                        while started.elapsed() < timeout {
                            let body = page.inspect("body", "text").await.map_err(runtime_error)?;
                            if body.as_str().is_some_and(|body| body.contains(text)) {
                                break;
                            }
                            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
                        }
                        if started.elapsed() >= timeout {
                            return Err(DaemonError {
                                code: codes::OPERATION_TIMEOUT.into(),
                                message: format!("timeout waiting for text {text:?}"),
                                ..Default::default()
                            });
                        }
                    }
                    "url" => {
                        let expected = args
                            .get("value")
                            .and_then(Value::as_str)
                            .ok_or_else(|| malformed("wait url requires value"))?;
                        let started = std::time::Instant::now();
                        while started.elapsed() < timeout {
                            let current =
                                page.inspect("body", "url").await.map_err(runtime_error)?;
                            if current.as_str().is_some_and(|url| url.contains(expected)) {
                                break;
                            }
                            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
                        }
                        if started.elapsed() >= timeout {
                            return Err(DaemonError {
                                code: codes::OPERATION_TIMEOUT.into(),
                                message: format!("timeout waiting for URL {expected:?}"),
                                ..Default::default()
                            });
                        }
                    }
                    other => return Err(malformed(format!("unsupported wait kind {other:?}"))),
                }
                json!({"ready":true,"kind":kind})
            }
            "back" | "forward" | "reload" => page
                .navigation(frame.cmd.as_str())
                .await
                .map_err(runtime_error)?,
            _ => {
                return Err(DaemonError {
                    code: codes::UNKNOWN_COMMAND.into(),
                    message: "browser command is not implemented".into(),
                    ..Default::default()
                });
            }
        };
        Ok((Some(data), Vec::new()))
    }

    fn flow_run(&self, frame: &Frame) -> HandlerResult {
        let args = object_args(frame)?;
        let source = required_string(args, "yaml")?;
        let flow = flows::parse(source.as_bytes(), "cli").map_err(|error| DaemonError {
            code: codes::MALFORMED_REQUEST.into(),
            message: error.to_string(),
            ..Default::default()
        })?;
        let inputs = args
            .get("inputs")
            .cloned()
            .map(serde_json::from_value::<std::collections::BTreeMap<String, String>>)
            .transpose()
            .map_err(|error| {
                malformed(format!("flow inputs must be an object of strings: {error}"))
            })?
            .unwrap_or_default();
        let dry_run = args
            .get("dry_run")
            .and_then(Value::as_bool)
            .unwrap_or(false);
        let mut executor = |command: &str, command_args: Value| {
            let request = Frame {
                cmd: command.to_owned(),
                args: Some(command_args),
                session: self.spec.session.clone(),
                ..Frame::default()
            };
            let response = tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(self.dispatch(request))
            });
            match response {
                Ok((Some(data), _)) => Ok(data),
                Ok((None, _)) => Ok(Value::Null),
                Err(error) => Err(ExecutionError::new(error.message)),
            }
        };
        let report = runner::run(
            &mut executor,
            RunOptions {
                flow,
                inputs,
                dry_run,
            },
        )
        .map_err(|error| DaemonError {
            code: "flow_failed".into(),
            message: error.to_string(),
            details: Some(json!({"step_index": error.step_index, "action": error.action})),
            ..Default::default()
        })?;
        Ok((
            Some(serde_json::to_value(report).map_err(runtime_error)?),
            Vec::new(),
        ))
    }
    #[cfg(target_os = "macos")]
    async fn ensure_safari(&self) -> Result<(), DaemonError> {
        {
            let guard = self.safari.lock().await;
            if guard.is_some() {
                return Ok(());
            }
        }
        let runtime = SafariRuntime::launch(&self.spec).await?;
        let mut guard = self.safari.lock().await;
        if guard.is_none() {
            *guard = Some(runtime);
        }
        Ok(())
    }

    async fn firefox_command(&self, frame: &Frame) -> HandlerResult {
        let args = object_args(frame)?;
        let mut guard = self.firefox.lock().await;
        if guard.is_none() {
            let executable = resolve_firefox_executable(
                (!self.spec.executable_path.as_os_str().is_empty())
                    .then_some(self.spec.executable_path.as_path()),
            )
            .map_err(runtime_error)?;
            let session = FirefoxSession::launch(
                executable,
                self.spec.user_data_dir(),
                self.spec.operation_timeout,
            )
            .await
            .map_err(runtime_error)?;
            *guard = Some(session);
        }
        let session = guard
            .as_mut()
            .ok_or_else(|| runtime_error("Firefox runtime was not initialized"))?;
        match frame.cmd.as_str() {
            "open" | "goto" | "read" => {
                let url = required_string(args, "url")?;
                let navigation = session.navigate(url).await.map_err(runtime_error)?;
                let value = session.evaluate("({url: location.href, title: document.title, content: document.body?.innerText ?? ''})").await.map_err(runtime_error)?;
                Ok((
                    Some(
                        json!({"url": navigation.url, "final_url": navigation.url, "title": value.value.as_ref().and_then(|v|v.get("title")).cloned().unwrap_or(Value::Null), "content": value.value.as_ref().and_then(|v|v.get("content")).cloned().unwrap_or(Value::Null)}),
                    ),
                    Vec::new(),
                ))
            }
            "get.url" => {
                let value = session
                    .evaluate("location.href")
                    .await
                    .map_err(runtime_error)?;
                Ok((Some(json!({"value":value.value})), Vec::new()))
            }
            "get.title" => {
                let value = session
                    .evaluate("document.title")
                    .await
                    .map_err(runtime_error)?;
                Ok((Some(json!({"value":value.value})), Vec::new()))
            }
            "evaluate" | "eval" => {
                let expression = required_string(args, "expression")?;
                let value = session.evaluate(expression).await.map_err(runtime_error)?;
                Ok((
                    Some(serde_json::to_value(value).map_err(runtime_error)?),
                    Vec::new(),
                ))
            }
            "cookies.get" => Ok((
                Some(session.cookies().await.map_err(runtime_error)?),
                Vec::new(),
            )),
            "cookies.set" => {
                let cookie = args
                    .get("cookie")
                    .cloned()
                    .ok_or_else(|| malformed("cookies.set requires cookie"))?;
                Ok((
                    Some(session.set_cookie(cookie).await.map_err(runtime_error)?),
                    Vec::new(),
                ))
            }
            "storage.get" => {
                let value = session
                    .evaluate("JSON.stringify({local_storage:Object.fromEntries(Object.entries(localStorage)),session_storage:Object.fromEntries(Object.entries(sessionStorage))})")
                    .await
                    .map_err(runtime_error)?;
                let data = value
                    .value
                    .and_then(|value| value.as_str().map(str::to_owned))
                    .ok_or_else(|| runtime_error("Firefox storage capture returned no JSON"))?;
                let data = serde_json::from_str(&data).map_err(runtime_error)?;
                Ok((Some(data), Vec::new()))
            }
            "storage.set" => {
                let local = args
                    .get("local_storage")
                    .cloned()
                    .unwrap_or_else(|| json!({}));
                let session_storage = args
                    .get("session_storage")
                    .cloned()
                    .unwrap_or_else(|| json!({}));
                let local = serde_json::to_string(&local).map_err(runtime_error)?;
                let session_storage =
                    serde_json::to_string(&session_storage).map_err(runtime_error)?;
                let expression = format!(
                    "(() => {{ for (const [k,v] of Object.entries({local})) localStorage.setItem(k,v); for (const [k,v] of Object.entries({session_storage})) sessionStorage.setItem(k,v); return true; }})()"
                );
                let value = session.evaluate(&expression).await.map_err(runtime_error)?;
                Ok((Some(value.value.unwrap_or(Value::Null)), Vec::new()))
            }
            "click" | "type" | "fill" => {
                let selector = args
                    .get("selector")
                    .and_then(Value::as_str)
                    .unwrap_or("body");
                let value = args.get("value").and_then(Value::as_str);
                let data = session
                    .interact(frame.cmd.as_str(), selector, value)
                    .await
                    .map_err(runtime_error)?;
                Ok((Some(data), Vec::new()))
            }
            "tabs.list" | "frames.list" => {
                let tree = session.browsing_contexts().await.map_err(runtime_error)?;
                Ok((
                    Some(if frame.cmd == "tabs.list" {
                        json!({"tabs":tree.get("contexts").cloned().unwrap_or(Value::Array(Vec::new()))})
                    } else {
                        json!({"frames":tree.get("contexts").cloned().unwrap_or(Value::Array(Vec::new()))})
                    }),
                    Vec::new(),
                ))
            }
            "screenshot" => {
                let format = args.get("format").and_then(Value::as_str).unwrap_or("png");
                Ok((
                    Some(session.screenshot(format).await.map_err(runtime_error)?),
                    Vec::new(),
                ))
            }
            "network.capture" | "download" | "network.har" => Err(DaemonError {
                code: "unsupported".into(),
                message: format!("Firefox does not implement {:?}", frame.cmd),
                hint: "the operation is explicitly unsupported by this engine".into(),
                ..Default::default()
            }),
            _ => Err(DaemonError {
                code: "unsupported".into(),
                message: format!("Firefox does not implement {:?}", frame.cmd),
                hint: "the operation is explicitly unsupported by this engine".into(),
                ..Default::default()
            }),
        }
    }

    fn chrome_session(&self) -> Result<Arc<ChromeSession>, DaemonError> {
        Ok(self
            .browser
            .lock()
            .map_err(|_| runtime_error("browser lock poisoned"))?
            .as_ref()
            .ok_or_else(|| runtime_error("browser was not initialized"))?
            .session
            .clone())
    }

    fn chrome_tab(&self, target: &str) -> Result<(usize, BrowserTab), DaemonError> {
        let target = target.trim().trim_start_matches('@');
        let browser = self
            .browser
            .lock()
            .map_err(|_| runtime_error("browser lock poisoned"))?;
        let browser = browser
            .as_ref()
            .ok_or_else(|| runtime_error("browser was not initialized"))?;
        if let Some(index) = target
            .strip_prefix('t')
            .and_then(|value| value.parse::<usize>().ok())
            .and_then(|index| index.checked_sub(1))
            && let Some(tab) = browser.tabs.get(index)
        {
            return Ok((index, tab.clone()));
        }
        browser
            .tabs
            .iter()
            .enumerate()
            .find(|(_, tab)| tab.label == target)
            .map(|(index, tab)| (index, tab.clone()))
            .ok_or_else(|| runtime_error(format!("tab {target:?} not found")))
    }

    fn chrome_active_tab(&self) -> Result<(usize, BrowserTab), DaemonError> {
        let browser = self
            .browser
            .lock()
            .map_err(|_| runtime_error("browser lock poisoned"))?;
        let browser = browser
            .as_ref()
            .ok_or_else(|| runtime_error("browser was not initialized"))?;
        let active_id = browser.page.target_id();
        browser
            .tabs
            .iter()
            .enumerate()
            .find(|(_, tab)| tab.page.target_id() == active_id)
            .map(|(index, tab)| (index, tab.clone()))
            .ok_or_else(|| runtime_error("active tab is not tracked"))
    }

    async fn ensure_browser(&self) -> Result<ChromePage, DaemonError> {
        {
            let guard = self
                .browser
                .lock()
                .map_err(|_| runtime_error("browser lock poisoned"))?;
            if let Some(browser) = guard.as_ref() {
                return Ok(browser.page.clone());
            }
        }
        let executable = resolve_chrome_executable(
            (!self.spec.executable_path.as_os_str().is_empty())
                .then_some(self.spec.executable_path.as_path()),
        )
        .map_err(runtime_error)?;
        let session = ChromeSession::connect(
            BrowserMode::Launch {
                executable,
                user_data_dir: self.spec.user_data_dir(),
                headless: true,
            },
            self.spec.operation_timeout,
        )
        .await
        .map_err(runtime_error)?;
        let page = session
            .new_page("about:blank")
            .await
            .map_err(runtime_error)?;
        let result = page.clone();
        let mut guard = self
            .browser
            .lock()
            .map_err(|_| runtime_error("browser lock poisoned"))?;
        if let Some(browser) = guard.as_ref() {
            return Ok(browser.page.clone());
        }
        *guard = Some(BrowserState {
            session: Arc::new(session),
            page: page.clone(),
            tabs: vec![BrowserTab {
                label: "t1".into(),
                page,
            }],
        });
        Ok(result)
    }

    async fn state_browser_command(&self, frame: &Frame) -> HandlerResult {
        let args = object_args(frame)?;
        let name = required_string(args, "name")?;
        let store = Store::new(
            self.spec.state_store_dir(),
            time::Duration::days(self.spec.state_expire_days),
            None,
        )
        .map_err(runtime_error)?;
        match frame.cmd.as_str() {
            "state.save" => {
                let mut captured = if self.spec.engine == "static" {
                    return Err(DaemonError {
                        code: codes::OPERATION_FAILED.into(),
                        message: "state.save requires a browser-backed engine".into(),
                        ..Default::default()
                    });
                } else if cfg!(target_os = "macos")
                    && matches!(self.spec.engine.as_str(), "safari-attach" | "safari-bidi")
                {
                    #[cfg(target_os = "macos")]
                    {
                        self.ensure_safari().await?;
                        let mut guard = self.safari.lock().await;
                        guard
                            .as_mut()
                            .ok_or_else(|| runtime_error("Safari runtime was not initialized"))?
                            .capture()
                            .await
                            .map_err(runtime_error)?
                    }
                    #[cfg(not(target_os = "macos"))]
                    {
                        unreachable!()
                    }
                } else {
                    self.ensure_browser().await?.evaluate_script(
                        "(() => ({origin: location.origin, local_storage: Object.fromEntries(Object.entries(localStorage)), session_storage: Object.fromEntries(Object.entries(sessionStorage)), cookies: document.cookie}))()",
                    ).await.map_err(runtime_error)?
                };
                if self.spec.engine == "chrome" {
                    captured["bidi_cookies"] = self
                        .ensure_browser()
                        .await
                        .map_err(runtime_error)?
                        .cookies()
                        .await
                        .map_err(runtime_error)?;
                }
                let (origin, entry) = captured_origin_state(&captured).map_err(runtime_error)?;
                let mut state = symbrowse_core::state::State {
                    schema_version: symbrowse_core::state::SCHEMA_VERSION,
                    name: name.to_owned(),
                    saved_at: String::new(),
                    expires_at: String::new(),
                    key_source: "none".to_owned(),
                    origins: std::iter::once((origin, entry)).collect(),
                };
                store
                    .save_at(&mut state, time::OffsetDateTime::now_utc())
                    .map_err(runtime_error)?;
                let metadata = store.metadata(name).map_err(runtime_error)?;
                Ok((
                    Some(json!({"saved": name, "metadata": metadata})),
                    Vec::new(),
                ))
            }
            "state.load" => {
                let state = store.load(name).map_err(runtime_error)?;
                if self.spec.engine == "static" {
                    return Err(DaemonError {
                        code: codes::OPERATION_FAILED.into(),
                        message: "state.load requires a browser-backed engine".into(),
                        ..Default::default()
                    });
                }
                let mut warnings = Vec::new();
                for (origin, entry) in &state.origins {
                    let result = if cfg!(target_os = "macos")
                        && matches!(self.spec.engine.as_str(), "safari-attach" | "safari-bidi")
                    {
                        #[cfg(target_os = "macos")]
                        {
                            self.ensure_safari().await?;
                            let mut guard = self.safari.lock().await;
                            guard
                                .as_mut()
                                .ok_or_else(|| runtime_error("Safari runtime was not initialized"))?
                                .restore_origin(origin, entry)
                                .await
                        }
                        #[cfg(not(target_os = "macos"))]
                        {
                            unreachable!()
                        }
                    } else {
                        let page = self.ensure_browser().await.map_err(runtime_error)?;
                        page.open(origin).await.map_err(runtime_error)?;
                        for cookie in &entry.cookies {
                            let cookie_value =
                                serde_json::to_value(cookie).map_err(runtime_error)?;
                            if let Err(error) = page.set_cookie(cookie_value).await {
                                warnings.push(Warning {
                                    kind: "state.restore".into(),
                                    severity: "warning".into(),
                                    message: format!(
                                        "cookie {:?}: {}",
                                        cookie.name,
                                        redact_str(&error.to_string())
                                    ),
                                    ..Default::default()
                                });
                            }
                        }
                        let local =
                            serde_json::to_string(&entry.local_storage).map_err(runtime_error)?;
                        let session =
                            serde_json::to_string(&entry.session_storage).map_err(runtime_error)?;
                        let script = format!(
                            "(() => {{ for (const [k,v] of Object.entries({local})) localStorage.setItem(k,v); for (const [k,v] of Object.entries({session})) sessionStorage.setItem(k,v); return true; }})()"
                        );
                        page.evaluate_script(&script)
                            .await
                            .map(|_| Vec::<String>::new())
                            .map_err(|e| e.to_string())
                    };
                    if let Err(error) = result {
                        warnings.push(Warning {
                            kind: "state.restore".into(),
                            severity: "warning".into(),
                            message: format!("{origin}: {}", redact_str(&error)),
                            ..Default::default()
                        });
                    }
                }
                let metadata = store.metadata(name).map_err(runtime_error)?;
                Ok((
                    Some(json!({"loaded": name, "metadata": metadata})),
                    warnings,
                ))
            }
            _ => unreachable!(),
        }
    }

    fn state_command(&self, frame: &Frame) -> HandlerResult {
        let store = Store::new(
            self.spec.state_store_dir(),
            time::Duration::days(self.spec.state_expire_days),
            None,
        )
        .map_err(runtime_error)?;
        let args = frame.args.as_ref().and_then(Value::as_object);
        match frame.cmd.as_str() {
            "state.list" => Ok((
                Some(json!({"schema_version":1,"states":store.list().map_err(runtime_error)?})),
                Vec::new(),
            )),
            "state.show" => {
                let empty = serde_json::Map::new();
                let name = required_string(args.unwrap_or(&empty), "name")?;
                Ok((
                    Some(
                        serde_json::to_value(store.metadata(name).map_err(runtime_error)?)
                            .map_err(runtime_error)?,
                    ),
                    Vec::new(),
                ))
            }
            "state.clear" => {
                let empty = serde_json::Map::new();
                let name = required_string(args.unwrap_or(&empty), "name")?;
                store.remove(name).map_err(runtime_error)?;
                Ok((Some(json!({"cleared":name})), Vec::new()))
            }
            "state.clean" => {
                let removed = if let Some(days) = args
                    .and_then(|value| value.get("older_than_days"))
                    .and_then(Value::as_i64)
                {
                    store
                        .clean_older_than_at(
                            time::Duration::days(days),
                            time::OffsetDateTime::now_utc(),
                        )
                        .map_err(runtime_error)?
                } else {
                    store
                        .clean_at(time::OffsetDateTime::now_utc())
                        .map_err(runtime_error)?
                };
                Ok((Some(json!({"removed":removed})), Vec::new()))
            }
            _ => unreachable!(),
        }
    }
}

pub fn handler(spec: SessionSpec) -> Result<crate::DaemonHandler, DaemonError> {
    let runtime = DispatchRuntime::new(spec)?;
    Ok(Arc::new(move |frame, operation| {
        runtime.handle(frame, operation)
    }))
}

/// Execute one command in-process through the same typed runtime used by the daemon.
pub fn dispatch_once(spec: SessionSpec, frame: Frame) -> crate::Response {
    match DispatchRuntime::new(spec)
        .and_then(|runtime| runtime.runtime.block_on(runtime.dispatch(frame)))
    {
        Ok((data, warnings)) => crate::success_response(data, warnings),
        Err(error) => crate::Response {
            success: false,
            error: Some(error),
            ..Default::default()
        },
    }
}

fn captured_origin_state(value: &Value) -> Result<(String, OriginState), String> {
    let origin = value
        .get("origin")
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| "state capture returned no origin".to_owned())?
        .to_owned();
    let object_map = |key: &str| {
        value
            .get(key)
            .and_then(Value::as_object)
            .map(|items| {
                items
                    .iter()
                    .filter_map(|(key, value)| {
                        value.as_str().map(|value| (key.clone(), value.to_owned()))
                    })
                    .collect::<std::collections::BTreeMap<_, _>>()
            })
            .unwrap_or_default()
    };
    let mut cookies = Vec::new();
    if let Some(raw) = value.get("cookies").and_then(Value::as_str) {
        for item in raw
            .split(';')
            .map(str::trim)
            .filter(|item| !item.is_empty())
        {
            if let Some((name, cookie_value)) = item.split_once('=') {
                cookies.push(symbrowse_core::state::Cookie {
                    name: name.to_owned(),
                    value: cookie_value.to_owned(),
                    domain: origin_host(&origin),
                    path: "/".to_owned(),
                    expires: -1.0,
                    size: (name.len() + cookie_value.len()) as i64,
                    http_only: false,
                    secure: origin.starts_with("https://"),
                    session: true,
                    same_site: String::new(),
                });
            }
        }
    }
    if cookies.is_empty()
        && let Some(items) = value.get("bidi_cookies").and_then(|raw| {
            raw.as_array()
                .or_else(|| raw.get("cookies").and_then(Value::as_array))
        })
    {
        for item in items {
            let name = item.get("name").and_then(Value::as_str).unwrap_or_default();
            let cookie_value = item
                .get("value")
                .and_then(|raw| {
                    raw.get("value")
                        .and_then(Value::as_str)
                        .or_else(|| raw.as_str())
                })
                .unwrap_or_default();
            if name.is_empty() {
                continue;
            }
            cookies.push(Cookie {
                name: name.to_owned(),
                value: cookie_value.to_owned(),
                domain: item
                    .get("domain")
                    .and_then(Value::as_str)
                    .map(str::to_owned)
                    .unwrap_or_else(|| origin_host(&origin)),
                path: item
                    .get("path")
                    .and_then(Value::as_str)
                    .unwrap_or("/")
                    .to_owned(),
                expires: item.get("expires").and_then(Value::as_f64).unwrap_or(-1.0),
                size: item
                    .get("size")
                    .and_then(Value::as_i64)
                    .unwrap_or_else(|| (name.len() + cookie_value.len()) as i64),
                http_only: item
                    .get("httpOnly")
                    .and_then(Value::as_bool)
                    .unwrap_or(false),
                secure: item.get("secure").and_then(Value::as_bool).unwrap_or(false),
                session: item.get("session").and_then(Value::as_bool).unwrap_or(true),
                same_site: item
                    .get("sameSite")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned(),
            });
        }
    }
    Ok((
        origin,
        OriginState {
            cookies,
            local_storage: object_map("local_storage"),
            session_storage: object_map("session_storage"),
        },
    ))
}

fn origin_host(origin: &str) -> String {
    let rest = origin
        .strip_prefix("https://")
        .or_else(|| origin.strip_prefix("http://"))
        .unwrap_or(origin);
    rest.split([':', '/']).next().unwrap_or_default().to_owned()
}

fn object_args(frame: &Frame) -> Result<&serde_json::Map<String, Value>, DaemonError> {
    frame
        .args
        .as_ref()
        .and_then(Value::as_object)
        .ok_or_else(|| malformed(format!("{} requires an object args payload", frame.cmd)))
}

fn required_string<'a>(
    args: &'a serde_json::Map<String, Value>,
    name: &str,
) -> Result<&'a str, DaemonError> {
    args.get(name)
        .and_then(Value::as_str)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| malformed(format!("missing required argument {name:?}")))
}

fn parse_cache_range(spec: &str) -> Result<(usize, usize), String> {
    let spec = spec.trim();
    let (start, end) = spec.split_once('-').unwrap_or((spec, ""));
    let start = if start.is_empty() {
        0
    } else {
        start
            .parse::<usize>()
            .map_err(|_| format!("invalid range {spec:?}: start must be a positive line number"))?
    };
    if !start.eq(&0) && start < 1 {
        return Err(format!(
            "invalid range {spec:?}: start must be a positive line number"
        ));
    }
    let end = if end.is_empty() {
        0
    } else {
        end.parse::<usize>()
            .map_err(|_| format!("invalid range {spec:?}: end must be >= start"))?
    };
    if end != 0 && end < start {
        return Err(format!("invalid range {spec:?}: end must be >= start"));
    }
    Ok((start, end))
}

fn malformed(message: impl Into<String>) -> DaemonError {
    DaemonError {
        code: codes::MALFORMED_REQUEST.into(),
        message: message.into(),
        ..Default::default()
    }
}

fn runtime_error(error: impl std::fmt::Display) -> DaemonError {
    DaemonError {
        code: codes::OPERATION_FAILED.into(),
        message: redact_str(&error.to_string()),
        ..Default::default()
    }
}

fn fetch_error(error: symbrowse_fetch::FetchError) -> DaemonError {
    let code = match error {
        symbrowse_fetch::FetchError::BlockedDomain(_)
        | symbrowse_fetch::FetchError::BlockedPrivate(_) => codes::PEER_DENIED,
        symbrowse_fetch::FetchError::Timeout => codes::OPERATION_TIMEOUT,
        _ => codes::OPERATION_FAILED,
    };
    DaemonError {
        code: code.into(),
        message: redact_str(&error.to_string()),
        retryable: Some(code == codes::OPERATION_TIMEOUT),
        ..Default::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        io::{Read, Write},
        net::TcpListener,
        thread,
    };

    fn temp_spec(name: &str) -> SessionSpec {
        let root =
            std::env::temp_dir().join(format!("symbrowse-runtime-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        let mut spec = SessionSpec::for_session(name);
        spec.state_dir = root.join("state");
        spec.cache_dir = root.join("cache");
        spec.allow_private = true;
        spec.engine = "static".into();
        spec
    }

    fn http_server(body: &'static [u8], content_type: &'static str) -> String {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind test server");
        let address = listener.local_addr().expect("test server address");
        thread::spawn(move || {
            let Ok((mut stream, _)) = listener.accept() else {
                return;
            };
            let mut request = [0_u8; 4096];
            let _ = stream.read(&mut request);
            let header = format!(
                "HTTP/1.1 200 OK\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                body.len()
            );
            let _ = stream.write_all(header.as_bytes());
            let _ = stream.write_all(body);
        });
        format!("http://{address}")
    }

    #[test]
    fn dispatch_cache_get_returns_full_and_ranged_content() {
        let runtime = DispatchRuntime::new(temp_spec("cache")).expect("runtime");
        let id = runtime
            .output_cache
            .store(b"line one\nline two\nline three")
            .expect("cache store");
        let frame = Frame {
            cmd: "cache.get".into(),
            args: Some(json!({"cache_id": id, "range": "2-3"})),
            ..Frame::default()
        };
        let (data, _) = runtime
            .runtime
            .block_on(runtime.dispatch(frame))
            .expect("cache dispatch");
        let data = data.expect("cache data");
        assert_eq!(data["cache_id"], id);
        assert_eq!(data["content"], "line two\nline three");
    }

    #[test]
    fn dispatch_wayback_snapshots_uses_policy_checked_http() {
        let endpoint = http_server(
            br#"[["timestamp","original","mimetype","statuscode","digest","length"],["20260101120000","https://example.test/a","text/html","200","abc","42"]]"#,
            "application/json",
        );
        let runtime =
            DispatchRuntime::new_with_wayback_url(temp_spec("wayback"), format!("{endpoint}/cdx"))
                .expect("runtime");
        let frame = Frame {
            cmd: "wayback.snapshots".into(),
            args: Some(json!({"url":"https://example.test/a"})),
            ..Frame::default()
        };
        let (data, _) = runtime
            .runtime
            .block_on(runtime.dispatch(frame))
            .expect("wayback dispatch");
        let data = data.expect("wayback data");
        let snapshots = data.as_array().expect("snapshot array");
        assert_eq!(snapshots.len(), 1);
        assert_eq!(snapshots[0]["timestamp"], "20260101120000");
    }

    #[test]
    fn dispatch_static_browser_open_is_engine_backed() {
        let endpoint = http_server(
            b"<html><title>Fixture</title><main>Hello browser</main></html>",
            "text/html",
        );
        let runtime = DispatchRuntime::new(temp_spec("browser")).expect("runtime");
        let frame = Frame {
            cmd: "open".into(),
            args: Some(json!({"url":endpoint})),
            ..Frame::default()
        };
        let (data, _) = runtime
            .runtime
            .block_on(runtime.dispatch(frame))
            .expect("browser dispatch");
        assert!(data.expect("browser data")["content"].as_str().is_some());
    }

    #[test]
    fn fetch_url_can_store_and_cache_full_output() {
        let endpoint = http_server(
            b"<html><body><h1>Long fixture</h1><p>one two three four five six seven eight nine ten eleven twelve</p></body></html>",
            "text/html",
        );
        let runtime = DispatchRuntime::new(temp_spec("cache-roundtrip")).expect("runtime");
        let (data, _) = runtime
            .runtime
            .block_on(runtime.dispatch(Frame {
                cmd: "fetch.url".into(),
                args: Some(json!({
                    "url": endpoint,
                    "store_full_text": true,
                    "char_limit": 20,
                    "max_chars": 20,
                })),
                ..Frame::default()
            }))
            .expect("fetch dispatch");
        let data = data.expect("fetch data");
        let cache_id = data["cache_id"].as_str().expect("cache id").to_owned();
        let (cached, _) = runtime
            .runtime
            .block_on(runtime.dispatch(Frame {
                cmd: "cache.get".into(),
                args: Some(json!({"cache_id": cache_id})),
                ..Frame::default()
            }))
            .expect("cache dispatch");
        assert!(
            cached.expect("cached data")["content"]
                .as_str()
                .is_some_and(|value| { value.contains("Long fixture") && value.len() > 20 })
        );
    }

    #[test]
    fn unknown_commands_are_typed_errors() {
        let runtime = DispatchRuntime::new(temp_spec("unknown")).expect("runtime");
        let error = runtime
            .runtime
            .block_on(runtime.dispatch(Frame {
                cmd: "not.advertised".into(),
                ..Frame::default()
            }))
            .expect_err("unknown command must fail");
        assert_eq!(error.code, codes::UNKNOWN_COMMAND);
    }

    #[test]
    fn state_capture_preserves_storage_and_bidi_cookie_fields() {
        let value = json!({
            "origin": "https://example.test:8443/app",
            "local_storage": {"token": "redacted"},
            "session_storage": {"step": "2"},
            "cookies": "",
            "bidi_cookies": [{
                "name": "sid",
                "value": {"type": "string", "value": "abc"},
                "domain": "example.test",
                "path": "/app",
                "secure": true,
                "httpOnly": true,
                "session": false,
                "sameSite": "lax"
            }]
        });
        let (origin, state) = captured_origin_state(&value).expect("capture parse");
        assert_eq!(origin, "https://example.test:8443/app");
        assert_eq!(state.local_storage["token"], "redacted");
        assert_eq!(state.session_storage["step"], "2");
        assert_eq!(state.cookies[0].name, "sid");
        assert_eq!(state.cookies[0].value, "abc");
        assert!(state.cookies[0].secure);
        assert!(state.cookies[0].http_only);
        assert!(!state.cookies[0].session);
    }
}
