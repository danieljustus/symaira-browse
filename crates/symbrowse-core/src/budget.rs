#![deny(unsafe_code)]

//! Token estimation, head/foot truncation and line-range contracts.

/// Result of the deterministic 60/40 head/foot truncation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Truncation {
    pub head: String,
    pub foot: String,
    pub tokens_returned: usize,
    pub tokens_total: usize,
    pub truncated: bool,
}

/// Estimates tokens as Unicode scalar values divided by four, with a minimum
/// of one for non-empty text.
#[must_use]
pub fn estimate(text: &str) -> usize {
    let characters = text.chars().count();
    if characters == 0 {
        return 0;
    }
    (characters / 4).max(1)
}

/// Truncates text to a 60% head / 40% foot token budget.
#[must_use]
pub fn truncate(content: &str, max_tokens: usize) -> Truncation {
    let tokens_total = estimate(content);
    if tokens_total <= max_tokens {
        return Truncation {
            head: content.to_owned(),
            foot: String::new(),
            tokens_returned: tokens_total,
            tokens_total,
            truncated: false,
        };
    }
    let characters: Vec<char> = content.chars().collect();
    let head_budget = max_tokens * 6 / 10;
    let foot_budget = max_tokens - head_budget;
    let head_end = (head_budget * 4).min(characters.len());
    let foot_start = characters
        .len()
        .saturating_sub(foot_budget * 4)
        .max(head_end);
    let head: String = characters[..head_end].iter().collect();
    let foot: String = characters[foot_start..].iter().collect();
    Truncation {
        tokens_returned: estimate(&head) + estimate(&foot),
        tokens_total,
        head,
        foot,
        truncated: true,
    }
}

/// Extracts a one-based inclusive line range with Go-compatible clamping.
#[must_use]
pub fn line_range(content: &str, start: usize, end: usize) -> String {
    let lines: Vec<&str> = content.split('\n').collect();
    let start = start.max(1);
    let end = if end < start || end > lines.len() {
        lines.len()
    } else {
        end
    };
    if start > lines.len() {
        return String::new();
    }
    lines[start - 1..end].join("\n")
}
