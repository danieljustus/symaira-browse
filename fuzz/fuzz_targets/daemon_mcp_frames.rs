#![no_main]

use libfuzzer_sys::fuzz_target;
use serde_json::Value;
use symbrowse_daemon::{MAX_FRAME_BYTES, decode_frame};

fuzz_target!(|data: &[u8]| {
    // Keep malformed-frame cases bounded at the same boundary as production.
    if data.len() > MAX_FRAME_BYTES + 64 {
        return;
    }
    let _ = serde_json::from_slice::<Value>(data);
    if let Ok(frame) = decode_frame(data) {
        // Exercise the stable serde shape as well as the decoder.
        let _ = serde_json::to_vec(&frame);
    }
});
