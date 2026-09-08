//! Deterministic trace export and replay comparison.

use crate::journal::Entry;
use serde::{Deserialize, Serialize};
use serde_json::Value;

pub const SCHEMA_VERSION: i64 = 1;
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Step {
    pub command: String,
    #[serde(default)]
    pub selector: String,
    #[serde(default)]
    pub value: String,
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub expected_url: String,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct File {
    pub schema_version: i64,
    pub created_at: String,
    pub session: String,
    pub steps: Vec<Step>,
}

#[must_use]
pub fn export(
    entries: &[Entry],
    session: impl Into<String>,
    created_at: impl Into<String>,
) -> File {
    File {
        schema_version: SCHEMA_VERSION,
        created_at: created_at.into(),
        session: session.into(),
        steps: entries
            .iter()
            .filter(|entry| !entry.result.starts_with("error") && entry.risk_class != "credential")
            .filter_map(step_from_entry)
            .collect(),
    }
}
fn step_from_entry(entry: &Entry) -> Option<Step> {
    let args = entry.args.as_ref()?.as_object()?;
    let string = |key: &str| {
        args.get(key)
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned()
    };
    match entry.command.as_str() {
        "open" | "goto" => {
            let url = string("url");
            (!url.is_empty()).then(|| Step {
                command: entry.command.clone(),
                selector: String::new(),
                value: String::new(),
                key: String::new(),
                expected_url: url.clone(),
                url,
            })
        }
        "click" | "dblclick" | "hover" | "focus" | "check" | "uncheck" | "scrollintoview"
        | "scroll" => {
            let selector = string("selector");
            (!selector.is_empty()).then(|| Step {
                command: entry.command.clone(),
                selector,
                value: String::new(),
                key: String::new(),
                url: String::new(),
                expected_url: String::new(),
            })
        }
        "fill" | "type" | "select" => {
            let selector = string("selector");
            (!selector.is_empty()).then(|| Step {
                command: entry.command.clone(),
                selector,
                value: string("value"),
                key: String::new(),
                url: String::new(),
                expected_url: String::new(),
            })
        }
        "press" => {
            let key = string("key");
            (!key.is_empty()).then(|| Step {
                command: entry.command.clone(),
                selector: String::new(),
                value: String::new(),
                key,
                url: String::new(),
                expected_url: String::new(),
            })
        }
        "auth.login" => {
            let value = string("entry");
            (!value.is_empty()).then(|| Step {
                command: entry.command.clone(),
                selector: String::new(),
                value,
                key: String::new(),
                url: String::new(),
                expected_url: String::new(),
            })
        }
        _ => None,
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ReplayOutcome {
    pub index: usize,
    pub command: String,
    pub matched: bool,
    #[serde(default)]
    pub expected_url: String,
    #[serde(default)]
    pub actual_url: String,
    #[serde(default)]
    pub error: String,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct ReplayResult {
    pub total: usize,
    pub matched: usize,
    pub deviated: usize,
    pub failed: usize,
    pub outcomes: Vec<ReplayOutcome>,
}
/// Compare recorded URL postconditions against observed results. Browser action
/// execution remains an engine concern; this function makes replay verdicts
/// deterministic and captures every deviation rather than aborting early.
#[must_use]
pub fn compare_urls(file: &File, actual_urls: &[Result<String, String>]) -> ReplayResult {
    let mut result = ReplayResult {
        total: file.steps.len(),
        matched: 0,
        deviated: 0,
        failed: 0,
        outcomes: Vec::with_capacity(file.steps.len()),
    };
    for (index, step) in file.steps.iter().enumerate() {
        let mut outcome = ReplayOutcome {
            index,
            command: step.command.clone(),
            matched: false,
            expected_url: step.expected_url.clone(),
            actual_url: String::new(),
            error: String::new(),
        };
        match actual_urls.get(index) {
            Some(Ok(actual)) => {
                outcome.actual_url = actual.clone();
                outcome.matched = normalize_url(actual) == normalize_url(&step.expected_url);
                if outcome.matched {
                    result.matched += 1;
                } else {
                    result.deviated += 1;
                }
            }
            Some(Err(error)) => {
                outcome.error = error.clone();
                result.failed += 1;
            }
            None => {
                outcome.error = "missing replay result".to_owned();
                result.failed += 1;
            }
        }
        result.outcomes.push(outcome);
    }
    result
}
fn normalize_url(url: &str) -> String {
    url.split('#')
        .next()
        .unwrap_or(url)
        .trim_end_matches('/')
        .to_owned()
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn skips_credentials_and_collects_deviation() {
        let entries = vec![
            Entry {
                command: "open".into(),
                args: Some(serde_json::json!({"url":"https://example.test/"})),
                risk_class: "navigate".into(),
                ..Entry::default()
            },
            Entry {
                command: "fill".into(),
                args: Some(serde_json::json!({"selector":"#password", "value":"••••"})),
                risk_class: "credential".into(),
                ..Entry::default()
            },
        ];
        let file = export(&entries, "s", "fixed");
        assert_eq!(file.steps.len(), 1);
        let result = compare_urls(&file, &[Ok("https://example.test".into())]);
        assert_eq!(result.matched, 1);
    }
}
