#![no_main]

use libfuzzer_sys::fuzz_target;
use symbrowse_fetch::dom;

fuzz_target!(|data: &[u8]| {
    // HTML parsing is intentionally bounded before handing bytes to the DOM
    // builder; this is the same hostile-input boundary used by fetch callers.
    if data.len() > 1 << 20 {
        return;
    }
    let Ok(mut tree) = dom::parse(data) else {
        return;
    };
    dom::cleanup(&mut tree.root);
    let _ = dom::serialize(&tree.root);
    for selector in ["html", "body", "a[href]", "input[name]", "main > *"] {
        let _ = dom::select(&tree.root, selector);
    }
    let _ = dom::text_content(match &tree.root {
        dom::Node::Document { children } => children,
        _ => &[],
    });
});
