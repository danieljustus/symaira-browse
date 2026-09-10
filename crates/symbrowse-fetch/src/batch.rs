//! Bounded, ordered batch execution for static fetch work.
use std::future::Future;
use std::sync::Arc;

use futures_util::{StreamExt, stream};

use crate::{Client, FetchError, Request, Response};

#[derive(Debug, PartialEq, Eq)]
pub struct BatchItem<T, E> {
    pub index: usize,
    pub result: Result<T, E>,
}

/// Run at most `concurrency` operations and return results in input order.
/// A failed item is retained rather than aborting the remaining batch.
pub async fn run_bounded<I, F, Fut, T, E>(
    inputs: Vec<I>,
    concurrency: usize,
    operation: F,
) -> Vec<BatchItem<T, E>>
where
    I: Send + 'static,
    F: Fn(I) -> Fut + Send + Sync + Clone + 'static,
    Fut: Future<Output = Result<T, E>> + Send + 'static,
    T: Send + 'static,
    E: Send + 'static,
{
    let limit = concurrency.max(1);
    let results = stream::iter(inputs.into_iter().enumerate().map(|(index, input)| {
        let operation = operation.clone();
        async move {
            BatchItem {
                index,
                result: operation(input).await,
            }
        }
    }))
    .buffer_unordered(limit)
    .collect::<Vec<_>>()
    .await;
    let mut results = results;
    results.sort_unstable_by_key(|item| item.index);
    results
}

/// A batch candidate useful to callers that only need URL-level recovery.
pub fn bounded_urls(urls: &[String], max_items: usize) -> Vec<String> {
    urls.iter().take(max_items).cloned().collect()
}

pub const MAX_URLS: usize = 20;

#[derive(Debug, PartialEq, Eq)]
pub struct BatchFailure {
    pub index: usize,
    pub url: String,
    pub error: String,
}

/// Execute GET requests through the production client. The result is always
/// input-ordered and includes every partial failure; more than 20 URLs is a
/// contract error rather than an implicit truncation.
pub async fn fetch_urls(
    client: Arc<dyn Client>,
    urls: Vec<String>,
    concurrency: usize,
    allow_private: bool,
) -> Result<Vec<BatchItem<Response, FetchError>>, String> {
    validate_size(urls.len())?;
    let results = run_bounded(urls, concurrency, move |url| {
        let client = Arc::clone(&client);
        async move {
            let mut request = Request::get(url);
            request.allow_private = allow_private;
            client.fetch(request).await
        }
    })
    .await;
    Ok(results)
}

/// Validate the public batch cardinality without performing network I/O.
pub fn validate_size(count: usize) -> Result<(), String> {
    if count == 0 {
        return Err("at least one URL is required".into());
    }
    if count > MAX_URLS {
        return Err(format!("too many URLs: maximum is {MAX_URLS}"));
    }
    Ok(())
}

/// Convert ordered results into JSON-safe status snapshots.
pub fn status_snapshot<T>(
    items: &[BatchItem<T, FetchError>],
    urls: &[String],
) -> Vec<serde_json::Value> {
    items
        .iter()
        .map(|item| match &item.result {
            Ok(_) => serde_json::json!({"index": item.index, "url": urls.get(item.index).cloned().unwrap_or_default(), "ok": true}),
            Err(error) => serde_json::json!({"index": item.index, "url": urls.get(item.index).cloned().unwrap_or_default(), "ok": false, "error": error.to_string()}),
        })
        .collect()
}
/// Convert a batch into the successful values and failures without losing order.
pub fn partition<T, E>(items: Vec<BatchItem<T, E>>) -> (Vec<T>, Vec<(usize, E)>) {
    let mut successes = Vec::new();
    let mut failures = Vec::new();
    for item in items {
        match item.result {
            Ok(value) => successes.push(value),
            Err(error) => failures.push((item.index, error)),
        }
    }
    (successes, failures)
}

#[cfg(test)]
mod tests {
    use std::sync::{Arc, Mutex};
    use std::time::Duration;

    use super::run_bounded;

    #[tokio::test]
    async fn preserves_input_order_and_partial_failures() {
        let active = Arc::new(Mutex::new(0usize));
        let peak = Arc::new(Mutex::new(0usize));
        let active_for_op = Arc::clone(&active);
        let peak_for_op = Arc::clone(&peak);
        let results = run_bounded(vec![3usize, 1, 2], 2, move |value| {
            let active = Arc::clone(&active_for_op);
            let peak = Arc::clone(&peak_for_op);
            async move {
                *active.lock().unwrap() += 1;
                let current = *active.lock().unwrap();
                {
                    let mut highest = peak.lock().unwrap();
                    *highest = (*highest).max(current);
                }
                tokio::time::sleep(Duration::from_millis((4 - value) as u64)).await;
                *active.lock().unwrap() -= 1;
                if value == 1 {
                    Err("one")
                } else {
                    Ok(value * 2)
                }
            }
        })
        .await;
        assert_eq!(
            results.iter().map(|item| item.index).collect::<Vec<_>>(),
            vec![0, 1, 2]
        );
        assert_eq!(results[0].result, Ok(6));
        assert_eq!(results[1].result, Err("one"));
        assert_eq!(*peak.lock().unwrap(), 2);
    }
}
