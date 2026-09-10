#![no_main]

use libfuzzer_sys::fuzz_target;
use symbrowse_core::state::{decode, decrypt};

fuzz_target!(|data: &[u8]| {
    // The state decoder performs the production magic/header/bounds checks.
    if data.len() <= (64 << 20) + (1 << 16) {
        let _ = decode(data, None);
        let _ = decrypt(data, b"fuzz-aad", &[0x5a; 32]);
    }
});
