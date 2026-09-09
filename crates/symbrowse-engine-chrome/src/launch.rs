use std::{
    env,
    path::{Path, PathBuf},
    time::Duration,
};

use chromiumoxide::{Browser, BrowserConfig};

use crate::{
    BrowserMode,
    connection::{Connected, ConnectionMode},
};

const CHROME_ENV: &str = "SYMBROWSE_CHROME_EXECUTABLE";

/// Resolve a usable Chrome/Chromium executable without requiring host-specific
/// environment configuration. An explicit environment value remains strict:
/// a typo must fail instead of silently selecting another browser.
///
/// The ordered candidates cover the standard macOS app bundles, Linux package
/// locations, and Windows installation roots. PATH is checked last so a
/// package-manager shim remains supported on every platform.
pub fn discover_chrome_executable() -> Result<PathBuf, String> {
    if let Some(value) = env::var_os(CHROME_ENV) {
        let path = PathBuf::from(value);
        return usable_executable(&path).ok_or_else(|| {
            format!(
                "{CHROME_ENV} does not point to an executable: {}",
                path.display()
            )
        });
    }

    let mut candidates = Vec::new();
    if cfg!(target_os = "macos") {
        if let Some(home) = env::var_os("HOME") {
            let home = PathBuf::from(home);
            candidates.extend([
                home.join("Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
                home.join("Applications/Chromium.app/Contents/MacOS/Chromium"),
            ]);
        }
        candidates.extend([
            PathBuf::from("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
            PathBuf::from("/Applications/Chromium.app/Contents/MacOS/Chromium"),
            PathBuf::from(
                "/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
            ),
            PathBuf::from("/Applications/Google Chrome Beta.app/Contents/MacOS/Google Chrome Beta"),
        ]);
    } else if cfg!(target_os = "windows") {
        for variable in ["LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)"] {
            if let Some(root) = env::var_os(variable) {
                let root = PathBuf::from(root);
                candidates.extend([
                    root.join("Google/Chrome/Application/chrome.exe"),
                    root.join("Chromium/Application/chrome.exe"),
                ]);
            }
        }
    } else {
        candidates.extend([
            PathBuf::from("/usr/bin/google-chrome"),
            PathBuf::from("/usr/bin/google-chrome-stable"),
            PathBuf::from("/usr/bin/chromium"),
            PathBuf::from("/usr/bin/chromium-browser"),
            PathBuf::from("/snap/bin/chromium"),
        ]);
    }

    candidates.extend([
        PathBuf::from("google-chrome"),
        PathBuf::from("google-chrome-stable"),
        PathBuf::from("chromium"),
        PathBuf::from("chromium-browser"),
    ]);
    candidates
        .into_iter()
        .find_map(|candidate| usable_executable(&candidate))
        .ok_or_else(|| {
            format!("Chrome/Chromium executable not found; install Chrome or set {CHROME_ENV}")
        })
}

/// Resolve an explicit configured executable or fall back to platform discovery.
pub fn resolve_chrome_executable(explicit: Option<&Path>) -> Result<PathBuf, String> {
    if let Some(path) = explicit {
        return usable_executable(path).ok_or_else(|| {
            format!(
                "configured Chrome path does not point to an executable: {}",
                path.display()
            )
        });
    }
    discover_chrome_executable()
}

fn usable_executable(path: &Path) -> Option<PathBuf> {
    if path.components().count() == 1 {
        let path_value = env::var_os("PATH")?;
        for directory in env::split_paths(&path_value) {
            let candidate = directory.join(path);
            if is_executable(&candidate) {
                return Some(candidate);
            }
        }
        return None;
    }
    is_executable(path).then(|| path.to_path_buf())
}

fn is_executable(path: &Path) -> bool {
    path.is_file() && {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            path.metadata()
                .map(|metadata| metadata.permissions().mode() & 0o111 != 0)
                .unwrap_or(false)
        }
        #[cfg(not(unix))]
        {
            true
        }
    }
}

pub(crate) async fn connect(
    mode: &BrowserMode,
    timeout: Duration,
) -> Result<Connected, Box<dyn std::error::Error + Send + Sync>> {
    match mode {
        BrowserMode::Attach { endpoint } => {
            let (browser, handler) = Browser::connect(endpoint.clone()).await?;
            Ok(Connected {
                browser,
                handler,
                mode: ConnectionMode::Attach,
            })
        }
        BrowserMode::Launch {
            executable,
            user_data_dir,
            headless,
        } => {
            let mut builder = BrowserConfig::builder()
                .chrome_executable(executable)
                .user_data_dir(Path::new(user_data_dir))
                .launch_timeout(timeout)
                .request_timeout(timeout)
                .arg("--no-first-run")
                .arg("--no-default-browser-check")
                .arg("--disable-background-networking");
            if *headless {
                builder = builder.new_headless_mode();
            }
            let (browser, handler) = Browser::launch(builder.build()?).await?;
            Ok(Connected {
                browser,
                handler,
                mode: ConnectionMode::Launch,
            })
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn path_lookup_requires_an_executable_file() {
        let path =
            env::temp_dir().join(format!("symbrowse-chrome-discovery-{}", std::process::id()));
        let _ = std::fs::remove_file(&path);
        std::fs::write(&path, b"not a browser").expect("write discovery fixture");
        assert!(usable_executable(&path).is_none());
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn installed_chrome_is_discoverable_without_an_environment_override() {
        if env::var_os(CHROME_ENV).is_some() {
            return;
        }
        if let Ok(path) = discover_chrome_executable() {
            assert!(is_executable(&path));
        }
    }
}
