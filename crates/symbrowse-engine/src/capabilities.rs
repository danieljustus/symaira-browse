use serde::{Deserialize, Serialize};

/// Canonical names for every optional engine extension.
///
/// The order is part of the machine-readable contract and must remain sorted.
pub const OPTIONAL_INTERFACE_NAMES: [&str; 19] = [
    "A11yAuditor",
    "AXSelectorResolver",
    "ClickDiagnosticEngine",
    "CookieEngine",
    "DialogController",
    "FileTransfer",
    "FrameManager",
    "InspectionEngine",
    "InteractionEngine",
    "NavigationStateProvider",
    "NetworkEvents",
    "NetworkPolicyReporter",
    "OverlayHost",
    "RuntimeEvents",
    "ScreenshotEngine",
    "ScreenshotOptionsEngine",
    "ScriptDisabler",
    "SettingsEngine",
    "TabManager",
];

/// Stable capability partition reported by an engine.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Capabilities {
    pub kind: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub launch_mode: String,
    pub interfaces: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub unsupported: Vec<String>,
}

/// Partition the canonical optional interface set into implemented and
/// unsupported halves. Unknown names are ignored, matching the Go oracle's
/// set-based capability contract.
pub fn capabilities_for<I, S>(kind: impl Into<String>, implemented: I) -> Capabilities
where
    I: IntoIterator<Item = S>,
    S: AsRef<str>,
{
    let implemented: std::collections::BTreeSet<String> = implemented
        .into_iter()
        .map(|name| name.as_ref().to_owned())
        .collect();
    let mut capabilities = Capabilities {
        kind: kind.into(),
        ..Capabilities::default()
    };
    for name in OPTIONAL_INTERFACE_NAMES {
        if implemented.contains(name) {
            capabilities.interfaces.push(name.to_owned());
        } else {
            capabilities.unsupported.push(name.to_owned());
        }
    }
    capabilities
}

impl Capabilities {
    /// Convenience constructor equivalent to [`capabilities_for`].
    pub fn for_engine<I, S>(kind: impl Into<String>, implemented: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: AsRef<str>,
    {
        capabilities_for(kind, implemented)
    }
}
