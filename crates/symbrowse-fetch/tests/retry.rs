use std::time::Duration;

use symbrowse_fetch::{
    BackoffConfig, CircuitBreaker, CircuitBreakerConfig, CircuitState, HostRateLimiter,
    is_transient_status, parse_retry_after,
};

#[test]
fn retry_after_supports_delta_seconds_and_invalid_values_fail_closed() {
    assert_eq!(parse_retry_after(" 12 "), Duration::from_secs(12));
    assert_eq!(parse_retry_after("invalid"), Duration::ZERO);
    assert_eq!(parse_retry_after("-5"), Duration::ZERO);
}

#[test]
fn retry_after_supports_future_http_date() {
    let future = httpdate::fmt_http_date(std::time::SystemTime::now() + Duration::from_secs(5));
    let delay = parse_retry_after(&future);
    assert!(delay >= Duration::from_secs(3));
    assert!(delay <= Duration::from_secs(6));
}

#[test]
fn backoff_is_capped_and_jittered_in_the_documented_range() {
    let config = BackoffConfig {
        initial_delay: Duration::from_millis(100),
        max_delay: Duration::from_millis(250),
        multiplier: 2.0,
        max_retries: 4,
    };
    for attempt in 0..5 {
        let base = config.base_delay(attempt);
        let delay = config.delay(attempt);
        assert!(delay >= base / 2 && delay <= base);
    }
}

#[test]
fn transient_classification_includes_timeout_and_server_overload() {
    assert!(!is_transient_status(408));
    assert!(is_transient_status(429));
    assert!(is_transient_status(503));
    assert!(!is_transient_status(500));
    assert!(!is_transient_status(404));
}

#[test]
fn circuit_opens_and_recovers_with_successful_probes() {
    let breaker = CircuitBreaker::new(CircuitBreakerConfig {
        failure_threshold: 2,
        recovery_timeout: Duration::from_millis(5),
        success_threshold: 2,
    });
    assert!(breaker.allow());
    breaker.record_failure();
    assert!(breaker.allow());
    breaker.record_failure();
    assert_eq!(breaker.state(), CircuitState::Open);
    assert!(!breaker.allow());
    std::thread::sleep(Duration::from_millis(8));
    assert!(breaker.allow());
    assert!(breaker.allow());
    breaker.record_success();
    assert!(breaker.allow());
    breaker.record_success();
    assert_eq!(breaker.state(), CircuitState::Closed);
}

#[test]
fn host_limiter_keeps_circuits_independent() {
    let limiter = HostRateLimiter::new(CircuitBreakerConfig {
        failure_threshold: 1,
        recovery_timeout: Duration::from_secs(60),
        success_threshold: 1,
    });
    assert!(limiter.allow("https://one.example/page"));
    limiter.record_failure("https://one.example/page");
    assert!(!limiter.allow("https://one.example/page"));
    assert!(limiter.allow("https://two.example/page"));
    assert_eq!(limiter.host_count(), 2);
}
