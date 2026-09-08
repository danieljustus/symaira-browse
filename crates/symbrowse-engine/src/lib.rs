#![deny(unsafe_code)]

//! Protocol-neutral browser engine contracts and deterministic snapshot logic.
//!
//! This crate deliberately contains no browser, CDP, WebDriver, or platform
//! adapter code. Concrete engines translate their protocol payloads into these
//! owned types and consume the same stable-ref and snapshot machinery.

pub mod capabilities;
pub mod diff;
pub mod files;
pub mod refs;
pub mod snapshot;

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// Identifies an isolated browser context without exposing a protocol handle.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Context {
    pub id: String,
}

/// Identifies a page and its protocol session without naming a protocol.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Page {
    pub id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub session_id: String,
}

/// Stable fields returned by navigation commands.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct NavigationResult {
    pub frame_id: String,
    pub loader_id: String,
    pub error_text: String,
}

/// Protocol-neutral JavaScript result. `value` remains structured JSON so an
/// adapter never has to expose a generated runtime type at this boundary.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct EvaluationResult {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub value: Option<Value>,
    #[serde(rename = "type", default, skip_serializing_if = "String::is_empty")]
    pub value_type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub exception_text: String,
}

/// A raw accessibility node, owned as JSON until an adapter-specific decoder is
/// needed. Keeping the payload opaque preserves forward compatibility with AX
/// protocol revisions.
pub type AxNode = Value;
