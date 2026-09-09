#![no_main]

use libfuzzer_sys::fuzz_target;
use symbrowse_core::flows;

fuzz_target!(|data: &[u8]| {
    if data.len() <= 1 << 20 {
        let _ = flows::parse(data, "fuzz-flow");
    }
});
