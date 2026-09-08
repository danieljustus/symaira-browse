#![deny(unsafe_code)]

//! Honest, policy-checked HTTP fetching and deterministic fetch controls.

pub mod archive;
pub mod batch;
pub mod cache;
mod client;
pub mod dom;
mod honest;
pub mod pipeline;
mod rate_limit;
pub mod relevance;
pub mod render;
mod retry;
pub mod robots;
pub mod semantic;

pub use client::{
    BodyTooLarge, Client, ClientOptions, FetchClient, FetchError, Profile, Request, Response,
};
pub use honest::PinnedResolver;
pub use rate_limit::{
    CircuitBreaker, CircuitBreakerConfig, CircuitState, HostRateLimiter, RateLimitConfig,
};
pub use retry::{
    BackoffConfig, RetryAfter, is_transient_status, parse_retry_after, parse_retry_after_millis,
};
