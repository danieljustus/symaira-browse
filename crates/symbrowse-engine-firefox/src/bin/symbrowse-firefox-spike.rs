use std::{env, path::PathBuf, time::Duration};
use symbrowse_engine_firefox::{
    FirefoxSession, canonical_capabilities, resolve_firefox_executable,
};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    if env::var_os("SYMBROWSE_E2E").is_none() {
        println!("Firefox native spike requires SYMBROWSE_E2E=1");
        return Ok(());
    }
    let executable = resolve_firefox_executable(
        env::var_os("SYMBROWSE_FIREFOX_EXECUTABLE")
            .as_deref()
            .map(PathBuf::from)
            .as_deref(),
    )?;
    let profile = env::temp_dir().join(format!("symbrowse-firefox-spike-{}", std::process::id()));
    let mut session = FirefoxSession::launch(executable, profile, Duration::from_secs(15)).await?;
    let target = env::var("SYMBROWSE_FIREFOX_FIXTURE_URL").unwrap_or_else(|_| "about:blank".into());
    if target != "about:blank" {
        let navigation = session.navigate(&target).await?;
        eprintln!("Firefox navigation settled at {}", navigation.url);
    }
    let result = session
        .evaluate("({ready: document.readyState, url: location.href})")
        .await?;
    eprintln!(
        "Firefox BiDi native spike connected: {}",
        result.value.unwrap_or_default()
    );
    session.close().await?;
    Ok(())
}

#[allow(dead_code)]
fn _capabilities() {
    let _ = canonical_capabilities();
}
