use std::{
    fs,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use symbrowse_fetch::{
    FetchClient, PinnedResolver, Request, batch,
    cache::{OutputCache, ResponseCache},
    pipeline, render,
};

fn temp_dir(label: &str) -> std::path::PathBuf {
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let path = std::env::temp_dir().join(format!("symbrowse-fetch-{label}-{nonce}"));
    fs::create_dir_all(&path).unwrap();
    path
}

#[test]
fn pipeline_covers_selector_raw_frontmatter_and_no_cache_controls() {
    let html = br#"<html lang="de"><head><title>Guide</title></head><body><main><article class="story"><h1>Heading</h1><p>Selected body.</p></article><p>Outside.</p></main></body></html>"#;
    let selected = pipeline::Options {
        selector: Some("article.story".into()),
        include_links: false,
        frontmatter: true,
        fetched_at: "2026-09-07T00:00:00Z".into(),
        ..Default::default()
    };
    let output = pipeline::render_html(
        html,
        "https://example.test/page",
        "https://example.test/page",
        200,
        &selected,
        None,
    )
    .unwrap();
    assert!(
        output
            .body
            .starts_with("---\ntitle: Guide\nurl: https://example.test/page\n")
    );
    assert!(output.body.contains("Selected body."));
    assert!(!output.body.contains("Outside."));

    let raw = pipeline::Options {
        raw: true,
        no_cache: true,
        ..Default::default()
    };
    let output = pipeline::render_html(
        html,
        "https://example.test/page",
        "https://example.test/page",
        200,
        &raw,
        None,
    )
    .unwrap();
    assert_eq!(output.body, String::from_utf8_lossy(html));
    assert!(output.cache_id.is_none());
}

#[test]
fn pipeline_store_full_text_uses_output_cache_and_is_bounded() {
    let root = temp_dir("store");
    let cache = OutputCache::new(&root, Some(Duration::from_secs(60)));
    let options = pipeline::Options {
        max_chars: 30,
        store_full_text: true,
        ..Default::default()
    };
    let html = b"<main><p>One two three four five six seven eight nine ten.</p></main>";
    let output = pipeline::render_html(
        html,
        "https://example.test/long",
        "https://example.test/long",
        200,
        &options,
        Some(&cache),
    )
    .unwrap();
    let id = output.cache_id.as_deref().expect("full text cache id");
    assert!(output.body.contains("Full text stored: cache_id="));
    let stored = String::from_utf8(cache.load(id).unwrap()).unwrap();
    assert!(stored.contains("One two three four five six seven eight nine ten."));
    assert_ne!(stored, output.body);
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn no_cache_disables_store_full_text_side_effects() {
    let root = temp_dir("no-cache");
    let cache = OutputCache::new(&root, Some(Duration::from_secs(60)));
    let options = pipeline::Options {
        max_chars: 20,
        store_full_text: true,
        no_cache: true,
        ..Default::default()
    };
    let output = pipeline::render_html(
        b"<main><p>one two three four five six seven eight nine.</p></main>",
        "https://example.test/no-cache",
        "https://example.test/no-cache",
        200,
        &options,
        Some(&cache),
    )
    .unwrap();
    assert!(output.cache_id.is_none());
    assert!(!root.join("out").exists());
    fs::remove_dir_all(root).unwrap();
}
#[test]
fn pipeline_marks_both_not_found_statuses_for_recovery() {
    assert!(pipeline::needs_recovery(404));
    assert!(pipeline::needs_recovery(410));
    assert!(!pipeline::needs_recovery(400));
    assert_eq!(
        pipeline::wayback_url("https://example.test/missing"),
        "https://web.archive.org/web/*/https://example.test/missing"
    );
}

#[test]
fn batch_boundaries_are_one_twenty_and_reject_twenty_one() {
    assert!(batch::validate_size(1).is_ok());
    assert!(batch::validate_size(20).is_ok());
    assert_eq!(
        batch::validate_size(21),
        Err("too many URLs: maximum is 20".into())
    );
    assert_eq!(
        batch::validate_size(0),
        Err("at least one URL is required".into())
    );
    let (successes, failures) = batch::partition(vec![
        batch::BatchItem {
            index: 0,
            result: Ok("one"),
        },
        batch::BatchItem {
            index: 1,
            result: Err("gone"),
        },
        batch::BatchItem {
            index: 2,
            result: Ok("three"),
        },
    ]);
    assert_eq!(successes, vec!["one", "three"]);
    assert_eq!(failures, vec![(1, "gone")]);
}

#[test]
fn frontmatter_keeps_final_url_and_schema_type() {
    let mut meta = render::Meta {
        final_url: "https://example.test/final".into(),
        status_code: 200,
        title: "Titled page".into(),
        est_tokens: 4,
        ..Default::default()
    };
    let doc = render::Document {
        url: "https://example.test/original".into(),
        final_url: meta.final_url.clone(),
        islands: vec![render::DataIsland {
            source: "ld+json".into(),
            json: serde_json::json!({"@type":"Article"}),
            raw_json: r#"{"@type":"Article"}"#.into(),
        }],
        ..Default::default()
    };
    let frontmatter = render::frontmatter_at(&meta, &doc, "2026-09-07T00:00:00Z");
    assert!(frontmatter.contains("final_url: https://example.test/final"));
    assert!(frontmatter.contains("schema_type: Article"));
    meta.truncated = true;
    assert!(render::metadata_header(&meta, "body").contains("⚠ truncated"));
}

#[tokio::test]
async fn production_pipeline_uses_a_valid_response_cache_before_network() {
    let root = temp_dir("response-cache");
    let cache = ResponseCache::new(&root);
    let options = pipeline::Options::default();
    pipeline::render_html_cached(
        b"<main><p>cached body</p></main>",
        "https://unresolvable.invalid/page",
        "https://unresolvable.invalid/page",
        200,
        &options,
        None,
        &cache,
        "honest",
        "default",
    )
    .unwrap();
    let key = ResponseCache::key(
        "https://unresolvable.invalid/page",
        "honest",
        "markdown",
        "default",
        "format=Markdown;selector=;raw=false;links=false;frontmatter=false;query=;top_k=0;max_chars=20000;char_threshold=500;max_island_bytes=5000;char_limit=0;store_full_text=false",
    );
    let (_, meta) = cache.get(&key).unwrap();
    assert_eq!(meta["status_code"], 200);
    assert_eq!(meta["final_url"], "https://unresolvable.invalid/page");
    let client = FetchClient::honest()
        .unwrap()
        .with_resolver(PinnedResolver::with_lookup(|_| {
            Ok(vec!["93.184.216.34:0".parse().unwrap()])
        }));
    let result = pipeline::fetch_and_render_cached(
        &client,
        Request::get("https://unresolvable.invalid/page"),
        &options,
        None,
        &cache,
        "honest",
        "default",
        None,
    )
    .await
    .unwrap();
    assert!(result.body.contains("cached body"));
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn response_cache_expires_and_removes_stale_entries() {
    let root = temp_dir("response-cache-ttl");
    let cache = ResponseCache::new(&root).with_ttl(Duration::from_nanos(1));
    let key = ResponseCache::key("https://example.test", "honest", "text", "default", "");
    cache.put(&key, b"body", &serde_json::json!({})).unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let dir = root.join(&key[..2]);
        assert_eq!(
            fs::metadata(&dir).unwrap().permissions().mode() & 0o777,
            0o700
        );
        assert_eq!(
            fs::metadata(dir.join(format!("{key}.body")))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
    }
    std::thread::sleep(Duration::from_millis(2));
    assert!(matches!(
        cache.get(&key),
        Err(symbrowse_fetch::cache::CacheError::Expired(_))
    ));
    fs::remove_dir_all(root).unwrap();
}
