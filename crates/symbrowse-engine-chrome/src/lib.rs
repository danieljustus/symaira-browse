#![deny(unsafe_code)]

//! Feasibility adapter for Chrome DevTools Protocol.
//!
//! This is intentionally a small, protocol-facing spike. It proves the
//! launch/attach lifecycle and a few observable CDP operations without
//! claiming that the full Go engine has been ported.

mod connection;
mod events;
mod full;
mod launch;

pub use full::{
    Artifact, ChromeCapabilities, ChromePage, ChromeSession, DialogInfo, FindOptions, FrameInfo,
    InteractionResult, NetworkCapture, NetworkEvent, ScreenshotOptions, UnsupportedOperation,
    capabilities,
};

use std::{path::PathBuf, time::Duration};

use chromiumoxide::cdp::browser_protocol::{accessibility, page::CaptureScreenshotFormat};
use futures::StreamExt;
use serde::Serialize;
use serde_json::Value;

pub use connection::ConnectionMode;
pub use launch::{discover_chrome_executable, resolve_chrome_executable};

#[derive(Clone, Debug)]
pub enum BrowserMode {
    Launch {
        executable: PathBuf,
        user_data_dir: PathBuf,
        headless: bool,
    },
    Attach {
        endpoint: String,
    },
}

#[derive(Clone, Debug)]
pub struct ProbeConfig {
    pub mode: BrowserMode,
    pub url: String,
    pub timeout: Duration,
}

#[derive(Debug, Serialize)]
pub struct ProbeReport {
    pub mode: ConnectionMode,
    pub url: String,
    pub target_id: String,
    pub title: String,
    pub evaluated_value: Value,
    pub ax_node_count: usize,
    pub screenshot_bytes: usize,
    pub event_count: usize,
}

/// Launch or attach to Chromium and exercise real CDP operations.
pub async fn run_probe(
    config: ProbeConfig,
) -> Result<ProbeReport, Box<dyn std::error::Error + Send + Sync>> {
    let connection = launch::connect(&config.mode, config.timeout).await?;
    let mode = connection.mode;
    let mut browser = connection.browser;
    let event_counter = events::spawn_counter(connection.handler);

    let result = async {
        let page = browser.new_page("about:blank").await?;
        let mut load_events = page
            .event_listener::<chromiumoxide::cdp::browser_protocol::page::EventLoadEventFired>()
            .await?;
        page.goto(config.url.as_str()).await?;
        let event_count = usize::from(
            tokio::time::timeout(config.timeout.min(Duration::from_secs(1)), load_events.next())
                .await
                .ok()
                .flatten()
                .is_some(),
        );

        let title = page.evaluate("document.title").await?.into_value::<String>()?;
        let evaluated_value = page
            .evaluate("({heading: document.querySelector('h1')?.textContent ?? '', ready: document.readyState})")
            .await?
            .into_value::<Value>()?;
        let ax = page
            .execute(accessibility::GetFullAxTreeParams::builder().build())
            .await?;
        let screenshot = page
            .screenshot(
                chromiumoxide::page::ScreenshotParams::builder()
                    .format(CaptureScreenshotFormat::Png)
                    .build(),
            )
            .await?;

        Ok::<_, Box<dyn std::error::Error + Send + Sync>>(ProbeReport {
            mode,
            url: page.url().await?.unwrap_or_default(),
            target_id: page.target_id().inner().clone(),
            title,
            evaluated_value,
            ax_node_count: ax.nodes.len(),
            screenshot_bytes: screenshot.len(),
            event_count: event_count.max(event_counter.load(std::sync::atomic::Ordering::Relaxed)),
        })
    }
    .await;

    let close_error = if mode == ConnectionMode::Launch {
        browser.close().await.err()
    } else {
        None
    };
    let wait_error = browser.wait().await.err();
    if result.is_ok() {
        if let Some(error) = close_error {
            return Err(error.into());
        }
        if let Some(error) = wait_error {
            return Err(error.into());
        }
    }
    result
}
