#![deny(unsafe_code)]
#![allow(clippy::result_large_err)]

//! Local daemon IPC, lifecycle and redaction contracts for `symbrowse`.

mod client;
mod protocol;
mod redaction;
mod runtime;
#[cfg(target_os = "macos")]
mod safari_runtime;
mod server;
mod session;
mod spec;

#[cfg(unix)]
pub use client::connect_unix;
pub use client::{Client, ClientError, ClientOptions, StartOptions};
pub use protocol::codes;
pub use protocol::{
    DaemonError, ErrorCode, Frame, Response, Warning, decode_frame, error_response,
    success_response,
};
pub use redaction::{Redactor, redact_args, redact_env, redact_json, redact_str};
pub use runtime::dispatch_once;
pub use server::{
    DaemonHandler, DaemonState, HandlerResult, OperationContext, PolicyStatus, Server, ServerError,
    ServerOptions, default_socket_path, socket_path, validate_session,
};
pub use session::{
    SESSION_SCHEMA_VERSION, SessionError, SessionInfo, SessionListData, SessionRegistry,
    SessionRegistryOptions,
};
pub use spec::{SessionSpec, default_cache_dir, default_log_path, default_state_dir};

pub const MAX_FRAME_BYTES: usize = 1 << 20;
pub const DEFAULT_READ_TIMEOUT_MS: u64 = 30_000;
pub const DEFAULT_OPERATION_TIMEOUT_MS: u64 = 25_000;
pub const DEFAULT_STARTUP_TIMEOUT_MS: u64 = 5_000;
