use symbrowse_fetch::{archive, dom, relevance, render, semantic};

#[test]
fn html5_cleanup_preserves_metadata_and_drops_hidden_content() {
    let mut tree = dom::parse(br#"<html lang='de'><head><title> Titel </title></head><body><main class='article' data-noise='1'><p>Visible text</p><div hidden>secret</div></main><script type='application/ld+json'>{"@type":"Article"}</script></body></html>"#).unwrap();
    assert_eq!(tree.title, "Titel");
    assert_eq!(tree.lang, "de");
    assert_eq!(tree.islands.len(), 1);
    dom::cleanup(&mut tree.root);
    let clean = dom::serialize(&tree.root);
    assert!(clean.contains("Visible text"));
    assert!(!clean.contains("secret"));
    assert!(!clean.contains("data-noise"));
}

#[test]
fn frontmatter_and_output_budget_are_deterministic() {
    let tree = dom::parse(
        b"<html><head><title>Title</title></head><body><main><p>body</p></main></body></html>",
    )
    .unwrap();
    let content = semantic::best_block(&tree.root, 1);
    let built = render::build_document(&tree, content, "https://example.test/page", 0);
    let mut meta = render::Meta {
        final_url: built.document.url.clone(),
        status_code: 200,
        title: built.document.title.clone(),
        lang: built.document.lang.clone(),
        ..Default::default()
    };
    let output = render::bounded_markdown(
        &mut meta,
        &built.document,
        "αβγδεζηθ",
        5,
        true,
        "2026-01-01T00:00:00Z",
    );
    assert!(meta.truncated);
    assert!(output.starts_with(
        "---\ntitle: Title\nurl: https://example.test/page\nfetched_at: 2026-01-01T00:00:00Z\n"
    ));
    assert!(output.contains("truncated: character budget reached"));
}

#[test]
fn relevance_keeps_original_order_after_top_k() {
    let docs = vec![
        "irrelevant".to_owned(),
        "rust static parser".to_owned(),
        "rust parser details".to_owned(),
    ];
    let selected = relevance::filter_json("rust", &docs, Clone::clone, 1);
    assert_eq!(selected, vec!["rust static parser"]);
    let sections =
        relevance::split_markdown_sections("# Intro\ncommon\n# Rust\nrust parser details");
    let ranked = relevance::rank_sections("parser", &sections, 1);
    assert_eq!(ranked[0].heading, "Rust");
    assert!(relevance::reassemble_markdown(&ranked, sections.len()).contains("1 section omitted"));
}

#[test]
fn wayback_and_recovery_candidates_are_bounded_and_safe() {
    assert_eq!(
        archive::rewrite_url("https://example.test/missing", None),
        "https://web.archive.org/web/*/https://example.test/missing"
    );
    assert_eq!(
        archive::parse_wayback_url(
            "https://web.archive.org/web/20260101120000/https://example.test/page"
        ),
        Some("https://example.test/page".to_owned())
    );
    let tree = dom::parse(b"<main><a href='https://example.test/missing'>Missing article</a><a href='javascript:bad'>bad</a><a href='https://example.test/missing-2'>Other</a></main>").unwrap();
    let candidates = archive::candidates_from_ancestor(&tree.root, "missing", 1);
    assert_eq!(candidates.len(), 1);
    assert_eq!(candidates[0].url, "https://example.test/missing");
}

#[test]
fn selector_preserves_distinct_identical_nodes_without_group_duplicates() {
    let tree = dom::parse(b"<main><p class='same'>x</p><p class='same'>x</p></main>").unwrap();
    assert_eq!(dom::select(&tree.root, "p.same").unwrap().len(), 2);
    assert_eq!(dom::select(&tree.root, "p.same, p").unwrap().len(), 2);
}
