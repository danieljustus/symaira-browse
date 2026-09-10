#![deny(unsafe_code)]

//! Stable machine-readable error taxonomy and process-exit mapping.

use core::fmt;

use serde::{Deserialize, Serialize};

/// Stable unified error codes. Values only grow additively.
#[derive(Clone, Copy, Debug, Deserialize, Eq, Hash, Ord, PartialEq, PartialOrd, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorCode {
    Auth,
    Config,
    Conflict,
    DaemonUnavailable,
    FlowFailed,
    HandoffTimeout,
    Internal,
    InvalidArgs,
    InvalidInspection,
    InvalidSession,
    MalformedRequest,
    NoInput,
    NotFound,
    OperationFailed,
    OperationTimeout,
    PeerDenied,
    Permission,
    SessionInactive,
    SessionNotFound,
    SessionUserControl,
    StaleRef,
    Unavailable,
    UnknownCommand,
    UnknownRef,
    Validation,
}

/// Stable corekit-compatible error kinds.
#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorKind {
    Auth,
    Config,
    Conflict,
    Internal,
    NotFound,
    Permission,
    Unavailable,
    Validation,
}

impl fmt::Display for ErrorKind {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        let value = serde_json::to_value(self).map_err(|_| fmt::Error)?;
        formatter.write_str(value.as_str().ok_or(fmt::Error)?)
    }
}

impl ErrorCode {
    /// Complete enum in the Go oracle's lexical fixture order.
    pub const ALL: [Self; 25] = [
        Self::Auth,
        Self::Config,
        Self::Conflict,
        Self::DaemonUnavailable,
        Self::FlowFailed,
        Self::HandoffTimeout,
        Self::Internal,
        Self::InvalidArgs,
        Self::InvalidInspection,
        Self::InvalidSession,
        Self::MalformedRequest,
        Self::NoInput,
        Self::NotFound,
        Self::OperationFailed,
        Self::OperationTimeout,
        Self::PeerDenied,
        Self::Permission,
        Self::SessionInactive,
        Self::SessionNotFound,
        Self::SessionUserControl,
        Self::StaleRef,
        Self::Unavailable,
        Self::UnknownCommand,
        Self::UnknownRef,
        Self::Validation,
    ];

    /// Returns the corekit-compatible category used by the Go oracle.
    #[must_use]
    pub const fn kind(self) -> ErrorKind {
        match self {
            Self::Auth => ErrorKind::Auth,
            Self::Config => ErrorKind::Config,
            Self::Conflict | Self::SessionUserControl | Self::StaleRef => ErrorKind::Conflict,
            Self::FlowFailed | Self::Internal => ErrorKind::Internal,
            Self::NotFound | Self::SessionInactive | Self::SessionNotFound | Self::UnknownRef => {
                ErrorKind::NotFound
            }
            Self::PeerDenied | Self::Permission => ErrorKind::Permission,
            Self::DaemonUnavailable
            | Self::HandoffTimeout
            | Self::OperationFailed
            | Self::OperationTimeout
            | Self::Unavailable => ErrorKind::Unavailable,
            Self::InvalidArgs
            | Self::InvalidInspection
            | Self::InvalidSession
            | Self::MalformedRequest
            | Self::NoInput
            | Self::UnknownCommand
            | Self::Validation => ErrorKind::Validation,
        }
    }

    /// Returns the stable numeric process exit code.
    #[must_use]
    pub const fn exit_code(self) -> u8 {
        match self.kind() {
            ErrorKind::Unavailable => 1,
            ErrorKind::Validation => 2,
            ErrorKind::Auth => 3,
            ErrorKind::Permission => 4,
            ErrorKind::NotFound => 5,
            ErrorKind::Conflict => 6,
            ErrorKind::Internal => 7,
            ErrorKind::Config => 9,
        }
    }
}
