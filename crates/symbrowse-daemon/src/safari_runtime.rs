#![cfg(target_os = "macos")]

//! Safari daemon dispatch preserves each adapter's supported command surface.
//! This module keeps protocol details out of the portable daemon and never
//! owns a user's ordinary Safari process.

use std::time::Duration;

use serde_json::{Value, json};
use symbrowse_core::{policy::SsrfGuard, state::OriginState};
use symbrowse_engine::{Page, capabilities::Capabilities};
use symbrowse_engine_safari::{
    AttachEngine, BidiEngine, BidiError, DriverOptions, NavigationPolicy, OsascriptRunner,
};

use crate::{DaemonError, Frame, HandlerResult, SessionSpec, Warning, codes};

const MAX_SCRIPT_BYTES: usize = 1024 * 1024;

pub struct SafariRuntime {
    session: SafariSession,
}

enum SafariSession {
    Attach {
        engine: AttachEngine<OsascriptRunner>,
        page: Page,
    },
    Bidi {
        engine: BidiEngine,
        page: Page,
    },
}

impl SafariRuntime {
    pub async fn launch(spec: &SessionSpec) -> Result<Self, DaemonError> {
        let policy = NavigationPolicy::from_allowlist(&spec.allowed_domains);
        if spec.engine == "safari-attach" {
            let mut engine = AttachEngine::default_engine()
                .with_command_timeout(spec.operation_timeout)
                .with_navigation_timeout(spec.operation_timeout)
                .with_navigation_policy(policy);
            // Attach is deliberately opt-in for all script-backed interaction,
            // including state capture and restore.
            engine.set_interactions_opt_in(true);
            engine.check_prerequisites().map_err(runtime_error)?;
            engine.launch().map_err(runtime_error)?;
            let context = engine.new_context().map_err(runtime_error)?;
            let page = engine.new_page(&context).map_err(runtime_error)?;
            return Ok(Self {
                session: SafariSession::Attach { engine, page },
            });
        }
        let options = DriverOptions {
            request_timeout: spec.operation_timeout,
            ready_timeout: spec.operation_timeout,
            session_timeout: spec.operation_timeout.max(Duration::from_secs(60)),
            navigation_policy: bidi_navigation_policy(spec),
            ..DriverOptions::default()
        };
        let engine = BidiEngine::launch(options).await.map_err(runtime_error)?;
        let page = engine.new_page().map_err(runtime_error)?;
        Ok(Self {
            session: SafariSession::Bidi { engine, page },
        })
    }

    #[must_use]
    pub fn planned_capabilities(spec: &SessionSpec) -> Capabilities {
        if spec.engine == "safari-attach" {
            let policy = NavigationPolicy::from_allowlist(&spec.allowed_domains);
            let mut engine = AttachEngine::default_engine().with_navigation_policy(policy);
            engine.set_interactions_opt_in(true);
            engine.capabilities()
        } else {
            BidiEngine::planned_capabilities()
        }
    }

    #[must_use]
    pub fn unsupported_operation(operation: &str) -> DaemonError {
        DaemonError {
            code: "unsupported".into(),
            message: BidiError::Unsupported {
                operation: operation.to_owned(),
            }
            .to_string(),
            hint: "the operation is explicitly unsupported by this engine".into(),
            ..Default::default()
        }
    }

    /// Reject absent adapters before initialization as well as on live sessions.
    #[must_use]
    pub fn bidi_command_unsupported(command: &str) -> bool {
        matches!(
            command,
            "click"
                | "fill"
                | "type"
                | "press"
                | "tabs.list"
                | "tab.list"
                | "tab.new"
                | "tab.switch"
                | "tab.close"
                | "window.new"
                | "frames.list"
                | "frame.tree"
                | "frame.select"
                | "frame.main"
        )
    }

    pub async fn command(&mut self, frame: &Frame, timeout: Duration) -> HandlerResult {
        if matches!(self.session, SafariSession::Bidi { .. })
            && Self::bidi_command_unsupported(&frame.cmd)
        {
            return Err(Self::unsupported_operation(&frame.cmd));
        }
        // Go NavigationRuntime.Handle emits history and limitations only on
        // successful responses, before any command-specific warnings.
        let (data, warnings) = self.command_inner(frame, timeout).await?;
        let mut policy_warnings = self.policy_warnings();
        policy_warnings.extend(warnings);
        Ok((data, policy_warnings))
    }

