#![cfg(target_os = "macos")]

//! Safari daemon dispatch preserves each adapter's supported command surface.
//! This module keeps protocol details out of the portable daemon and never
//! owns a user's ordinary Safari process.

use std::time::Duration;

use serde_json::{Value, json};
use symbrowse_core::state::OriginState;
use symbrowse_engine::Page;
use symbrowse_engine_safari::{
    AttachEngine, BidiEngine, BidiError, DriverOptions, NavigationPolicy, OsascriptRunner,
};

use crate::{DaemonError, Frame, HandlerResult, SessionSpec, codes};

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
            navigation_policy: policy,
            ..DriverOptions::default()
        };
        let engine = BidiEngine::launch(options).await.map_err(runtime_error)?;
        let page = engine.new_page().map_err(runtime_error)?;
        Ok(Self {
            session: SafariSession::Bidi { engine, page },
        })
    }

    pub async fn command(&mut self, frame: &Frame, timeout: Duration) -> HandlerResult {
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
                    return Err(DaemonError {
                        code: "unsupported".into(),
                        message: BidiError::Unsupported {
                            operation: frame.cmd.clone(),
                        }
                        .to_string(),
                        hint: "the operation is explicitly unsupported by this engine".into(),
                        ..Default::default()
                    });
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
}
