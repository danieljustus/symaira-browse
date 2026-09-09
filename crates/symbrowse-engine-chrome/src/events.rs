use std::sync::{
    Arc,
    atomic::{AtomicUsize, Ordering},
};

use chromiumoxide::Handler;
use futures::StreamExt;

pub(crate) fn spawn_counter(mut handler: Handler) -> Arc<AtomicUsize> {
    let counter = Arc::new(AtomicUsize::new(0));
    let observed = Arc::clone(&counter);
    tokio::spawn(async move {
        while handler.next().await.is_some() {
            observed.fetch_add(1, Ordering::Relaxed);
        }
    });
    counter
}