    fn policy_warnings(&self) -> Vec<Warning> {
        let SafariSession::Bidi { engine, .. } = &self.session else {
            return Vec::new();
        };
        let blocked = engine.blocked_requests();
        let mut warnings = Vec::new();
        if !blocked.is_empty() {
            let total: u64 = blocked.iter().map(|entry| entry.count).sum();
            warnings.push(policy_warning(
                "network_policy",
                format!("domain allowlist blocked {total} request(s)"),
            ));
            for entry in blocked.iter().take(10) {
                warnings.push(policy_warning(
                    "network_policy.blocked",
                    format!(
                        "blocked {} {} ({} requests)",
                        entry.resource_type, entry.url, entry.count
                    ),
                ));
            }
            if blocked.len() > 10 {
                warnings.push(policy_warning(
                    "network_policy.blocked",
                    format!("and {} more blocked URL(s)", blocked.len() - 10),
                ));
            }
        }
        for limitation in engine.limitations() {
            warnings.push(policy_warning(
                "network_policy.limitation",
                limitation.to_owned(),
            ));
        }
        warnings
    }

    async fn command_inner(&mut self, frame: &Frame, timeout: Duration) -> HandlerResult {
        let args = frame
            .args
            .as_ref()
            .and_then(Value::as_object)
            .ok_or_else(|| malformed(format!("{} requires an object args payload", frame.cmd)))?;
        match frame.cmd.as_str() {
            "open" | "goto" => {
                let url = required(args, "url")?;
                let result = self.navigate(url).await.map_err(runtime_error)?;
                Ok((
                    Some(serde_json::to_value(result).map_err(runtime_error)?),
                    Vec::new(),
                ))
            }
            "read" => {
                if let Some(url) = args
                    .get("url")
                    .and_then(Value::as_str)
                    .filter(|v| !v.is_empty())
                {
                    self.navigate(url).await.map_err(runtime_error)?;
                }
                let value = self
                    .eval("document.body?.innerText ?? ''")
                    .await
                    .map_err(runtime_error)?;
                Ok((Some(value), Vec::new()))
            }
            "snapshot" => {
                let value = self.eval("(() => Array.from(document.querySelectorAll('body *')).slice(0, 4096).map((e, i) => ({ref: 'safari-' + i, role: e.getAttribute('role') || e.tagName.toLowerCase(), name: (e.innerText || e.textContent || '').trim().slice(0, 200)})))()").await.map_err(runtime_error)?;
                Ok((Some(json!({"nodes": value})), Vec::new()))
            }
            "click" | "fill" | "type" | "press" => {
                if matches!(self.session, SafariSession::Bidi { .. }) {
                    return Err(Self::unsupported_operation(&frame.cmd));
                }
                let selector = args
                    .get("selector")
                    .and_then(Value::as_str)
                    .unwrap_or("body");
                let selector = selector_json(selector).map_err(malformed)?;
                let value = args.get("value").and_then(Value::as_str).unwrap_or("");
                let key = args.get("key").and_then(Value::as_str).unwrap_or("");
                let script = match frame.cmd.as_str() {
                    "click" => format!(
                        "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.click(); return {{action:'click',selector:{selector}}}; }})()"
                    ),
                    "fill" => format!(
                        "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.focus(); e.value={}; e.dispatchEvent(new Event('input',{{bubbles:true}})); e.dispatchEvent(new Event('change',{{bubbles:true}})); return {{action:'fill',selector:{selector}}}; }})()",
                        serde_json::to_string(value).map_err(runtime_error)?
                    ),
                    "type" => format!(
                        "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.focus(); e.value=(e.value||'')+{}; e.dispatchEvent(new Event('input',{{bubbles:true}})); return {{action:'type',selector:{selector}}}; }})()",
                        serde_json::to_string(value).map_err(runtime_error)?
                    ),
                    "press" => format!(
                        "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.dispatchEvent(new KeyboardEvent('keydown',{{key:{}}})); return {{action:'press',selector:{selector},key:{}}}; }})()",
                        serde_json::to_string(key).map_err(runtime_error)?,
                        serde_json::to_string(key).map_err(runtime_error)?
                    ),
                    _ => unreachable!(),
                };
                let value = self.eval(&script).await.map_err(runtime_error)?;
                Ok((Some(value), Vec::new()))
            }
            command if command.starts_with("get.") || command.starts_with("is.") => {
                let selector = args
                    .get("selector")
                    .and_then(Value::as_str)
                    .unwrap_or("body");
                let kind = command
                    .strip_prefix("get.")
                    .or_else(|| command.strip_prefix("is."))
                    .unwrap_or("text");
                let value = self.inspect(selector, kind).await.map_err(runtime_error)?;
                if command == "get.attr" {
                    let attribute = required(args, "attribute")?;
                    Ok((
                        Some(value.get(attribute).cloned().unwrap_or(Value::Null)),
                        Vec::new(),
                    ))
                } else {
                    Ok((Some(value), Vec::new()))
                }
            }
            "get.box" | "get.styles" => unreachable!(),
            "find" => {
                let kind = args.get("kind").and_then(Value::as_str).unwrap_or("text");
                let query = required(args, "query")?;
                let exact = args.get("exact").and_then(Value::as_bool).unwrap_or(false);
                let value = self.find(kind, query, exact).await.map_err(runtime_error)?;
                Ok((Some(value), Vec::new()))
            }
            "scrollintoview" => {
                let selector = selector_json(required(args, "selector")?).map_err(malformed)?;
                let value = self.eval(&format!("(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.scrollIntoView({{block:'center'}}); return {{action:'scroll',selector:{selector}}}; }})()")).await.map_err(runtime_error)?;
                Ok((Some(value), Vec::new()))
            }
            "back" | "forward" | "reload" => {
                let script = match frame.cmd.as_str() {
                    "back" => "history.back();",
                    "forward" => "history.forward();",
                    _ => "location.reload();",
                };
                self.eval(&format!("(() => {{{script} return location.href; }})()"))
                    .await
                    .map_err(runtime_error)?;
                Ok((
                    Some(json!({"url": self.eval("location.href").await.map_err(runtime_error)?})),
                    Vec::new(),
                ))
            }
            "wait" => {
                let kind = args
                    .get("kind")
                    .and_then(Value::as_str)
                    .unwrap_or("selector");
                let started = std::time::Instant::now();
                loop {
                    if started.elapsed() >= timeout {
                        return Err(DaemonError {
                            code: codes::OPERATION_TIMEOUT.into(),
                            message: "Safari wait timed out".into(),
                            ..Default::default()
                        });
                    }
                    let ready = match kind {
                        "ms" => {
                            let nanos = args.get("duration").and_then(Value::as_u64).unwrap_or(0);
                            tokio::time::sleep(Duration::from_nanos(nanos)).await;
                            true
                        }
                        "url" => self
                            .eval("location.href")
                            .await
                            .map_err(runtime_error)?
                            .as_str()
                            .is_some_and(|url| {
                                args.get("value")
                                    .and_then(Value::as_str)
                                    .is_some_and(|want| url.contains(want))
                            }),
                        _ => {
                            let selector = args
                                .get("value")
                                .or_else(|| args.get("selector"))
                                .and_then(Value::as_str)
                                .unwrap_or("body");
                            self.inspect(selector, "visible")
                                .await
                                .map_err(runtime_error)?
                                .as_bool()
                                == Some(true)
                        }
                    };
                    if ready {
                        return Ok((Some(json!({"ready":true,"kind":kind})), Vec::new()));
                    }
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
            }
            _ => Err(DaemonError {
                code: codes::UNKNOWN_COMMAND.into(),
                message: format!("Safari command {:?} is not implemented", frame.cmd),
                ..Default::default()
            }),
        }
    }

    pub async fn capture(&mut self) -> Result<Value, String> {
        let mut value = self.eval("(() => ({origin: location.origin, local_storage: Object.fromEntries(Object.entries(localStorage)), session_storage: Object.fromEntries(Object.entries(sessionStorage)), cookies: document.cookie}))()").await?;
        if let SafariSession::Bidi { engine, page } = &mut self.session
            && let Ok(cookies) = engine.get_cookies(page).await
        {
            value["bidi_cookies"] = cookies;
        }
        Ok(value)
    }

    pub async fn restore_origin(
        &mut self,
        origin: &str,
        state: &OriginState,
    ) -> Result<Vec<String>, String> {
        self.navigate(origin).await?;
        let local = serde_json::to_string(&state.local_storage).map_err(|e| e.to_string())?;
        let session = serde_json::to_string(&state.session_storage).map_err(|e| e.to_string())?;
        let script = format!(
            "(() => {{ for (const [k,v] of Object.entries({local})) localStorage.setItem(k,v); for (const [k,v] of Object.entries({session})) sessionStorage.setItem(k,v); return true; }})()"
        );
        self.eval(&script).await?;
        let mut warnings = Vec::new();
        for cookie in &state.cookies {
            let cookie_json = serde_json::to_string(cookie).map_err(|e| e.to_string())?;
            match &mut self.session {
                SafariSession::Bidi { engine, page } => {
                    let bidi_cookie = json!({"name":cookie.name,"value":{"type":"string","value":cookie.value},"domain":cookie.domain,"path":cookie.path,"secure":cookie.secure,"httpOnly":cookie.http_only,"sameSite":if cookie.same_site.is_empty(){"lax"}else{&cookie.same_site}});
                    if let Err(error) = engine.set_cookie(bidi_cookie, page).await {
                        warnings.push(format!("cookie {:?}: {error}", cookie.name));
                    }
                }
                SafariSession::Attach { .. } => {
                    let script = format!(
                        "document.cookie = (() => {{ const c={cookie_json}; return c.name + '=' + encodeURIComponent(c.value) + '; Path=' + (c.path || '/') + (c.domain ? '; Domain=' + c.domain : ''); }})() ; true"
                    );
                    if let Err(error) = self.eval(&script).await {
                        warnings.push(format!("cookie {:?}: {error}", cookie.name));
                    }
                }
            }
        }
        Ok(warnings)
    }

    async fn navigate(&mut self, target: &str) -> Result<Value, String> {
        match &mut self.session {
            SafariSession::Attach { engine, page } => Ok(serde_json::to_value(
                engine.navigate(page, target).map_err(|e| e.to_string())?,
            )
            .map_err(|e| e.to_string())?),
            SafariSession::Bidi { engine, page } => Ok(serde_json::to_value(
                engine
                    .navigate(page, target)
                    .await
                    .map_err(|e| e.to_string())?,
            )
            .map_err(|e| e.to_string())?),
        }
    }

    async fn eval(&mut self, expression: &str) -> Result<Value, String> {
        if expression.len() > MAX_SCRIPT_BYTES {
            return Err("evaluation script exceeds 1 MiB".into());
        }
        match &mut self.session {
            SafariSession::Attach { engine, .. } => engine
                .evaluate_script(expression)
                .map_err(|e| e.to_string()),
            SafariSession::Bidi { engine, page } => engine
                .evaluate_script(page, expression)
                .await
                .map_err(|e| e.to_string())
                .and_then(|result| {
                    if !result.exception_text.is_empty() {
                        Err(result.exception_text)
                    } else {
                        Ok(result.value.unwrap_or(Value::Null))
                    }
                }),
        }
    }

    async fn inspect(&mut self, raw_selector: &str, kind: &str) -> Result<Value, String> {
        let selector = selector_json(raw_selector)?;
        let expression = match kind {
            "text" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); return e.innerText || e.textContent || ''; }})()"
            ),
            "html" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); return e.innerHTML || ''; }})()"
            ),
            "value" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); return e.value ?? null; }})()"
            ),
            "title" => "document.title".into(),
            "url" => "location.href".into(),
            "count" => format!("document.querySelectorAll({selector}).length"),
            "box" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); const r=e.getBoundingClientRect(); return {{x:r.x,y:r.y,width:r.width,height:r.height}}; }})()"
            ),
            "styles" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); const c=getComputedStyle(e); return {{display:c.display,visibility:c.visibility,opacity:c.opacity,color:c.color,backgroundColor:c.backgroundColor}}; }})()"
            ),
            "attr" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); return Object.fromEntries([...e.attributes].map(a=>[a.name,a.value])); }})()"
            ),
            "visible" | "enabled" | "checked" => format!(
                "(() => {{ const e=document.querySelector({selector}); if (!e) return false; if ({kind:?} === 'enabled') return !e.disabled; if ({kind:?} === 'checked') return !!e.checked; const r=e.getBoundingClientRect(), c=getComputedStyle(e); return c.display !== 'none' && c.visibility !== 'hidden' && c.opacity !== '0' && r.width > 0 && r.height > 0; }})()"
            ),
            _ => return Err(format!("unsupported inspection kind {kind:?}")),
        };
        self.eval(&expression).await
    }

    async fn find(&mut self, kind: &str, query: &str, exact: bool) -> Result<Value, String> {
        let kind = serde_json::to_string(kind).map_err(|e| e.to_string())?;
        let query = serde_json::to_string(query).map_err(|e| e.to_string())?;
        let exact = if exact { "true" } else { "false" };
        self.eval(&format!("(() => {{ const k={kind}, q={query}, exact={exact}; const text=e=>(e.innerText||e.textContent||'').trim(); const implicit=e=>{{const t=e.tagName.toLowerCase(); return t==='button'?'button':t==='a'?'link':t==='input'?'textbox':t;}}; const candidate=e=>k==='role'?(e.getAttribute('role')||implicit(e)):k==='label'?(e.getAttribute('aria-label')||text(e)):k==='placeholder'?(e.getAttribute('placeholder')||''):k==='text'?text(e):k==='testid'?(e.getAttribute('data-testid')||''):''; const matches=[...document.querySelectorAll('*')].filter(e=>{{const got=candidate(e); return exact?got===q:got.toLowerCase().includes(q.toLowerCase());}}); if (!matches.length) throw new Error('find matched no elements'); const e=matches[0], ref='safari-'+matches.indexOf(e); e.setAttribute('data-symbrowse-ref',ref); return {{ref,kind:k,query:q}}; }})()")).await
    }
}

