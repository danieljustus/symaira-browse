//! Pure flow parsing, discovery, planning and recording contracts.
//!
//! Browser execution remains behind the existing Go/engine boundary. This module
//! deliberately owns only deterministic data transformations so it can be
//! checked without a browser, credentials or a network.

use std::{collections::BTreeMap, fmt, fs, path::Path};

use serde::{Deserialize, Serialize};
use yaml_rust2::{Yaml, YamlLoader};

pub const SCHEMA_VERSION: i64 = 1;
const MAX_DOCUMENT_BYTES: usize = 1024 * 1024;
const MAX_NODES: usize = 4096;
const MAX_STEPS: usize = 256;
const MAX_DEPTH: usize = 64;

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Flow {
    pub name: String,
    pub version: i64,
    pub domains: Vec<String>,
    pub inputs: Vec<String>,
    pub steps: Vec<Step>,
    pub outputs: Vec<Output>,
    #[serde(skip)]
    pub source: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Output {
    pub name: String,
    pub from: String,
    #[serde(default)]
    pub path: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Step {
    pub action: String,
    pub fields: BTreeMap<String, String>,
}

impl Step {
    #[must_use]
    pub fn field(&self, name: &str) -> &str {
        self.fields.get(name).map(String::as_str).unwrap_or("")
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ValidationError {
    pub line: usize,
    pub field: String,
    pub reason: String,
}

impl fmt::Display for ValidationError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.line > 0 {
            write!(f, "line {}: {}: {}", self.line, self.field, self.reason)
        } else {
            write!(f, "{}: {}", self.field, self.reason)
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct FlowError {
    pub errors: Vec<ValidationError>,
}
impl fmt::Display for FlowError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let joined = self
            .errors
            .iter()
            .map(ToString::to_string)
            .collect::<Vec<_>>()
            .join("; ");
        write!(f, "flow validation failed: {joined}")
    }
}
impl std::error::Error for FlowError {}

/// Parse and validate one bounded YAML flow document.
pub fn parse(data: &[u8], source: impl Into<String>) -> Result<Flow, FlowError> {
    if data.len() > MAX_DOCUMENT_BYTES {
        return Err(error("document", "flow document exceeds 1 MiB", 1));
    }
    let text =
        String::from_utf8(data.to_vec()).map_err(|_| error("document", "invalid UTF-8", 1))?;
    let docs = YamlLoader::load_from_str(&text).map_err(|e| {
        error(
            "document",
            &format!("invalid YAML: {e}"),
            marker_line(e.marker().line()),
        )
    })?;
    if docs.len() != 1 || docs[0].is_badvalue() {
        return Err(error("document", "empty flow document", 1));
    }
    if count_nodes(&docs[0], 0).is_err() {
        return Err(error(
            "document",
            "flow document exceeds node/depth limits",
            1,
        ));
    }
    let root = docs.first().expect("checked above");
    let Some(map) = root.as_hash() else {
        return Err(error("document", "flow must be a YAML mapping", 1));
    };
    let source = source.into();
    let mut flow = Flow {
        name: scalar_string(map_get(map, "name")).unwrap_or_default(),
        version: scalar_i64(map_get(map, "version")).unwrap_or_default(),
        domains: scalar_list(map_get(map, "domains")),
        inputs: scalar_list(map_get(map, "inputs")),
        steps: parse_steps(map_get(map, "steps"), &text),
        outputs: parse_outputs(map_get(map, "outputs"), &text),
        source,
    };
    let mut errors = Vec::new();
    validate_mapping_keys(
        &mut errors,
        &text,
        map,
        "",
        &["name", "version", "domains", "inputs", "steps", "outputs"],
    );
    validate_step_shapes(&mut errors, &text, map_get(map, "steps"));
    validate_output_shapes(&mut errors, &text, map_get(map, "outputs"));
    if flow.name.trim().is_empty() {
        errors.push(field_error(&text, "name", "required"));
    }
    if flow.version == 0 {
        errors.push(field_error(&text, "version", "required (use 1)"));
    }
    if flow.version != SCHEMA_VERSION {
        errors.push(field_error(
            &text,
            "version",
            &format!(
                "unsupported version {} (supported: {})",
                flow.version, SCHEMA_VERSION
            ),
        ));
    }
    if flow.domains.is_empty() {
        errors.push(field_error(
            &text,
            "domains",
            "required: at least one allowed domain",
        ));
    }
    for (i, domain) in flow.domains.iter().enumerate() {
        if !valid_domain(domain) {
            errors.push(field_error(
                &text,
                &format!("domains[{i}]"),
                &format!("invalid domain pattern {domain:?}"),
            ));
        }
    }
    if flow.steps.is_empty() {
        errors.push(field_error(&text, "steps", "required: at least one step"));
    }
    if flow.steps.len() > MAX_STEPS {
        errors.push(field_error(
            &text,
            "steps",
            &format!("at most {MAX_STEPS} steps are allowed"),
        ));
    }
    for (i, step) in flow.steps.iter().enumerate() {
        validate_step(&mut errors, &text, i, step);
    }
    for (i, output) in flow.outputs.iter().enumerate() {
        if output.name.trim().is_empty() {
            errors.push(field_error(
                &text,
                &format!("outputs[{i}].name"),
                "required",
            ));
        }
        if !matches!(output.from.as_str(), "url" | "text" | "attribute" | "html") {
            errors.push(field_error(
                &text,
                &format!("outputs[{i}].from"),
                &format!("invalid source {:?}", output.from),
            ));
        }
    }
    if errors.is_empty() {
        Ok(flow)
    } else {
        flow.source.clear();
        Err(FlowError { errors })
    }
}

fn error(field: &str, reason: &str, line: usize) -> FlowError {
    FlowError {
        errors: vec![ValidationError {
            line,
            field: field.to_owned(),
            reason: reason.to_owned(),
        }],
    }
}
fn marker_line(line: usize) -> usize {
    line.saturating_add(1)
}
fn field_error(text: &str, field: &str, reason: &str) -> ValidationError {
    ValidationError {
        line: line_for(text, field),
        field: field.to_owned(),
        reason: reason.to_owned(),
    }
}
fn line_for(text: &str, field: &str) -> usize {
    if let Some((rest, index)) = field
        .strip_prefix("steps[")
        .and_then(|value| value.split_once(']'))
    {
        let index = index.parse::<usize>().unwrap_or(0);
        let step_line = step_line_for(text, index);
        if let Some(nested) = rest.strip_prefix(".") {
            let action = nested.split('.').next().unwrap_or(nested);
            let key = nested.rsplit('.').next().unwrap_or(nested);
            return nested_line_for(text, step_line, action, key).unwrap_or(step_line);
        }
        return step_line;
    }
    let key = field.rsplit('.').next().unwrap_or(field);
    text.lines()
        .enumerate()
        .find_map(|(i, line)| {
            let trimmed = line.trim_start();
            (trimmed.starts_with(&format!("{key}:"))
                || trimmed.starts_with(&format!("{key} "))
                || trimmed.contains(&format!("{key}:"))
                || trimmed.contains(&format!("{key} ")))
            .then_some(i + 1)
        })
        .unwrap_or(0)
}

fn step_line_for(text: &str, wanted: usize) -> usize {
    let mut in_steps = false;
    let mut seen = 0;
    for (index, line) in text.lines().enumerate() {
        let trimmed = line.trim_start();
        if trimmed == "steps:" || trimmed.starts_with("steps: ") {
            in_steps = true;
            continue;
        }
        if in_steps && !trimmed.is_empty() && !line.starts_with(' ') && !line.starts_with('-') {
            break;
        }
        if in_steps && trimmed.starts_with('-') {
            if seen == wanted {
                return index + 1;
            }
            seen += 1;
        }
    }
    0
}

fn nested_line_for(text: &str, step_line: usize, action: &str, key: &str) -> Option<usize> {
    if step_line == 0 {
        return None;
    }
    let lines = text.lines().collect::<Vec<_>>();
    let start = step_line - 1;
    let step_indent = lines[start].len() - lines[start].trim_start().len();
    for (offset, line) in lines.iter().enumerate().skip(start) {
        let trimmed = line.trim_start();
        let indent = line.len() - trimmed.len();
        if offset > start && indent <= step_indent && trimmed.starts_with('-') {
            break;
        }
        if trimmed.contains(&format!("{key}:")) || trimmed.contains(&format!("{key} ")) {
            return Some(offset + 1);
        }
        if trimmed.starts_with(&format!("{action}:")) && key == action {
            return Some(offset + 1);
        }
    }
    Some(step_line)
}

fn validate_step_shapes(errors: &mut Vec<ValidationError>, text: &str, value: Option<&Yaml>) {
    let Some(items) = value.and_then(Yaml::as_vec) else {
        return;
    };
    for (index, item) in items.iter().enumerate() {
        let Some(map) = item.as_hash() else {
            errors.push(field_error(
                text,
                &format!("steps[{index}]"),
                "step must declare exactly one supported action",
            ));
            continue;
        };
        let actions = map.keys().filter_map(Yaml::as_str).collect::<Vec<_>>();
        if actions.len() != 1 {
            errors.push(field_error(
                text,
                &format!("steps[{index}]"),
                "step must declare exactly one supported action",
            ));
            continue;
        }
        let action = actions[0];
        let allowed: &[&str] = match action {
            "open" => &["url"],
            "find" => &[
                "label",
                "role",
                "text",
                "placeholder",
                "alt",
                "title",
                "testid",
                "action",
                "value",
                "exact",
            ],
            "click" | "fill" => &["label", "role", "text", "name", "value", "exact"],
            "wait" => &["url", "visible", "ms"],
            "assert" => &["visible", "url", "text", "not"],
            "snapshot" => &["diff", "compact", "max_tokens"],
            _ => &[],
        };
        match map_get(map, action).and_then(Yaml::as_hash) {
            Some(body) if !allowed.is_empty() => validate_mapping_keys(
                errors,
                text,
                body,
                &format!("steps[{index}].{action}"),
                allowed,
            ),
            _ => {}
        }
    }
}

fn validate_output_shapes(errors: &mut Vec<ValidationError>, text: &str, value: Option<&Yaml>) {
    let Some(items) = value.and_then(Yaml::as_vec) else {
        return;
    };
    for (index, item) in items.iter().enumerate() {
        if let Some(map) = item.as_hash() {
            validate_mapping_keys(
                errors,
                text,
                map,
                &format!("outputs[{index}]"),
                &["name", "from", "path"],
            );
        }
    }
}

fn validate_mapping_keys(
    errors: &mut Vec<ValidationError>,
    text: &str,
    map: &yaml_rust2::yaml::Hash,
    prefix: &str,
    allowed: &[&str],
) {
    for key in map.keys() {
        let Some(key) = key.as_str() else {
            errors.push(field_error(
                text,
                &prefix_or(prefix, "<non-string>"),
                "mapping keys must be strings",
            ));
            continue;
        };
        if !allowed.contains(&key) {
            errors.push(field_error(text, &prefix_or(prefix, key), "unknown field"));
        }
    }
}

fn prefix_or(prefix: &str, key: &str) -> String {
    if prefix.is_empty() {
        key.to_owned()
    } else {
        format!("{prefix}.{key}")
    }
}
fn count_nodes(node: &Yaml, depth: usize) -> Result<usize, ()> {
    if depth > MAX_DEPTH {
        return Err(());
    }
    let mut total = 1;
    match node {
        Yaml::Array(items) => {
            for item in items {
                total += count_nodes(item, depth + 1)?;
            }
        }
        Yaml::Hash(map) => {
            for (key, value) in map {
                total += count_nodes(key, depth + 1)? + count_nodes(value, depth + 1)?;
            }
        }
        _ => {}
    }
    if total > MAX_NODES {
        Err(())
    } else {
        Ok(total)
    }
}
fn map_get<'a>(map: &'a yaml_rust2::yaml::Hash, key: &str) -> Option<&'a Yaml> {
    map.get(&Yaml::String(key.to_owned()))
}
fn scalar_string(value: Option<&Yaml>) -> Option<String> {
    value.and_then(|v| v.as_str().map(ToOwned::to_owned))
}
fn scalar_i64(value: Option<&Yaml>) -> Option<i64> {
    value.and_then(Yaml::as_i64)
}
fn scalar_list(value: Option<&Yaml>) -> Vec<String> {
    value
        .and_then(Yaml::as_vec)
        .map(|v| {
            v.iter()
                .filter_map(|x| x.as_str().map(ToOwned::to_owned))
                .collect()
        })
        .unwrap_or_default()
}
fn parse_steps(value: Option<&Yaml>, text: &str) -> Vec<Step> {
    let Some(items) = value.and_then(Yaml::as_vec) else {
        return Vec::new();
    };
    items
        .iter()
        .filter_map(|item| {
            let map = item.as_hash()?;
            let (action, body) = map
                .iter()
                .find_map(|(key, value)| key.as_str().map(|name| (name.to_owned(), value)))?;
            let mut fields = BTreeMap::new();
            if let Some(body) = body.as_hash() {
                for (key, value) in body {
                    if let Some(key) = key.as_str() {
                        if let Some(value) = scalar_string(Some(value)) {
                            fields.insert(key.to_owned(), value);
                        } else if let Some(value) = value.as_i64() {
                            fields.insert(key.to_owned(), value.to_string());
                        } else if let Some(value) = value.as_bool() {
                            fields.insert(key.to_owned(), value.to_string());
                        }
                    }
                }
            }
            let _ = text;
            Some(Step { action, fields })
        })
        .collect()
}
fn parse_outputs(value: Option<&Yaml>, _text: &str) -> Vec<Output> {
    value
        .and_then(Yaml::as_vec)
        .map(|items| {
            items
                .iter()
                .filter_map(|item| {
                    let map = item.as_hash()?;
                    Some(Output {
                        name: scalar_string(map_get(map, "name")).unwrap_or_default(),
                        from: scalar_string(map_get(map, "from")).unwrap_or_default(),
                        path: scalar_string(map_get(map, "path")).unwrap_or_default(),
                    })
                })
                .collect()
        })
        .unwrap_or_default()
}
fn validate_step(errors: &mut Vec<ValidationError>, text: &str, index: usize, step: &Step) {
    let field = format!("steps[{index}]");
    if !matches!(
        step.action.as_str(),
        "open" | "find" | "click" | "fill" | "wait" | "assert" | "snapshot"
    ) {
        errors.push(field_error(
            text,
            &field,
            "step must declare exactly one supported action",
        ));
        return;
    }
    let selector = [
        "label",
        "role",
        "text",
        "placeholder",
        "alt",
        "title",
        "testid",
        "name",
    ]
    .iter()
    .any(|key| !step.field(key).trim().is_empty());
    match step.action.as_str() {
        "open" if step.field("url").trim().is_empty() => {
            errors.push(field_error(text, "open.url", "required"))
        }
        "find" => {
            if !selector {
                errors.push(field_error(
                    text,
                    "find",
                    "at least one semantic selector is required",
                ));
            }
            if !step.field("action").is_empty()
                && !matches!(
                    step.field("action"),
                    "click"
                        | "fill"
                        | "check"
                        | "hover"
                        | "text"
                        | "ref"
                        | "first"
                        | "last"
                        | "nth"
                )
            {
                errors.push(field_error(text, "find.action", "invalid action"));
            }
            if matches!(step.field("action"), "fill" | "text")
                && step.field("value").trim().is_empty()
            {
                errors.push(field_error(text, "find.value", "action requires a value"));
            }
        }
        "click" if !selector => errors.push(field_error(
            text,
            "click",
            "at least one semantic selector is required",
        )),
        "fill" => {
            if !selector {
                errors.push(field_error(
                    text,
                    "fill",
                    "at least one semantic selector is required",
                ));
            }
            if step.field("value").trim().is_empty() {
                errors.push(field_error(text, "fill.value", "required"));
            }
        }
        "wait" => {
            if step.field("url").is_empty()
                && step.field("visible").is_empty()
                && step.field("ms").is_empty()
            {
                errors.push(field_error(
                    text,
                    "wait",
                    "one of url, visible or ms is required",
                ));
            }
            if step.field("ms").parse::<i64>().is_ok_and(|ms| ms < 0) {
                errors.push(field_error(text, "wait.ms", "must not be negative"));
            }
        }
        "assert"
            if ["visible", "url", "text", "not"]
                .iter()
                .all(|key| step.field(key).is_empty()) =>
        {
            errors.push(field_error(
                text,
                "assert",
                "one of visible, url, text or not is required",
            ))
        }
        "snapshot" if step.field("max_tokens").parse::<i64>().is_ok_and(|n| n < 0) => errors.push(
            field_error(text, "snapshot.max_tokens", "must not be negative"),
        ),
        _ => {}
    }
    for key in ["value"] {
        if !step.field(key).starts_with("op://") && looks_secret(step.field(key)) {
            let secret_field = match step.action.as_str() {
                "find" => "find.value".to_owned(),
                "fill" => "fill.value".to_owned(),
                _ => format!("{field}.{key}"),
            };
            errors.push(field_error(
                text,
                &secret_field,
                "plaintext secret detected: use an op:// reference instead",
            ));
        }
    }
}
fn looks_secret(value: &str) -> bool {
    let lower = value.to_ascii_lowercase();
    [
        "password",
        "passwd",
        "pwd=",
        "secret",
        "token",
        "apikey",
        "api_key",
        "authorization",
        "bearer ",
        "client_secret",
    ]
    .iter()
    .any(|hint| lower.contains(hint))
}
fn valid_domain(pattern: &str) -> bool {
    let host = pattern
        .trim()
        .rsplit_once(':')
        .map_or(pattern.trim(), |(host, port)| {
            if !port.is_empty() && port.chars().all(|c| c.is_ascii_digit()) {
                host
            } else {
                ""
            }
        });
    if host.is_empty() {
        return false;
    }
    if host.split('.').count() == 4
        && host
            .split('.')
            .all(|p| !p.is_empty() && p.len() <= 3 && p.chars().all(|c| c.is_ascii_digit()))
    {
        return true;
    }
    host.split('.').enumerate().all(|(i, part)| {
        (i == 0 && part == "*")
            || (!part.is_empty() && part.chars().all(|c| c.is_ascii_alphanumeric() || c == '-'))
    })
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct FoundFlow {
    pub name: String,
    pub path: String,
    pub origin: String,
    pub version: i64,
    pub steps: usize,
}

/// Discover YAML flows from an explicit directory. The caller supplies roots,
/// keeping environment and home-directory policy outside this pure contract.
pub fn discover_directory(directory: &Path, origin: &str) -> std::io::Result<Vec<FoundFlow>> {
    let mut found = Vec::new();
    let mut entries = match fs::read_dir(directory) {
        Ok(entries) => entries.collect::<Result<Vec<_>, _>>()?,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(found),
        Err(error) => return Err(error),
    };
    entries.sort_by_key(|entry| entry.file_name());
    for entry in entries {
        if !entry.file_type()?.is_file() {
            continue;
        }
        let path = entry.path();
        if !matches!(
            path.extension().and_then(|e| e.to_str()),
            Some("yaml" | "yml")
        ) {
            continue;
        }
        if let Ok(flow) = parse(&fs::read(&path)?, path.to_string_lossy().to_string()) {
            found.push(FoundFlow {
                name: flow.name,
                path: path.to_string_lossy().into_owned(),
                origin: origin.to_owned(),
                version: flow.version,
                steps: flow.steps.len(),
            });
        }
    }
    Ok(found)
}

#[must_use]
pub fn merge_discovered(groups: impl IntoIterator<Item = Vec<FoundFlow>>) -> Vec<FoundFlow> {
    let mut merged = BTreeMap::<String, FoundFlow>::new();
    for group in groups {
        for flow in group {
            let replace = merged
                .get(&flow.name)
                .is_none_or(|old| rank(&flow.origin) < rank(&old.origin));
            if replace {
                merged.insert(flow.name.clone(), flow);
            }
        }
    }
    merged.into_values().collect()
}
fn rank(origin: &str) -> usize {
    match origin {
        "project" => 0,
        "global" => 1,
        _ => 2,
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PlanItem {
    pub index: usize,
    pub action: String,
    pub risk_class: String,
}
#[must_use]
pub fn dry_run(flow: &Flow) -> Vec<PlanItem> {
    flow.steps
        .iter()
        .enumerate()
        .map(|(index, step)| PlanItem {
            index,
            action: step.action.clone(),
            risk_class: risk_class(&step.action).to_owned(),
        })
        .collect()
}
fn risk_class(action: &str) -> &str {
    match action {
        "open" => "navigate",
        "find" | "click" | "fill" => "interact",
        "wait" => "navigate",
        _ => "read",
    }
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct RecordedAction {
    pub index: usize,
    pub command: String,
    pub selector: String,
    pub value: String,
    pub url: String,
    pub role: String,
    pub name: String,
    pub input_type: String,
    pub autocomplete: String,
}
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct Draft {
    pub name: String,
    pub version: i64,
    pub domains: Vec<String>,
    pub inputs: Vec<String>,
    pub steps: Vec<Step>,
    pub comments: Vec<String>,
    pub secret_refs: Vec<String>,
}

/// Convert a recording to a reviewable draft. Values are never emitted as
/// secrets: credential-looking values become opaque op:// placeholders.
pub fn generate_draft(actions: &[RecordedAction]) -> Result<Draft, FlowError> {
    if actions.is_empty() {
        return Err(error("recording", "no actions recorded", 0));
    }
    let mut draft = Draft {
        name: "recorded-flow".to_owned(),
        version: SCHEMA_VERSION,
        domains: Vec::new(),
        inputs: Vec::new(),
        steps: Vec::new(),
        comments: Vec::new(),
        secret_refs: Vec::new(),
    };
    let mut domains = std::collections::BTreeSet::new();
    let mut last_url = String::new();
    for action in actions {
        let mut fields = BTreeMap::new();
        let step_action = match action.command.as_str() {
            "open" | "goto" => {
                fields.insert("url".to_owned(), action.selector.clone());
                "open"
            }
            "find" => {
                fields.insert("label".to_owned(), action.name.clone());
                "find"
            }
            "click" => {
                fields.insert("role".to_owned(), action.role.clone());
                fields.insert("name".to_owned(), action.name.clone());
                fields.insert("exact".to_owned(), "true".to_owned());
                "click"
            }
            "fill" | "type" => {
                fields.insert("role".to_owned(), action.role.clone());
                fields.insert("name".to_owned(), action.name.clone());
                fields.insert("exact".to_owned(), "true".to_owned());
                fields.insert(
                    "value".to_owned(),
                    redact_recorded_value(action, &mut draft),
                );
                "fill"
            }
            "wait" => {
                fields.insert("url".to_owned(), action.selector.clone());
                "wait"
            }
            "assert" => {
                fields.insert("visible".to_owned(), action.selector.clone());
                "assert"
            }
            "snapshot" => {
                fields.insert("compact".to_owned(), "true".to_owned());
                "snapshot"
            }
            _ => continue,
        };
        draft.steps.push(Step {
            action: step_action.to_owned(),
            fields,
        });
        if action.url != last_url && !action.url.is_empty() {
            draft.steps.push(Step {
                action: "assert".to_owned(),
                fields: BTreeMap::from([(String::from("url"), glob_for_url(&action.url))]),
            });
            last_url = action.url.clone();
        }
        if let Some(host) = host_of(&action.url) {
            domains.insert(host);
        }
        if let Some(host) = host_of(&action.selector) {
            domains.insert(host);
        }
    }
    if draft.steps.is_empty() {
        return Err(error("recording", "recording contains no flow steps", 0));
    }
    draft.domains = domains.into_iter().collect();
    draft.comments.push(
        "review: verify selectors, inputs and assertions before approving this draft".to_owned(),
    );
    Ok(draft)
}
fn glob_for_url(raw: &str) -> String {
    raw.split(['?', '#']).next().unwrap_or(raw).to_owned()
}
fn host_of(raw: &str) -> Option<String> {
    let rest = raw
        .strip_prefix("https://")
        .or_else(|| raw.strip_prefix("http://"))?;
    let host = rest.split(['/', '?', '#']).next().unwrap_or("");
    (!host.is_empty()).then(|| host.to_owned())
}

fn redact_recorded_value(action: &RecordedAction, draft: &mut Draft) -> String {
    let value = action.value.trim();
    if value.is_empty() {
        return String::new();
    }
    let sensitive = action.input_type.eq_ignore_ascii_case("password")
        || matches!(
            action.autocomplete.to_ascii_lowercase().as_str(),
            "current-password" | "new-password"
        )
        || looks_secret(value)
        || looks_secret(&action.name);
    if sensitive {
        let reference = format!("op://recording/secret-{}", draft.secret_refs.len() + 1);
        draft.secret_refs.push(reference.clone());
        draft.comments.push(format!(
            "secret placeholder {reference} — resolve via symvault/1Password before running"
        ));
        reference
    } else {
        let name = format!("input_{}", draft.inputs.len() + 1);
        draft.inputs.push(name.clone());
        draft.comments.push(format!(
            "{name}: recorded literal value {value:?} — replace with a real input or op:// reference"
        ));
        format!("{{{{{name}}}}}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn parses_flow_and_plans_without_browser() {
        let flow = parse(b"name: demo\nversion: 1\ndomains: [example.com]\nsteps:\n  - open: {url: https://example.com}\n  - fill: {label: Email, value: '{{email}}'}\n", "fixture").unwrap();
        assert_eq!(flow.steps.len(), 2);
        assert_eq!(dry_run(&flow)[1].risk_class, "interact");
    }
    #[test]
    fn rejects_secret_literal() {
        let error = parse(b"name: demo\nversion: 1\ndomains: [example.com]\nsteps:\n - fill: {label: Password, value: fixture-password-value}\n", "fixture").unwrap_err();
        assert!(error.to_string().contains("plaintext secret"));
    }
    #[test]
    fn rejects_multiple_actions_and_unknown_fields_with_lines() {
        let error = parse(
            b"name: demo\nversion: 1\ndomains: [example.com]\nsteps:\n  - open: {url: https://example.com, timeout: 1}\n  - {click: {label: Go}, fill: {label: Name, value: '{{name}}'}}\n",
            "fixture",
        )
        .unwrap_err();
        assert!(
            error
                .errors
                .iter()
                .any(|item| item.field == "steps[0].open.timeout" && item.line == 5),
            "errors: {:?}",
            error.errors
        );
        assert!(
            error
                .errors
                .iter()
                .any(|item| item.field == "steps[1]" && item.reason.contains("exactly one"))
        );
    }

    #[test]
    fn rejects_unknown_top_level_and_output_fields() {
        let error = parse(
            b"name: demo\nversion: 1\ndomains: [example.com]\nextra: true\nsteps:\n  - open: {url: https://example.com}\noutputs:\n  - {name: url, from: url, secret: nope}\n",
            "fixture",
        )
        .unwrap_err();
        assert!(error.errors.iter().any(|item| item.field == "extra"));
        assert!(
            error
                .errors
                .iter()
                .any(|item| item.field == "outputs[0].secret")
        );
    }

    #[test]
    fn reports_nested_validation_on_the_step_line() {
        let error = parse(
            b"name: demo\nversion: 1\ndomains: [example.com]\nsteps:\n  - fill: {label: Password, value: password123}\n",
            "fixture",
        )
        .unwrap_err();
        assert!(
            error
                .errors
                .iter()
                .any(|item| item.field == "fill.value" && item.line == 5),
            "errors: {:?}",
            error.errors
        );
    }
}
