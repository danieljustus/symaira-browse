use serde::Deserialize;
use serde_json::Value;
use symbrowse_fetch::{dom, relevance, render, semantic};

#[derive(Deserialize)]
struct Fixture {
    oracle_commit: String,
    generated_by: String,
    vectors: Vec<Vector>,
}

#[derive(Deserialize)]
struct Vector {
    html: String,
    query: String,
    top_k: usize,
    title: String,
    lang: String,
    clean_html: String,
    markdown: String,
    document: Value,
    ranked_headings: Vec<String>,
}

#[test]
fn go_generated_static_vectors_match() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../port/fixtures/fetch/static.json")).unwrap();
    assert_eq!(
        fixture.oracle_commit,
        "652453d1595fc302bd69c328e7da8a21dbee28b9"
    );
    assert_eq!(
        fixture.generated_by,
        "scripts/rust-port/fetch_fixture_gen.go"
    );
    assert!(!fixture.vectors.is_empty());
    for vector in fixture.vectors {
        let mut tree = dom::parse(vector.html.as_bytes()).unwrap();
        dom::cleanup(&mut tree.root);
        assert_eq!(tree.title, vector.title);
        assert_eq!(tree.lang, vector.lang);
        assert_eq!(dom::serialize(&tree.root), vector.clean_html);
        let content = semantic::best_block(&tree.root, 20);
        let built = render::build_document(&tree, content, "https://example.com/source", 0);
        let markdown = render::markdown(&built.document, Some(content), true);
        assert_eq!(markdown, vector.markdown, "markdown mismatch");
        let actual_document = serde_json::to_value(&built.document).unwrap();
        assert_eq!(actual_document, vector.document, "document mismatch");
        let sections = relevance::split_markdown_sections(&markdown);
        let ranked = relevance::rank_sections(&vector.query, &sections, vector.top_k);
        let headings: Vec<String> = ranked.into_iter().map(|section| section.heading).collect();
        assert_eq!(headings, vector.ranked_headings);
    }
}