fn bidi_navigation_policy(spec: &SessionSpec) -> NavigationPolicy {
    NavigationPolicy::from_allowlist(&spec.allowed_domains)
        .with_ssrf_guard(SsrfGuard::new(spec.allow_private))
}

fn policy_warning(kind: &str, message: String) -> Warning {
    Warning {
        kind: kind.to_owned(),
        severity: "warning".to_owned(),
        message,
        ..Warning::default()
    }
}

fn selector_json(selector: &str) -> Result<String, String> {
    let selector = if let Some(reference) = selector.strip_prefix('@') {
        format!(
            "[data-symbrowse-ref={}]",
            serde_json::to_string(reference).map_err(|e| e.to_string())?
        )
    } else {
        selector.to_owned()
    };
    serde_json::to_string(&selector).map_err(|e| e.to_string())
}

fn required<'a>(
    args: &'a serde_json::Map<String, Value>,
    key: &str,
) -> Result<&'a str, DaemonError> {
    args.get(key)
        .and_then(Value::as_str)
        .filter(|v| !v.is_empty())
        .ok_or_else(|| malformed(format!("missing required argument {key:?}")))
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
        message: crate::redact_str(&error.to_string()),
        ..Default::default()
    }
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};

    use symbrowse_engine_safari::{BidiTransport, BoxFuture};

    use super::*;

    #[derive(Clone, Default)]
    struct FakeTransport {
        calls: Arc<Mutex<Vec<String>>>,
    }

    impl BidiTransport for FakeTransport {
        fn command<'a>(&'a mut self, method: &'a str, _params: Value) -> BoxFuture<'a, Value> {
            self.calls
                .lock()
                .expect("calls lock")
                .push(method.to_owned());
            Box::pin(async { Ok(json!({"result": {"type": "string", "value": "fixture title"}})) })
        }

        fn close<'a>(&'a mut self) -> BoxFuture<'a, ()> {
            Box::pin(async { Ok(()) })
        }
    }

    #[test]
    fn bidi_capabilities_are_exposed_by_the_safari_runtime() {
        let capabilities = BidiEngine::planned_capabilities();
        assert_eq!(capabilities.kind, "safari-bidi");
        assert_eq!(
            capabilities.interfaces,
            [
                "InspectionEngine",
                "NavigationStateProvider",
                "NetworkPolicyReporter",
            ]
        );
        assert!(
            capabilities
                .unsupported
                .iter()
                .any(|name| name == "InteractionEngine")
        );
    }

    async fn assert_bidi_interaction_unsupported(command: &str) {
        let fake = FakeTransport::default();
        let engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1");
        let page = engine.new_page().expect("page");
        let mut runtime = SafariRuntime {
            session: SafariSession::Bidi { engine, page },
        };
        let timeout = Duration::from_secs(1);
        let inspection = Frame {
            cmd: "get.title".into(),
            args: Some(json!({"selector": "body"})),
            ..Default::default()
        };
        assert_eq!(
            runtime
                .command(&inspection, timeout)
                .await
                .expect("inspection")
                .0,
            Some(json!("fixture title"))
        );
        assert_eq!(*fake.calls.lock().expect("calls lock"), ["script.evaluate"]);
        fake.calls.lock().expect("calls lock").clear();

        let frame = Frame {
            cmd: command.into(),
            args: Some(json!({"selector": "#target", "value": "text", "key": "Enter"})),
            ..Default::default()
        };
        let result = runtime.command(&frame, timeout).await;
        assert!(
            fake.calls.lock().expect("calls lock").is_empty(),
            "unsupported {command} reached the BiDi transport"
        );
        let error = result.expect_err("BiDi interactions are unsupported");
        assert_eq!(error.code, "unsupported");
        assert_eq!(
            error.message,
            format!("safari-bidi engine: unsupported operation: {command}")
        );
    }

    #[tokio::test]
    async fn bidi_click_is_unsupported_without_script_transport() {
        assert_bidi_interaction_unsupported("click").await;
    }

    #[tokio::test]
    async fn bidi_fill_is_unsupported_without_script_transport() {
        assert_bidi_interaction_unsupported("fill").await;
    }

    #[tokio::test]
    async fn bidi_type_is_unsupported_without_script_transport() {
        assert_bidi_interaction_unsupported("type").await;
    }

    #[tokio::test]
    async fn bidi_press_is_unsupported_without_script_transport() {
        assert_bidi_interaction_unsupported("press").await;
    }
    fn policy_runtime() -> (SafariRuntime, FakeTransport) {
        let fake = FakeTransport::default();
        let engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1")
            .with_navigation_policy(NavigationPolicy::from_allowlist(
                &["allowed.example".into()],
            ));
        let page = engine.new_page().expect("page");
        (
            SafariRuntime {
                session: SafariSession::Bidi { engine, page },
            },
            fake,
        )
    }

    #[tokio::test]
    async fn bidi_tab_and_frame_commands_are_unsupported_without_transport() {
        let (mut runtime, fake) = policy_runtime();
        for command in [
            "tabs.list",
            "tab.list",
            "tab.new",
            "tab.switch",
            "tab.close",
            "window.new",
            "frames.list",
            "frame.tree",
            "frame.select",
            "frame.main",
        ] {
            let error = runtime
                .command(
                    &Frame {
                        cmd: command.into(),
                        ..Default::default()
                    },
                    Duration::from_secs(1),
                )
                .await
                .expect_err("unsupported");
            assert_eq!(error.code, "unsupported");
            assert_eq!(
                error.message,
                format!("safari-bidi engine: unsupported operation: {command}")
            );
        }
        assert!(fake.calls.lock().expect("calls").is_empty());
    }

    #[tokio::test]
    async fn bidi_policy_warnings_reach_each_successful_navigation_and_read_response() {
        let (mut runtime, fake) = policy_runtime();
        for command in ["open", "goto", "read"] {
            let frame = Frame {
                cmd: command.into(),
                args: Some(json!({"url":"https://blocked.example/"})),
                ..Default::default()
            };
            let error = runtime
                .command(&frame, Duration::from_secs(1))
                .await
                .expect_err("denied");
            assert_eq!(error.code, codes::OPERATION_FAILED);
        }
        assert!(fake.calls.lock().expect("calls").is_empty());
        for command in ["open", "goto", "read", "get.title"] {
            let frame = Frame {
                cmd: command.into(),
                args: Some(json!({"url":"https://allowed.example/"})),
                ..Default::default()
            };
            let (_, warnings) = runtime
                .command(&frame, Duration::from_secs(1))
                .await
                .expect("success");
            assert_eq!(warnings.len(), 3);
            assert_eq!(
                serde_json::to_value(&warnings[..2]).expect("JSON"),
                json!([
                    {"kind":"network_policy", "severity":"warning", "message":"domain allowlist blocked 3 request(s)"},
                    {"kind":"network_policy.blocked", "severity":"warning", "message":"blocked document https://blocked.example/ (3 requests)"}
                ])
            );
            assert_eq!(warnings[2].kind, "network_policy.limitation");
            assert_eq!(warnings[2].severity, "warning");
            assert!(warnings[2].message.contains("script-initiated navigations"));
        }
        assert_eq!(
            *fake.calls.lock().expect("calls"),
            [
                "browsingContext.navigate",
                "browsingContext.navigate",
                "browsingContext.navigate",
                "script.evaluate",
                "script.evaluate"
            ]
        );
    }

    #[tokio::test]
    async fn bidi_policy_warnings_include_startup_limitation_and_cap_sorted_url_details() {
        let (mut runtime, fake) = policy_runtime();
        let frame = Frame {
            cmd: "get.title".into(),
            args: Some(json!({})),
            ..Default::default()
        };
        let (_, warnings) = runtime
            .command(&frame, Duration::from_secs(1))
            .await
            .expect("title");
        assert_eq!(warnings.len(), 1);
        assert_eq!(warnings[0].kind, "network_policy.limitation");
        fake.calls.lock().expect("calls").clear();
        for count in [10, 11, 15] {
            let (mut runtime, fake) = policy_runtime();
            for index in (0..count).rev() {
                assert!(
                    runtime
                        .navigate(&format!("https://blocked.example/{index:02}"))
                        .await
                        .is_err()
                );
            }
            let warnings = runtime.policy_warnings();
            assert_eq!(warnings.len(), if count == 10 { 12 } else { 13 });
            assert_eq!(
                warnings[0].message,
                format!("domain allowlist blocked {count} request(s)")
            );
            for (index, warning) in warnings[1..11].iter().enumerate() {
                assert_eq!(
                    warning.message,
                    format!("blocked document https://blocked.example/{index:02} (1 requests)")
                );
            }
            if count > 10 {
                assert_eq!(
                    warnings[11].message,
                    format!("and {} more blocked URL(s)", count - 10)
                );
            }
            assert_eq!(
                warnings.last().expect("limitation").kind,
                "network_policy.limitation"
            );
            assert!(fake.calls.lock().expect("calls").is_empty());
        }
    }

    #[tokio::test]
    async fn bidi_policy_errors_do_not_turn_into_successful_warning_responses() {
        let (mut runtime, fake) = policy_runtime();
        let bad = Frame {
            cmd: "open".into(),
            args: Some(json!({})),
            ..Default::default()
        };
        assert_eq!(
            runtime
                .command(&bad, Duration::from_secs(1))
                .await
                .expect_err("missing URL")
                .code,
            codes::MALFORMED_REQUEST
        );
        assert!(fake.calls.lock().expect("calls").is_empty());
        if let SafariSession::Bidi { engine, .. } = &mut runtime.session {
            engine.close().await.expect("close");
        }
        let read = Frame {
            cmd: "read".into(),
            args: Some(json!({})),
            ..Default::default()
        };
        assert_eq!(
            runtime
                .command(&read, Duration::from_secs(1))
                .await
                .expect_err("closed")
                .code,
            codes::OPERATION_FAILED
        );
        assert!(fake.calls.lock().expect("calls").is_empty());
    }

    #[tokio::test]
    async fn bidi_launch_policy_reports_ssrf_denials_and_respects_allow_private() {
        for allow_private in [false, true] {
            let fake = FakeTransport::default();
            let spec = SessionSpec {
                allow_private,
                ..SessionSpec::for_session("policy-test")
            };
            let engine = BidiEngine::from_transport(Box::new(fake.clone()), "page-1")
                .with_navigation_policy(bidi_navigation_policy(&spec));
            let page = engine.new_page().expect("page");
            let mut runtime = SafariRuntime {
                session: SafariSession::Bidi { engine, page },
            };
            let frame = Frame {
                cmd: "open".into(),
                args: Some(json!({"url":"http://127.0.0.1/"})),
                ..Default::default()
            };
            let result = runtime.command(&frame, Duration::from_secs(1)).await;
            if allow_private {
                assert_eq!(result.expect("private explicitly allowed").1.len(), 1);
                assert_eq!(
                    *fake.calls.lock().expect("calls"),
                    ["browsingContext.navigate"]
                );
            } else {
                assert_eq!(
                    result.expect_err("SSRF denied").code,
                    codes::OPERATION_FAILED
                );
                assert!(fake.calls.lock().expect("calls").is_empty());
                let SafariSession::Bidi { engine, .. } = &runtime.session else {
                    unreachable!()
                };
                assert_eq!(engine.blocked_requests()[0].reason, "ssrf guard");
                assert_eq!(
                    runtime.policy_warnings()[1].message,
                    "blocked document http://127.0.0.1/ (1 requests)"
                );
            }
        }
    }
}
