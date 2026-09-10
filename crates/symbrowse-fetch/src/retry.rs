use std::time::Duration;

use rand::Rng;

/// Exponential retry configuration. Delays include Go-compatible 50–100% jitter.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct BackoffConfig {
    pub initial_delay: Duration,
    pub max_delay: Duration,
    pub multiplier: f64,
    pub max_retries: usize,
}

impl Default for BackoffConfig {
    fn default() -> Self {
        Self {
            initial_delay: Duration::from_millis(500),
            max_delay: Duration::from_secs(30),
            multiplier: 2.0,
            max_retries: 3,
        }
    }
}

impl BackoffConfig {
    /// Returns a jittered delay for a zero-based retry attempt.
    #[must_use]
    pub fn delay(self, attempt: usize) -> Duration {
        let base = self
            .initial_delay
            .as_secs_f64()
            .mul_add(self.multiplier.powi(attempt as i32), 0.0)
            .min(self.max_delay.as_secs_f64());
        let jitter = rand::rng().random_range(0.5..=1.0);
        Duration::from_secs_f64((base * jitter).max(0.0))
    }

    /// Returns a deterministic delay for fixture and oracle comparisons.
    #[must_use]
    pub fn deterministic_delay(self, attempt: usize, jitter_percent: u8) -> Duration {
        let jitter = u64::from(jitter_percent.clamp(50, 100));
        self.base_delay(attempt).mul_f64(jitter as f64 / 100.0)
    }

    /// Returns a delay using a caller-provided jitter sample in [0.5, 1.0].
    #[must_use]
    pub fn delay_with_jitter(self, attempt: usize, sample: f64) -> Duration {
        let sample = sample.clamp(0.5, 1.0);
        self.base_delay(attempt).mul_f64(sample)
    }

    /// Returns the uncapped, unjittered value. Useful for deterministic tests.
    #[must_use]
    pub fn base_delay(self, attempt: usize) -> Duration {
        let seconds = self
            .initial_delay
            .as_secs_f64()
            .mul_add(self.multiplier.powi(attempt as i32), 0.0)
            .min(self.max_delay.as_secs_f64());
        Duration::from_secs_f64(seconds.max(0.0))
    }
}

/// Parses a Retry-After value as either delta-seconds or an HTTP date.
/// Invalid and past values produce zero, which means no additional delay.
#[must_use]
pub fn parse_retry_after(value: &str) -> Duration {
    Duration::from_millis(parse_retry_after_millis(value).max(0) as u64)
}

/// Signed millisecond representation used for exact Go-oracle comparison.
#[must_use]
pub fn parse_retry_after_millis(value: &str) -> i64 {
    let value = value.trim();
    if value.is_empty() {
        return 0;
    }
    if let Ok(seconds) = value.parse::<i64>() {
        return seconds.saturating_mul(1000);
    }
    let Ok(when) = httpdate::parse_http_date(value) else {
        return 0;
    };
    match when.duration_since(std::time::SystemTime::now()) {
        Ok(duration) => duration.as_millis().min(i64::MAX as u128) as i64,
        Err(error) => -(error.duration().as_millis().min(i64::MAX as u128) as i64),
    }
}

/// Named result form for callers that need to distinguish a missing header.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct RetryAfter(pub Duration);

/// Status codes that the Go transport treats as retryable.
#[must_use]
pub const fn is_transient_status(status: u16) -> bool {
    status == 429 || matches!(status, 502..=504)
}
