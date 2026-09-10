#![deny(unsafe_code)]

//! Owned MCP stdio framing and registry adapter for the staged Rust port.
//!
//! The adapter owns MCP framing and tool dispatch while reusing the daemon
//! crate for endpoint redaction and shared daemon error hygiene.

pub mod error;
pub mod proxy;
pub mod registry;
pub mod transport;

pub use proxy::{DaemonProxy, DaemonProxyOptions, NoopProxy, ToolError, ToolProxy};
pub use transport::{ServeOptions, serve_stdio, serve_with_proxy};
