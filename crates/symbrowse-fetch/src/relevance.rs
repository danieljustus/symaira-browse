//! BM25 relevance filtering with the same token and section contracts as Go.
use std::cmp::Ordering;
use std::collections::HashMap;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Section {
    pub heading: String,
    pub text: String,
    pub raw: String,
    pub score: f64,
}

pub fn tokenize(text: &str) -> Vec<String> {
    let mut tokens = Vec::new();
    let mut current = String::new();
    for ch in text.to_lowercase().chars() {
        if ch.is_whitespace() || !ch.is_alphanumeric() {
            if !current.is_empty() {
                tokens.push(std::mem::take(&mut current));
            }
        } else {
            current.push(ch);
        }
    }
    if !current.is_empty() {
        tokens.push(current);
    }
    tokens
}

pub fn bm25(query: &str, docs: &[String]) -> Vec<f64> {
    if query.is_empty() || docs.is_empty() {
        return vec![0.0; docs.len()];
    }
    let query_tokens = tokenize(query);
    if query_tokens.is_empty() {
        return vec![0.0; docs.len()];
    }
    let doc_tokens: Vec<Vec<String>> = docs.iter().map(|doc| tokenize(doc)).collect();
    let lengths: Vec<f64> = doc_tokens
        .iter()
        .map(|tokens| tokens.len() as f64)
        .collect();
    let avg = (lengths.iter().sum::<f64>() / docs.len() as f64).max(1.0);
    let n = docs.len() as f64;
    let mut idf = HashMap::new();
    for term in &query_tokens {
        let df = doc_tokens
            .iter()
            .filter(|tokens| tokens.iter().any(|token| token == term))
            .count() as f64;
        idf.insert(term, ((n - df + 0.5) / (df + 0.5) + 1.0).ln());
    }
    let mut scores = vec![0.0; docs.len()];
    for (index, tokens) in doc_tokens.iter().enumerate() {
        let mut frequencies = HashMap::<&str, f64>::new();
        for token in tokens {
            *frequencies.entry(token).or_default() += 1.0;
        }
        for term in &query_tokens {
            let frequency = frequencies.get(term.as_str()).copied().unwrap_or(0.0);
            if frequency == 0.0 {
                continue;
            }
            let denominator = frequency + 1.5 * (1.0 - 0.75 + 0.75 * lengths[index] / avg);
            scores[index] += idf[term] * frequency * 2.5 / denominator;
        }
    }
    scores
}

pub fn split_markdown_sections(markdown: &str) -> Vec<Section> {
    if markdown.is_empty() {
        return vec![Section::default()];
    }
    let mut sections = Vec::new();
    let mut heading = String::new();
    let mut body: Vec<String> = Vec::new();
    let mut raw: Vec<String> = Vec::new();
    for line in markdown.split('\n') {
        let trimmed = line.trim();
        if trimmed.starts_with('#') {
            if !raw.is_empty() || !heading.is_empty() {
                sections.push(Section {
                    heading: heading.clone(),
                    text: body.join("\n").trim().into(),
                    raw: raw.join("\n").trim().into(),
                    score: 0.0,
                });
            }
            let level = trimmed.chars().take_while(|c| *c == '#').count();
            heading = trimmed[level..].trim().into();
            body.clear();
            raw = vec![line.into()];
        } else {
            if !trimmed.is_empty() {
                body.push(trimmed.into());
            }
            if !raw.is_empty() || !trimmed.is_empty() {
                raw.push(line.into());
            }
        }
    }
    sections.push(Section {
        heading,
        text: body.join("\n").trim().into(),
        raw: raw.join("\n").trim().into(),
        score: 0.0,
    });
    sections
}

pub fn rank_sections(query: &str, sections: &[Section], top_k: usize) -> Vec<Section> {
    if sections.is_empty() {
        return Vec::new();
    }
    let docs: Vec<String> = sections
        .iter()
        .map(|section| format!("{} {}", section.heading, section.text))
        .collect();
    let scores = bm25(query, &docs);
    let mut ranked: Vec<Section> = sections
        .iter()
        .cloned()
        .zip(scores)
        .map(|(mut section, score)| {
            section.score = score;
            section
        })
        .collect();
    ranked.sort_by(|a, b| b.score.partial_cmp(&a.score).unwrap_or(Ordering::Equal));
    if top_k > 0 {
        ranked.truncate(top_k);
    }
    ranked
}

pub fn reassemble_markdown(sections: &[Section], total: usize) -> String {
    if sections.is_empty() {
        return String::new();
    }
    let mut out = String::new();
    for (index, section) in sections.iter().enumerate() {
        if index > 0 {
            out.push_str("\n\n");
        }
        if !section.heading.is_empty() {
            out.push_str("## ");
            out.push_str(&section.heading);
            out.push_str("\n\n");
        }
        out.push_str(&section.text);
    }
    let omitted = total.saturating_sub(sections.len());
    if omitted > 0 {
        out.push_str("\n\n<!-- ... ");
        out.push_str(&format!(
            "{omitted} section{} omitted for relevance",
            if omitted == 1 { "" } else { "s" }
        ));
        out.push_str(" -->");
    }
    out
}

pub fn filter_json<T: Clone>(
    query: &str,
    items: &[T],
    texts: impl Fn(&T) -> String,
    top_k: usize,
) -> Vec<T> {
    if query.is_empty() || items.is_empty() {
        return items.to_vec();
    }
    let docs: Vec<String> = items.iter().map(texts).collect();
    let scores = bm25(query, &docs);
    let mut order: Vec<usize> = (0..items.len()).collect();
    order.sort_by(|a, b| {
        scores[*b]
            .partial_cmp(&scores[*a])
            .unwrap_or(Ordering::Equal)
    });
    if top_k > 0 {
        order.truncate(top_k);
    }
    order.sort_unstable();
    order
        .into_iter()
        .map(|index| items[index].clone())
        .collect()
}
