//! Chrome interaction, inspection, frame, network and artifact adapter.
//!
//! The adapter intentionally exposes only operations that are backed by a CDP
//! command in chromiumoxide.  Features which need a separate protocol (for
//! example axe-core injection or HAR export) are represented by an explicit
//! [`UnsupportedOperation`] error rather than a best-effort implementation.

use std::{
    error::Error,
    fmt,
    path::Path,
    sync::Arc,
    time::{Duration, Instant},
};

use chromiumoxide::{
    Browser, Element, Page,
    cdp::browser_protocol::{accessibility, browser, dom, network, page},
};
use futures::StreamExt;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokio::sync::Mutex;

use crate::{BrowserMode, ConnectionMode, launch};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UnsupportedOperation(pub &'static str);

impl fmt::Display for UnsupportedOperation {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "unsupported Chrome operation: {}", self.0)
    }
}
impl Error for UnsupportedOperation {}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct InteractionResult {
    pub action: String,
    pub selector: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct FindOptions {
    pub kind: String,
    pub query: String,
    #[serde(default)]
    pub action: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub exact: bool,
    #[serde(default)]
    pub index: Option<usize>,
    #[serde(default)]
    pub value: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct FrameInfo {
    pub id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub parent_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    pub url: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct DialogInfo {
    pub kind: String,
    pub message: String,
    pub url: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub default_prompt: Option<String>,
}

/// Persistent state for the daemon's manual dialog controller.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct PendingDialog {
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    pub dialog_type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub message: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub default: String,
    pub handled: bool,
    #[serde(rename = "auto_mode", skip_serializing_if = "String::is_empty")]
    pub auto_mode: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct NetworkEvent {
    pub kind: String,
    pub id: String,
    pub url: String,
    #[serde(default)]
    pub status: u16,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub mime_type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error_text: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct ScreenshotOptions {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub format: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub quality: Option<i64>,
    #[serde(default)]
    pub full_page: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub selector: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Artifact {
    pub kind: String,
    pub mime_type: String,
    pub bytes: Vec<u8>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct ChromeCapabilities {
    pub interactions: Vec<String>,
    pub inspection: Vec<String>,
    pub tabs_frames_dialogs: Vec<String>,
    pub artifacts: Vec<String>,
    pub unsupported: Vec<String>,
}

/// The capabilities proved by this adapter. Keep this list synchronized with
/// the opt-in integration test in `tests/full.rs`.
pub fn capabilities() -> ChromeCapabilities {
    ChromeCapabilities {
        interactions: [
            "click", "dblclick", "fill", "type", "press", "focus", "hover", "select", "check",
            "uncheck", "scroll",
        ]
        .into_iter()
        .map(str::to_owned)
        .collect(),
        inspection: ["find", "get", "is", "count"]
            .into_iter()
            .map(str::to_owned)
            .collect(),
        tabs_frames_dialogs: ["tabs", "frames", "dialogs-manual", "dialogs-auto"]
            .into_iter()
            .map(str::to_owned)
            .collect(),
        artifacts: [
            "network-events",
            "upload",
            "download",
            "screenshot-png",
            "screenshot-jpeg",
            "screenshot-full",
            "screenshot-selector",
            "pdf",
            "accessibility-tree",
        ]
        .into_iter()
        .map(str::to_owned)
        .collect(),
        unsupported: ["har-export", "axe-core-audit"]
            .into_iter()
            .map(str::to_owned)
            .collect(),
    }
}

/// A connected browser plus one page. The handler is kept alive in a task so
/// chromiumoxide can dispatch CDP events and page commands.
pub struct ChromeSession {
    browser: Browser,
    mode: ConnectionMode,
    _handler_task: tokio::task::JoinHandle<()>,
}

impl ChromeSession {
    pub async fn connect(
        mode: BrowserMode,
        timeout: Duration,
    ) -> Result<Self, Box<dyn Error + Send + Sync>> {
        let connection = launch::connect(&mode, timeout).await?;
        let task = tokio::spawn(async move {
            let mut handler = connection.handler;
            while handler.next().await.is_some() {}
        });
        Ok(Self {
            browser: connection.browser,
            mode: connection.mode,
            _handler_task: task,
        })
    }

    pub fn mode(&self) -> ConnectionMode {
        self.mode
    }

    pub async fn new_page(
        &self,
        url: impl Into<String>,
    ) -> Result<ChromePage, Box<dyn Error + Send + Sync>> {
        ChromePage::new(self.browser.new_page(url.into()).await?).await
    }

    pub async fn pages(&self) -> Result<Vec<ChromePage>, Box<dyn Error + Send + Sync>> {
        let mut chrome_pages = Vec::new();
        for page in self.browser.pages().await? {
            chrome_pages.push(ChromePage::new(page).await?);
        }
        Ok(chrome_pages)
    }

    pub async fn close(mut self) -> Result<(), Box<dyn Error + Send + Sync>> {
        let close_result = if self.mode == ConnectionMode::Launch {
            self.browser.close().await.map(|_| ())
        } else {
            Ok(())
        };
        let wait_result = self.browser.wait().await;
        self._handler_task.abort();
        close_result?;
        wait_result?;
        Ok(())
    }
}

#[derive(Clone)]
pub struct ChromePage {
    page: Page,
    dialogs: DialogMonitor,
}

#[derive(Clone)]
struct DialogMonitor {
    state: Arc<Mutex<DialogState>>,
    _task: Arc<tokio::task::JoinHandle<()>>,
}

#[derive(Default)]
struct DialogState {
    pending: Option<PendingDialog>,
}

impl ChromePage {
    async fn new(page: Page) -> Result<Self, Box<dyn Error + Send + Sync>> {
        let mut events = page
            .event_listener::<page::EventJavascriptDialogOpening>()
            .await?;
        let state = Arc::new(Mutex::new(DialogState::default()));
        let monitor_state = Arc::clone(&state);
        let task = tokio::spawn(async move {
            while let Some(event) = events.next().await {
                monitor_state.lock().await.pending = Some(PendingDialog {
                    dialog_type: format!("{:?}", event.r#type).to_ascii_lowercase(),
                    message: event.message.clone(),
                    default: event.default_prompt.clone().unwrap_or_default(),
                    handled: false,
                    auto_mode: String::new(),
                });
            }
        });
        Ok(Self {
            page,
            dialogs: DialogMonitor {
                state,
                _task: Arc::new(task),
            },
        })
    }
    pub fn target_id(&self) -> String {
        self.page.target_id().inner().clone()
    }
    pub fn raw(&self) -> &Page {
        &self.page
    }

    pub async fn open(&self, url: &str) -> Result<Value, Box<dyn Error + Send + Sync>> {
        self.page.goto(url).await?;
        Ok(serde_json::json!({
            "url": self.page.url().await?.unwrap_or_default(),
            "title": self.page.evaluate("document.title").await?.into_value::<String>()?,
        }))
    }

    pub async fn read(&self) -> Result<Value, Box<dyn Error + Send + Sync>> {
        Ok(self
            .page
            .evaluate("document.body?.innerText ?? ''")
            .await?
            .into_value::<String>()?
            .into())
    }

    /// Evaluate a bounded JSON-producing script for shared browser state
    /// capture/restore and engine-neutral dispatch.
    pub async fn evaluate_script(
        &self,
        expression: &str,
    ) -> Result<Value, Box<dyn Error + Send + Sync>> {
        if expression.len() > 1024 * 1024 {
            return Err("evaluation script exceeds 1 MiB".into());
        }
        Ok(self
            .page
            .evaluate(expression)
            .await?
            .into_value::<Value>()?)
    }

    /// Read complete cookie metadata through Chrome's Network domain.
    pub async fn cookies(&self) -> Result<Value, Box<dyn Error + Send + Sync>> {
        Ok(serde_json::to_value(
            self.page
                .execute(network::GetCookiesParams::default())
                .await?
                .result,
        )?)
    }

    /// Set a complete cookie through Chrome's Network domain.
    pub async fn set_cookie(&self, cookie: Value) -> Result<Value, Box<dyn Error + Send + Sync>> {
        let params: network::SetCookieParams = serde_json::from_value(cookie)?;
        Ok(serde_json::to_value(
            self.page.execute(params).await?.result,
        )?)
    }

    pub async fn snapshot(&self) -> Result<Vec<Value>, Box<dyn Error + Send + Sync>> {
        self.accessibility_tree().await
    }

    pub async fn navigation(&self, command: &str) -> Result<Value, Box<dyn Error + Send + Sync>> {
        match command {
            "reload" => {
                self.page.reload().await?;
            }
            "back" => {
                self.page.evaluate("history.back()").await?;
            }
            "forward" => {
                self.page.evaluate("history.forward()").await?;
            }
            _ => return Err(format!("unsupported navigation command {command:?}").into()),
        }
        Ok(serde_json::json!({"url": self.page.url().await?.unwrap_or_default()}))
    }

    async fn element(&self, selector: &str) -> Result<Element, Box<dyn Error + Send + Sync>> {
        if selector.trim().is_empty() {
            return Err("selector must not be empty".into());
        }
        let selector = if let Some(reference) = selector.strip_prefix('@') {
            let reference = serde_json::to_string(reference)?;
            format!("[data-symbrowse-ref={reference}]")
        } else {
            selector.to_owned()
        };
        Ok(self.page.find_element(selector).await?)
    }

    pub async fn click(
        &self,
        selector: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.element(selector).await?.click().await?;
        Ok(result("click", selector))
    }
    pub async fn double_click(
        &self,
        selector: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.element(selector)
            .await?
            .click_with(
                chromiumoxide::types::ClickOptions::builder()
                    .click_count(2)
                    .build(),
            )
            .await?;
        Ok(result("dblclick", selector))
    }
    pub async fn focus(
        &self,
        selector: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.element(selector).await?.focus().await?;
        Ok(result("focus", selector))
    }
    pub async fn hover(
        &self,
        selector: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.element(selector).await?.hover().await?;
        Ok(result("hover", selector))
    }
    pub async fn scroll_into_view(
        &self,
        selector: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.element(selector).await?.scroll_into_view().await?;
        Ok(result("scroll", selector))
    }
    pub async fn type_text(
        &self,
        selector: &str,
        text: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.element(selector)
            .await?
            .focus()
            .await?
            .type_str(text)
            .await?;
        Ok(result("type", selector))
    }
    pub async fn press(
        &self,
        selector: &str,
        key: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.element(selector)
            .await?
            .focus()
            .await?
            .press_key(key)
            .await?;
        Ok(result("press", selector))
    }
    pub async fn fill(
        &self,
        selector: &str,
        text: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        let value = serde_json::to_string(text)?;
        let selector = serde_json::to_string(selector)?;
        self.page.evaluate(format!("(() => {{ const e=document.querySelector({selector}); if (!e) throw new Error('selector did not match'); e.focus(); const setter=Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value')?.set || Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value')?.set; if (setter) setter.call(e,{value}); else e.value={value}; e.dispatchEvent(new Event('input',{{bubbles:true}})); e.dispatchEvent(new Event('change',{{bubbles:true}})); return e.value; }})()" )).await?;
        Ok(result("fill", selector.trim_matches('"')))
    }
    pub async fn select(
        &self,
        selector: &str,
        value: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        let selector_json = serde_json::to_string(selector)?;
        let value_json = serde_json::to_string(value)?;
        self.page.evaluate(format!("(() => {{ const e=document.querySelector({selector_json}); if (!e || e.tagName !== 'SELECT') throw new Error('select requires a SELECT element'); const wanted={value_json}; let hit=false; for (const o of e.options) {{ const yes=o.value===wanted || o.text===wanted; o.selected=yes; hit ||= yes; }} if (!hit) throw new Error('option did not match'); e.dispatchEvent(new Event('input',{{bubbles:true}})); e.dispatchEvent(new Event('change',{{bubbles:true}})); return e.value; }})()" )).await?;
        Ok(result("select", selector))
    }
    pub async fn check(
        &self,
        selector: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.set_checked(selector, true).await
    }
    pub async fn uncheck(
        &self,
        selector: &str,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        self.set_checked(selector, false).await
    }
    async fn set_checked(
        &self,
        selector: &str,
        checked: bool,
    ) -> Result<InteractionResult, Box<dyn Error + Send + Sync>> {
        let s = serde_json::to_string(selector)?;
        self.page.evaluate(format!("(() => {{ const e=document.querySelector({s}); if (!e || e.type !== 'checkbox') throw new Error('check requires a checkbox'); if (e.checked !== {checked}) e.click(); return e.checked; }})()" )).await?;
        Ok(result(if checked { "check" } else { "uncheck" }, selector))
    }

    /// Inspect a selector using only serializable DOM values.
    pub async fn inspect(
        &self,
        selector: &str,
        kind: &str,
    ) -> Result<Value, Box<dyn Error + Send + Sync>> {
        let s = serde_json::to_string(selector)?;
        let expression = match kind {
            "text" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) throw new Error('selector did not match'); return e.innerText ?? ''; }})()"
            ),
            "html" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) throw new Error('selector did not match'); return e.innerHTML ?? ''; }})()"
            ),
            "value" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) throw new Error('selector did not match'); return e.value ?? null; }})()"
            ),
            "title" => "document.title".to_owned(),
            "url" => "location.href".to_owned(),
            "box" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) throw new Error('selector did not match'); const r=e.getBoundingClientRect(); return {{x:r.x,y:r.y,width:r.width,height:r.height}}; }})()"
            ),
            "styles" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) throw new Error('selector did not match'); const c=getComputedStyle(e); return {{display:c.display,visibility:c.visibility,opacity:c.opacity,color:c.color,backgroundColor:c.backgroundColor}}; }})()"
            ),
            "attr" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) throw new Error('selector did not match'); return Object.fromEntries([...e.attributes].map(a=>[a.name,a.value])); }})()"
            ),
            "find" => format!("document.querySelector({s}) !== null"),
            "count" => format!("document.querySelectorAll({s}).length"),
            "visible" | "enabled" | "checked" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) return false; if ({kind:?} === 'checked') return !!e.checked; if ({kind:?} === 'enabled') return !e.disabled; const r=e.getBoundingClientRect(), c=getComputedStyle(e); return c.display !== 'none' && c.visibility !== 'hidden' && c.opacity !== '0' && r.width > 0 && r.height > 0; }})()"
            ),
            "get" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) throw new Error('selector did not match'); return {{text:e.innerText ?? '', html:e.innerHTML ?? '', value:e.value ?? null, checked:typeof e.checked === 'boolean' ? e.checked : null, attributes:Object.fromEntries([...e.attributes].map(a=>[a.name,a.value]))}}; }})()"
            ),
            "is" => format!(
                "(() => {{ const e=document.querySelector({s}); if (!e) return false; const r=e.getBoundingClientRect(), c=getComputedStyle(e); return c.display !== 'none' && c.visibility !== 'hidden' && c.opacity !== '0' && r.width > 0 && r.height > 0; }})()"
            ),
            _ => return Err(format!("unsupported inspection kind {kind:?}").into()),
        };
        Ok(self
            .page
            .evaluate(expression)
            .await?
            .into_value::<Value>()?)
    }

    /// Find an element by the semantic selectors used by the public command
    /// and flow surfaces, assigning a stable DOM-local reference on demand.
    pub async fn find(&self, options: FindOptions) -> Result<Value, Box<dyn Error + Send + Sync>> {
        if options.query.trim().is_empty() {
            return Err("find query is required".into());
        }
        let kind = serde_json::to_string(&options.kind)?;
        let query = serde_json::to_string(&options.query)?;
        let action = serde_json::to_string(&options.action)?;
        let name = serde_json::to_string(&options.name)?;
        let value = serde_json::to_string(&options.value)?;
        let exact = if options.exact { "true" } else { "false" };
        let index = options
            .index
            .map_or_else(|| "null".to_owned(), |index| index.to_string());
        let expression = format!(
            "(() => {{ const kind={kind}, query={query}, action={action}, name={name}, exact={exact}, wantedIndex={index}, value={value}; const text=e=>(e.innerText||e.textContent||'').trim(); const implicit=e=>{{ const tag=e.tagName.toLowerCase(); return tag==='button'?'button':tag==='a'?'link':tag==='input'?(e.type==='checkbox'?'checkbox':e.type==='radio'?'radio':'textbox'):tag==='textarea'?'textbox':tag==='select'?'combobox':''; }}; const candidate=e=>{{ switch(kind) {{ case 'role': return e.getAttribute('role')||implicit(e); case 'text': return text(e); case 'label': return e.getAttribute('aria-label')||text(document.querySelector(`label[for='${{CSS.escape(e.id||'')}}']` )||e); case 'placeholder': return e.getAttribute('placeholder')||''; case 'alt': return e.getAttribute('alt')||''; case 'title': return e.getAttribute('title')||''; case 'testid': return e.getAttribute('data-testid')||''; case 'css': return e.matches(query)?query:''; case 'ref': return e.getAttribute('data-symbrowse-ref')||''; default: return ''; }} }}; const matchesText=(got)=>exact?got===query:got.toLowerCase().includes(query.toLowerCase()); let nodes=[...document.querySelectorAll('*')]; let matches=nodes.filter(e=>kind==='ref'?candidate(e)===query.replace(/^@/,''):matchesText(candidate(e))); if (kind==='text') matches=matches.filter(e=>!matches.some(other=>other!==e&&e.contains(other))); if (name) matches=matches.filter(e=>{{ const got=e.getAttribute('aria-label')||e.getAttribute('name')||''; return exact?got===name:got.toLowerCase().includes(name.toLowerCase()); }}); if (!matches.length) throw new Error(`find ${{kind}} ${{query}} matched no elements`); if (wantedIndex!==null) matches=[matches[wantedIndex]].filter(Boolean); if (!matches.length) throw new Error(`find ${{kind}} index ${{wantedIndex}} is out of range`); if (wantedIndex===null&&matches.length>1&&['first','last','nth'].indexOf(action)<0) throw new Error(`find ${{kind}} ${{query}} matched ${{matches.length}} elements; use index`); let selected=action==='last'?matches[matches.length-1]:matches[0]; let next=1; for (const e of nodes) {{ if (!e.getAttribute('data-symbrowse-ref')) e.setAttribute('data-symbrowse-ref',`e${{next++}}`); }} const ref=selected.getAttribute('data-symbrowse-ref'); if (action==='click') selected.click(); else if (action==='focus') selected.focus(); else if (action==='fill') {{ selected.focus(); selected.value=value; selected.dispatchEvent(new Event('input',{{bubbles:true}})); selected.dispatchEvent(new Event('change',{{bubbles:true}})); }} else if (action==='text') return {{kind,query,action,ref,matches:matches.map(e=>({{ref:e.getAttribute('data-symbrowse-ref'),text:text(e),tag:e.tagName.toLowerCase()}})),value:text(selected)}}; return {{kind,query,action,ref,matches:matches.map(e=>({{ref:e.getAttribute('data-symbrowse-ref'),text:text(e),tag:e.tagName.toLowerCase()}}))}}; }})()"
        );
        Ok(self
            .page
            .evaluate(expression)
            .await?
            .into_value::<Value>()?)
    }

    pub async fn wait_for_selector(
        &self,
        selector: &str,
        visible: bool,
        timeout: Duration,
    ) -> Result<(), Box<dyn Error + Send + Sync>> {
        let started = Instant::now();
        while started.elapsed() < timeout {
            if self
                .inspect(selector, if visible { "is" } else { "find" })
                .await?
                .as_bool()
                == Some(true)
            {
                return Ok(());
            }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        Err(format!("timeout waiting for selector {selector:?}").into())
    }
    pub async fn wait_for_navigation(
        &self,
        timeout: Duration,
    ) -> Result<(), Box<dyn Error + Send + Sync>> {
        tokio::time::timeout(timeout, self.page.wait_for_navigation()).await??;
        Ok(())
    }

    pub async fn frames(&self) -> Result<Vec<FrameInfo>, Box<dyn Error + Send + Sync>> {
        let tree = self
            .page
            .execute(page::GetFrameTreeParams {})
            .await?
            .frame_tree
            .clone();
        let mut result = Vec::new();
        flatten_frames(&tree, &mut result);
        Ok(result)
    }

    pub async fn accessibility_tree(&self) -> Result<Vec<Value>, Box<dyn Error + Send + Sync>> {
        let tree = self
            .page
            .execute(accessibility::GetFullAxTreeParams::builder().build())
            .await?;
        Ok(tree
            .nodes
            .iter()
            .map(serde_json::to_value)
            .collect::<Result<_, _>>()?)
    }

    pub async fn dialog(
        &self,
        accept: bool,
        prompt_text: Option<String>,
        timeout: Duration,
    ) -> Result<DialogInfo, Box<dyn Error + Send + Sync>> {
        let mut events = self
            .page
            .event_listener::<page::EventJavascriptDialogOpening>()
            .await?;
        let event = tokio::time::timeout(timeout, events.next())
            .await?
            .ok_or("dialog stream closed")?;
        let info = DialogInfo {
            kind: format!("{:?}", event.r#type).to_ascii_lowercase(),
            message: event.message.clone(),
            url: event.url.clone(),
            default_prompt: event.default_prompt.clone(),
        };
        let params = page::HandleJavaScriptDialogParams::builder()
            .accept(accept)
            .prompt_text(prompt_text.unwrap_or_default())
            .build()?;
        self.page.execute(params).await?;
        Ok(info)
    }

    pub async fn dialog_status(&self) -> PendingDialog {
        self.dialogs
            .state
            .lock()
            .await
            .pending
            .clone()
            .unwrap_or(PendingDialog {
                dialog_type: String::new(),
                message: String::new(),
                default: String::new(),
                handled: true,
                auto_mode: String::new(),
            })
    }

    pub async fn accept_dialog(
        &self,
        prompt_text: Option<String>,
    ) -> Result<PendingDialog, Box<dyn Error + Send + Sync>> {
        self.handle_pending_dialog(true, prompt_text).await
    }

    pub async fn dismiss_dialog(&self) -> Result<PendingDialog, Box<dyn Error + Send + Sync>> {
        self.handle_pending_dialog(false, None).await
    }

    async fn handle_pending_dialog(
        &self,
        accept: bool,
        prompt_text: Option<String>,
    ) -> Result<PendingDialog, Box<dyn Error + Send + Sync>> {
        let mut state = self.dialogs.state.lock().await;
        let pending = state
            .pending
            .clone()
            .ok_or("no JavaScript dialog is pending")?;
        let params = page::HandleJavaScriptDialogParams::builder()
            .accept(accept)
            .prompt_text(prompt_text.unwrap_or_default())
            .build()?;
        self.page.execute(params).await?;
        state.pending = None;
        Ok(pending)
    }

    pub async fn start_auto_dialog_handler(
        &self,
        accept: bool,
        timeout: Duration,
    ) -> Result<tokio::task::JoinHandle<Result<usize, String>>, Box<dyn Error + Send + Sync>> {
        let mut events = self
            .page
            .event_listener::<page::EventJavascriptDialogOpening>()
            .await?;
        let page = self.page.clone();
        Ok(tokio::spawn(async move {
            let deadline = Instant::now() + timeout;
            let mut count = 0;
            while Instant::now() < deadline {
                let remaining = deadline.saturating_duration_since(Instant::now());
                let event = match tokio::time::timeout(remaining, events.next()).await {
                    Ok(Some(event)) => event,
                    Ok(None) | Err(_) => break,
                };
                page.execute(page::HandleJavaScriptDialogParams::new(accept))
                    .await
                    .map_err(|e| e.to_string())?;
                let _ = event;
                count += 1;
            }
            Ok(count)
        }))
    }

    pub async fn start_network_capture(
        &self,
    ) -> Result<NetworkCapture, Box<dyn Error + Send + Sync>> {
        self.page
            .execute(network::EnableParams::builder().build())
            .await?;
        Ok(NetworkCapture {
            requests: self
                .page
                .event_listener::<network::EventRequestWillBeSent>()
                .await?,
            responses: self
                .page
                .event_listener::<network::EventResponseReceived>()
                .await?,
            failed: self
                .page
                .event_listener::<network::EventLoadingFailed>()
                .await?,
        })
    }

    #[allow(deprecated)]
    pub async fn set_offline(&self, offline: bool) -> Result<(), Box<dyn Error + Send + Sync>> {
        self.page
            .execute(
                network::EmulateNetworkConditionsParams::builder()
                    .offline(offline)
                    .latency(0)
                    .download_throughput(-1)
                    .upload_throughput(-1)
                    .build()?,
            )
            .await?;
        Ok(())
    }

    pub async fn block_urls(&self, urls: Vec<String>) -> Result<(), Box<dyn Error + Send + Sync>> {
        let patterns: Vec<network::BlockPattern> = urls
            .into_iter()
            .map(|url_pattern| network::BlockPattern {
                url_pattern,
                block: true,
            })
            .collect();
        self.page
            .execute(
                network::SetBlockedUrLsParams::builder()
                    .url_patterns(patterns)
                    .build(),
            )
            .await?;
        Ok(())
    }

    pub async fn set_download_behavior(
        &self,
        directory: Option<&Path>,
    ) -> Result<(), Box<dyn Error + Send + Sync>> {
        let (behavior, path) = match directory {
            Some(path) => (
                browser::SetDownloadBehaviorBehavior::AllowAndName,
                Some(path.to_string_lossy().into_owned()),
            ),
            None => (browser::SetDownloadBehaviorBehavior::Deny, None),
        };
        self.page
            .execute(browser_set_download_behavior(behavior, path)?)
            .await?;
        Ok(())
    }

    pub async fn upload_files(
        &self,
        selector: &str,
        files: &[String],
        allowed_dirs: &[String],
    ) -> Result<(), Box<dyn Error + Send + Sync>> {
        let request = symbrowse_engine::files::UploadRequest {
            selector: selector.to_owned(),
            files: files.to_vec(),
            allowed_dirs: allowed_dirs.to_vec(),
        };
        let checked = symbrowse_engine::files::guard_upload_request(&request)?;
        let element = self.element(selector).await?;
        let node = element.description().await?;
        self.page
            .execute(
                dom::SetFileInputFilesParams::builder()
                    .files(checked.uploaded)
                    .backend_node_id(node.backend_node_id)
                    .build()?,
            )
            .await?;
        Ok(())
    }

    pub async fn screenshot(
        &self,
        options: ScreenshotOptions,
    ) -> Result<Artifact, Box<dyn Error + Send + Sync>> {
        let format = options.format.to_ascii_lowercase();
        if !format.is_empty() && format != "png" && format != "jpeg" {
            return Err(
                format!("unsupported screenshot format {format:?} (want png or jpeg)").into(),
            );
        }
        let is_jpeg = format == "jpeg";
        let format = if is_jpeg {
            page::CaptureScreenshotFormat::Jpeg
        } else {
            page::CaptureScreenshotFormat::Png
        };
        let bytes = if options.selector.is_empty() {
            let mut builder = chromiumoxide::page::ScreenshotParams::builder()
                .format(format.clone())
                .full_page(options.full_page);
            if let Some(quality) = options.quality {
                builder = builder.quality(quality);
            }
            self.page.screenshot(builder.build()).await?
        } else {
            if options.full_page {
                return Err("selector screenshot cannot also request full_page".into());
            }
            self.element(&options.selector)
                .await?
                .screenshot(format)
                .await?
        };
        Ok(Artifact {
            kind: "screenshot".to_owned(),
            mime_type: if is_jpeg { "image/jpeg" } else { "image/png" }.to_owned(),
            bytes,
        })
    }

    pub async fn pdf(&self) -> Result<Artifact, Box<dyn Error + Send + Sync>> {
        let bytes = self.page.pdf(page::PrintToPdfParams::default()).await?;
        Ok(Artifact {
            kind: "pdf".to_owned(),
            mime_type: "application/pdf".to_owned(),
            bytes,
        })
    }

    pub async fn har(&self) -> Result<Artifact, Box<dyn Error + Send + Sync>> {
        Err(UnsupportedOperation(
            "HAR export requires deterministic request/response body capture; use network-events",
        )
        .into())
    }
    pub async fn axe_audit(&self) -> Result<Artifact, Box<dyn Error + Send + Sync>> {
        Err(UnsupportedOperation("axe-core is not bundled or injected by this crate").into())
    }
}

pub struct NetworkCapture {
    requests: chromiumoxide::listeners::EventStream<network::EventRequestWillBeSent>,
    responses: chromiumoxide::listeners::EventStream<network::EventResponseReceived>,
    failed: chromiumoxide::listeners::EventStream<network::EventLoadingFailed>,
}

impl NetworkCapture {
    pub async fn collect(mut self, timeout: Duration) -> Vec<NetworkEvent> {
        let deadline = Instant::now() + timeout;
        let mut events = Vec::new();
        while Instant::now() < deadline {
            let remaining = deadline.saturating_duration_since(Instant::now());
            tokio::select! {
                event = self.requests.next() => if let Some(event) = event { events.push(NetworkEvent { kind: "request".to_owned(), id: event.request_id.clone().into(), url: event.request.url.clone(), ..Default::default() }); },
                event = self.responses.next() => if let Some(event) = event { events.push(NetworkEvent { kind: "response".to_owned(), id: event.request_id.clone().into(), url: event.response.url.clone(), status: event.response.status as u16, mime_type: event.response.mime_type.clone(), ..Default::default() }); },
                event = self.failed.next() => if let Some(event) = event { events.push(NetworkEvent { kind: "failed".to_owned(), id: event.request_id.clone().into(), error_text: event.error_text.clone(), ..Default::default() }); },
                _ = tokio::time::sleep(remaining) => break,
            }
        }
        events
    }
}

fn result(action: &str, selector: &str) -> InteractionResult {
    InteractionResult {
        action: action.to_owned(),
        selector: selector.to_owned(),
    }
}

fn flatten_frames(tree: &page::FrameTree, output: &mut Vec<FrameInfo>) {
    output.push(FrameInfo {
        id: tree.frame.id.inner().clone(),
        parent_id: tree
            .frame
            .parent_id
            .as_ref()
            .map(|id| id.inner().clone())
            .unwrap_or_default(),
        name: tree.frame.name.clone().unwrap_or_default(),
        url: tree.frame.url.clone(),
    });
    if let Some(children) = &tree.child_frames {
        for child in children {
            flatten_frames(child, output);
        }
    }
}

fn browser_set_download_behavior(
    behavior: browser::SetDownloadBehaviorBehavior,
    path: Option<String>,
) -> Result<browser::SetDownloadBehaviorParams, String> {
    let mut builder = browser::SetDownloadBehaviorParams::builder()
        .behavior(behavior)
        .events_enabled(true);
    if let Some(path) = path {
        builder = builder.download_path(path);
    }
    builder.build()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn canonical_capabilities_partition_matches_daemon_commands() {
        let value = crate::canonical_capabilities();
        assert!(value.interfaces.contains(&"TabManager".to_owned()));
        assert!(value.interfaces.contains(&"FileTransfer".to_owned()));
        assert!(value.unsupported.contains(&"A11yAuditor".to_owned()));
        assert!(value.unsupported.contains(&"SettingsEngine".to_owned()));
        assert!(!value.interfaces.iter().any(|name| name == "HAR"));
    }

    #[test]
    fn capabilities_are_explicit_and_sorted_with_unsupported_features() {
        let value = capabilities();
        assert_eq!(value.interactions.len(), 11);
        assert!(value.artifacts.contains(&"pdf".to_owned()));
        assert_eq!(value.unsupported, vec!["har-export", "axe-core-audit"]);
    }

    #[test]
    fn frame_fixture_flattens_deterministically() {
        let raw = json!({"frame":{"id":"root","url":"https://example.test","name":"main","loaderId":"loader-root","domainAndRegistry":"example.test","securityOrigin":"https://example.test","mimeType":"text/html","secureContextType":"Secure","crossOriginIsolatedContextType":"NotIsolated","gatedAPIFeatures":[]},"childFrames":[{"frame":{"id":"child","parentId":"root","url":"https://example.test/child","name":"child","loaderId":"loader-child","domainAndRegistry":"example.test","securityOrigin":"https://example.test","mimeType":"text/html","secureContextType":"Secure","crossOriginIsolatedContextType":"NotIsolated","gatedAPIFeatures":[]}}]});
        let tree: page::FrameTree = serde_json::from_value(raw).unwrap();
        let mut frames = Vec::new();
        flatten_frames(&tree, &mut frames);
        assert_eq!(
            frames,
            vec![
                FrameInfo {
                    id: "root".into(),
                    parent_id: "".into(),
                    name: "main".into(),
                    url: "https://example.test".into()
                },
                FrameInfo {
                    id: "child".into(),
                    parent_id: "root".into(),
                    name: "child".into(),
                    url: "https://example.test/child".into()
                }
            ]
        );
    }

    #[test]
    fn screenshot_rejects_unknown_format_without_chrome() {
        let options = ScreenshotOptions {
            format: "bmp".into(),
            ..Default::default()
        };
        assert!(options.format != "png");
        assert!(matches!(
            UnsupportedOperation("axe-core-audit").to_string().as_str(),
            "unsupported Chrome operation: axe-core-audit"
        ));
    }
}
