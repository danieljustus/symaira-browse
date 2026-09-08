use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use tokio::sync::{OwnedSemaphorePermit, Semaphore};
use url::Url;

/// Circuit breaker state.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum CircuitState {
    Closed,
    Open,
    HalfOpen,
}

/// Circuit breaker thresholds and recovery timing.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct CircuitBreakerConfig {
    pub failure_threshold: usize,
    pub recovery_timeout: Duration,
    pub success_threshold: usize,
}

impl Default for CircuitBreakerConfig {
    fn default() -> Self {
        Self {
            failure_threshold: 5,
            recovery_timeout: Duration::from_secs(60),
            success_threshold: 2,
        }
    }
}

struct CircuitInner {
    state: CircuitState,
    consecutive_failures: usize,
    half_open_successes: usize,
    opened_at: Option<Instant>,
    probe_in_flight: bool,
}

/// A small, thread-safe circuit breaker.
#[derive(Clone)]
pub struct CircuitBreaker {
    inner: Arc<Mutex<CircuitInner>>,
    config: CircuitBreakerConfig,
}

impl std::fmt::Debug for CircuitBreaker {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("CircuitBreaker")
            .field("state", &self.state())
            .finish_non_exhaustive()
    }
}

impl CircuitBreaker {
    #[must_use]
    pub fn new(config: CircuitBreakerConfig) -> Self {
        Self {
            inner: Arc::new(Mutex::new(CircuitInner {
                state: CircuitState::Closed,
                consecutive_failures: 0,
                half_open_successes: 0,
                opened_at: None,
                probe_in_flight: false,
            })),
            config,
        }
    }

    /// Returns whether a request may start. Half-open admits one probe only.
    pub fn allow(&self) -> bool {
        let mut inner = self.inner.lock().expect("circuit mutex poisoned");
        match inner.state {
            CircuitState::Closed => true,
            CircuitState::Open => {
                if inner
                    .opened_at
                    .is_some_and(|opened| opened.elapsed() >= self.config.recovery_timeout)
                {
                    inner.state = CircuitState::HalfOpen;
                    inner.half_open_successes = 0;
                    inner.probe_in_flight = true;
                    true
                } else {
                    false
                }
            }
            CircuitState::HalfOpen => true,
        }
    }

    pub fn record_success(&self) {
        let mut inner = self.inner.lock().expect("circuit mutex poisoned");
        match inner.state {
            CircuitState::HalfOpen => {
                inner.probe_in_flight = false;
                inner.half_open_successes += 1;
                if inner.half_open_successes >= self.config.success_threshold.max(1) {
                    inner.state = CircuitState::Closed;
                    inner.consecutive_failures = 0;
                    inner.half_open_successes = 0;
                    inner.opened_at = None;
                }
            }
            CircuitState::Closed => {
                inner.consecutive_failures = 0;
            }
            CircuitState::Open => {}
        }
    }

    pub fn record_failure(&self) {
        let mut inner = self.inner.lock().expect("circuit mutex poisoned");
        inner.probe_in_flight = false;
        inner.consecutive_failures += 1;
        if inner.state == CircuitState::HalfOpen
            || inner.consecutive_failures >= self.config.failure_threshold.max(1)
        {
            inner.state = CircuitState::Open;
            inner.opened_at = Some(Instant::now());
            inner.half_open_successes = 0;
        }
    }

    #[must_use]
    pub fn state(&self) -> CircuitState {
        self.inner.lock().expect("circuit mutex poisoned").state
    }
}

/// Per-host concurrency and pacing settings.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct RateLimitConfig {
    pub max_concurrent: usize,
    pub min_interval: Duration,
}

impl Default for RateLimitConfig {
    fn default() -> Self {
        Self {
            max_concurrent: 4,
            min_interval: Duration::ZERO,
        }
    }
}

struct HostState {
    breaker: CircuitBreaker,
    semaphore: Arc<Semaphore>,
    next_request: Mutex<Instant>,
}

/// Per-host circuit, concurrency and pacing controller.
#[derive(Clone)]
pub struct HostRateLimiter {
    states: Arc<Mutex<HashMap<String, Arc<HostState>>>>,
    circuit: CircuitBreakerConfig,
    rate: RateLimitConfig,
}

impl std::fmt::Debug for HostRateLimiter {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("HostRateLimiter")
            .field(
                "hosts",
                &self.states.lock().expect("host mutex poisoned").len(),
            )
            .finish()
    }
}

impl HostRateLimiter {
    #[must_use]
    pub fn new(circuit: CircuitBreakerConfig) -> Self {
        Self::with_rate(circuit, RateLimitConfig::default())
    }

    #[must_use]
    pub fn with_rate(circuit: CircuitBreakerConfig, rate: RateLimitConfig) -> Self {
        Self {
            states: Arc::new(Mutex::new(HashMap::new())),
            circuit,
            rate: RateLimitConfig {
                max_concurrent: rate.max_concurrent.max(1),
                ..rate
            },
        }
    }

    fn host(raw_url: &str) -> String {
        Url::parse(raw_url)
            .ok()
            .and_then(|url| url.host_str().map(str::to_ascii_lowercase))
            .unwrap_or_else(|| raw_url.to_owned())
    }

    fn state(&self, raw_url: &str) -> Arc<HostState> {
        let host = Self::host(raw_url);
        let mut states = self.states.lock().expect("host mutex poisoned");
        Arc::clone(states.entry(host).or_insert_with(|| {
            Arc::new(HostState {
                breaker: CircuitBreaker::new(self.circuit),
                semaphore: Arc::new(Semaphore::new(self.rate.max_concurrent)),
                next_request: Mutex::new(Instant::now()),
            })
        }))
    }

    #[must_use]
    pub fn allow(&self, raw_url: &str) -> bool {
        self.state(raw_url).breaker.allow()
    }

    pub fn record_success(&self, raw_url: &str) {
        self.state(raw_url).breaker.record_success();
    }

    pub fn record_failure(&self, raw_url: &str) {
        self.state(raw_url).breaker.record_failure();
    }

    /// Acquires one host permit and reserves the next pacing slot.
    pub async fn acquire(&self, raw_url: &str) -> OwnedSemaphorePermit {
        let state = self.state(raw_url);
        let permit = Arc::clone(&state.semaphore)
            .acquire_owned()
            .await
            .expect("host semaphore is never closed");
        if !self.rate.min_interval.is_zero() {
            let wait = {
                let mut next = state.next_request.lock().expect("host mutex poisoned");
                let now = Instant::now();
                let start = (*next).max(now);
                *next = start + self.rate.min_interval;
                start.saturating_duration_since(now)
            };
            if !wait.is_zero() {
                tokio::time::sleep(wait).await;
            }
        }
        permit
    }

    #[must_use]
    pub fn host_count(&self) -> usize {
        self.states.lock().expect("host mutex poisoned").len()
    }
}
