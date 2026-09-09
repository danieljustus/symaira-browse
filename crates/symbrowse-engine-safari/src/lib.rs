#![deny(unsafe_code)]
#![cfg(target_os = "macos")]

//! macOS-only Safari adapters.
//!
//! The attach adapter and the BiDi adapter intentionally have different
//! lifetimes: attach never owns Safari, while BiDi owns both safaridriver and
//! its isolated automation session. Neither adapter is available on other
//! targets, so portable crates cannot accidentally acquire a macOS privilege
//! or process dependency.

mod attach;
mod bidi;

pub use attach::{
    AttachEngine, AttachError, DEFAULT_COMMAND_TIMEOUT, DEFAULT_NAVIGATION_TIMEOUT,
    DEFAULT_TAB_NAME, ENGINE_KIND as ATTACH_ENGINE_KIND, NavigationPolicy, OsascriptRunner,
    SafariPrerequisite, ScriptRunner,
};
pub use bidi::{
    BidiEngine, BidiError, BidiTransport, BoxFuture, DRIVER_PATH, DriverOptions,
    ENGINE_KIND as BIDI_ENGINE_KIND, ProcessAdapter, ProcessHandle, SessionCapabilities,
    SystemProcessAdapter, TransportConnector, WebSocketConnector, parse_session_response,
    require_loopback, session_request,
};
