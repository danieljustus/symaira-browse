#![no_main]

use libfuzzer_sys::fuzz_target;
use symbrowse_core::policy::{Allowlist, SsrfGuard, is_private_ip};

fuzz_target!(|data: &[u8]| {
    if data.len() > 4096 {
        return;
    }
    let candidate = String::from_utf8_lossy(data);
    let patterns = vec![candidate.to_string()];
    if let Ok(allowlist) = Allowlist::parse(&patterns) {
        let _ = allowlist.allows_url(&candidate);
    }
    // Never perform DNS during fuzzing. The injected public address exercises
    // URL/host policy without allowing a fuzz input to reach the network.
    let guard = SsrfGuard::with_lookup(false, |_host| Ok(vec!["8.8.8.8".to_owned()]));
    let _ = guard.allows_url(&candidate);
    if let Ok(address) = candidate.parse() {
        let _ = is_private_ip(address);
    }
});
