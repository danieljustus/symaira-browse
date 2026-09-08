#![deny(unsafe_code)]

//! Language-neutral wire contracts for the staged Symaira Browse Rust port.

use symaira_core_version::{Info, new as new_version_info};

/// Public binary and protocol tool name.
pub const TOOL_NAME: &str = "symbrowse";

/// Current stable machine-readable output schema.
pub const SCHEMA_VERSION: i32 = 8;

fn version_info(version: &str) -> Info {
    // Keep Browse's tool/schema values explicit: CoreKit supplies only the
    // byte-compatible payload and formatting implementation.
    new_version_info(TOOL_NAME, version, SCHEMA_VERSION)
}

/// Renders the exact plain-text `version` subcommand contract.
///
/// ```
/// assert_eq!(
///     symbrowse_protocol::render_version_text("dev"),
///     "symbrowse dev\n"
/// );
/// ```
#[must_use]
pub fn render_version_text(version: &str) -> String {
    let mut output = version_info(version).to_string();
    output.push('\n');
    output
}

/// Renders the exact Cobra root `--version`/`-v` contract.
#[must_use]
pub fn render_root_version(version: &str) -> String {
    format!("{TOOL_NAME} version {version}\n")
}

/// Renders the exact versionkit JSON handshake.
///
/// # Errors
///
/// Returns an error only when JSON serialization fails.
pub fn render_version_json(version: &str) -> Result<String, serde_json::Error> {
    let mut output = String::from_utf8(version_info(version).json()?)
        .expect("CoreKit JSON serialization always emits UTF-8");
    output.push('\n');
    Ok(output)
}

#[cfg(test)]
mod tests {
    use super::{render_root_version, render_version_json, render_version_text};

    #[test]
    fn version_contracts_are_exact() {
        assert_eq!(render_version_text("v1.2.3"), "symbrowse v1.2.3\n");
        assert_eq!(render_root_version("v1.2.3"), "symbrowse version v1.2.3\n");
        assert_eq!(
            render_version_json("dev").expect("serialize fixed version document"),
            "{\"tool\":\"symbrowse\",\"version\":\"dev\",\"schema_version\":8}\n"
        );
    }
}
