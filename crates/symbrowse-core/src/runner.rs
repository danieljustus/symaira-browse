//! Browser-independent workflow execution orchestration.
//!
//! The executor boundary is deliberately small: browser and daemon crates own
//! transport, while this module owns input resolution, domain checks, step
//! ordering, hard assertions, output extraction and bounded error handling.

use std::{collections::BTreeMap, fmt, future::Future, pin::Pin, time::Instant};

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use url::Url;

use crate::flows::{Flow, Step};

pub trait Executor {
    fn execute(&mut self, command: &str, args: Value) -> Result<Value, ExecutionError>;
}

/// Async executor used by transports whose commands can be cancelled while
/// they are waiting on the browser or network.
pub trait AsyncExecutor {
    fn execute<'a>(
        &'a mut self,
        command: &'a str,
        args: Value,
    ) -> Pin<Box<dyn Future<Output = Result<Value, ExecutionError>> + Send + 'a>>;
}

impl<F> Executor for F
where
    F: FnMut(&str, Value) -> Result<Value, ExecutionError>,
{
    fn execute(&mut self, command: &str, args: Value) -> Result<Value, ExecutionError> {
        self(command, args)
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ExecutionError {
    pub message: String,
}

impl ExecutionError {
    #[must_use]
    pub fn new(message: impl Into<String>) -> Self {
        Self {
            message: message.into(),
        }
    }
}

impl fmt::Display for ExecutionError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}
impl std::error::Error for ExecutionError {}

#[derive(Clone, Debug)]
pub struct RunOptions {
    pub flow: Flow,
    pub inputs: BTreeMap<String, String>,
    pub dry_run: bool,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct StepRun {
    pub index: usize,
    pub action: String,
    pub risk_class: String,
    pub success: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
    pub duration_ms: u128,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct PlanItem {
    pub index: usize,
    pub action: String,
    pub risk_class: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub struct RunReport {
    pub name: String,
    pub version: i64,
    pub dry_run: bool,
    pub success: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub steps: Vec<StepRun>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub plan: Vec<PlanItem>,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub outputs: BTreeMap<String, String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
    pub duration_ms: u128,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RunError {
    pub step_index: usize,
    pub action: String,
    pub message: String,
}
impl fmt::Display for RunError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "flow step {} ({}) failed: {}",
            self.step_index, self.action, self.message
        )
    }
}
impl std::error::Error for RunError {}

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

pub fn run<E: Executor>(executor: &mut E, options: RunOptions) -> Result<RunReport, RunError> {
    let started = Instant::now();
    let inputs = resolve_inputs(&options.flow, &options.inputs).map_err(|message| RunError {
        step_index: 0,
        action: "inputs".to_owned(),
        message,
    })?;
    let mut report = RunReport {
        name: options.flow.name.clone(),
        version: options.flow.version,
        dry_run: options.dry_run,
        success: false,
        steps: Vec::new(),
        plan: Vec::new(),
        outputs: BTreeMap::new(),
        error: String::new(),
        duration_ms: 0,
    };
    if options.dry_run {
        report.plan = dry_run(&options.flow);
        report.success = true;
        report.duration_ms = started.elapsed().as_millis();
        return Ok(report);
    }
    enforce_domains(&options.flow, &inputs)?;
    for (index, step) in options.flow.steps.iter().enumerate() {
        let step_started = Instant::now();
        let action = step.action.clone();
        let result = execute_step(executor, step, &inputs);
        let mut record = StepRun {
            index,
            action: action.clone(),
            risk_class: risk_class(&action).to_owned(),
            success: result.is_ok(),
            error: String::new(),
            data: result.as_ref().ok().cloned(),
            duration_ms: step_started.elapsed().as_millis(),
        };
        if let Err(error) = result {
            record.error = sanitize_error(&error.message, &inputs);
            report.steps.push(record);
            report.error = report
                .steps
                .last()
                .map(|step| step.error.clone())
                .unwrap_or_default();
            report.duration_ms = started.elapsed().as_millis();
            return Err(RunError {
                step_index: index,
                action,
                message: report.error.clone(),
            });
        }
        report.steps.push(record);
    }
    for output in &options.flow.outputs {
        let value = match output.from.as_str() {
            "url" => executor.execute("get.url", json!({})),
            "html" => executor.execute("get.html", json!({})),
            "text" => executor.execute("get.text", json!({"selector": output.path})),
            "attribute" => {
                let Some((selector, attribute)) = output.path.split_once('@') else {
                    return fail(
                        &mut report,
                        started,
                        options.flow.steps.len(),
                        "outputs",
                        format!(
                            "output {:?}: attribute path must be selector@attribute",
                            output.name
                        ),
                    );
                };
                executor.execute(
                    "get.attr",
                    json!({"selector": selector, "attribute": attribute}),
                )
            }
            other => {
                return fail(
                    &mut report,
                    started,
                    options.flow.steps.len(),
                    "outputs",
                    format!("output {:?}: unsupported source {:?}", output.name, other),
                );
            }
        };
        match value {
            Ok(value) => {
                report
                    .outputs
                    .insert(output.name.clone(), response_string(value));
            }
            Err(error) => {
                return fail(
                    &mut report,
                    started,
                    options.flow.steps.len(),
                    "outputs",
                    format!(
                        "extract output {:?}: {}",
                        output.name,
                        sanitize_error(&error.message, &inputs)
                    ),
                );
            }
        }
    }
    report.success = true;
    report.duration_ms = started.elapsed().as_millis();
    Ok(report)
}

pub async fn run_async<E: AsyncExecutor>(
    executor: &mut E,
    options: RunOptions,
) -> Result<RunReport, RunError> {
    let started = Instant::now();
    let inputs = resolve_inputs(&options.flow, &options.inputs).map_err(|message| RunError {
        step_index: 0,
        action: "inputs".to_owned(),
        message,
    })?;
    let mut report = RunReport {
        name: options.flow.name.clone(),
        version: options.flow.version,
        dry_run: options.dry_run,
        success: false,
        steps: Vec::new(),
        plan: Vec::new(),
        outputs: BTreeMap::new(),
        error: String::new(),
        duration_ms: 0,
    };
    if options.dry_run {
        report.plan = dry_run(&options.flow);
        report.success = true;
        report.duration_ms = started.elapsed().as_millis();
        return Ok(report);
    }
    enforce_domains(&options.flow, &inputs)?;
    for (index, step) in options.flow.steps.iter().enumerate() {
        let step_started = Instant::now();
        let action = step.action.clone();
        let result = execute_step_async(executor, step, &inputs).await;
        let mut record = StepRun {
            index,
            action: action.clone(),
            risk_class: risk_class(&action).to_owned(),
            success: result.is_ok(),
            error: String::new(),
            data: result.as_ref().ok().cloned(),
            duration_ms: step_started.elapsed().as_millis(),
        };
        if let Err(error) = result {
            record.error = sanitize_error(&error.message, &inputs);
            report.steps.push(record);
            report.error = report
                .steps
                .last()
                .map(|step| step.error.clone())
                .unwrap_or_default();
            report.duration_ms = started.elapsed().as_millis();
            return Err(RunError {
                step_index: index,
                action,
                message: report.error.clone(),
            });
        }
        report.steps.push(record);
    }
    for output in &options.flow.outputs {
        let value = match output.from.as_str() {
            "url" => executor.execute("get.url", json!({})).await,
            "html" => executor.execute("get.html", json!({})).await,
            "text" => {
                executor
                    .execute("get.text", json!({"selector": output.path}))
                    .await
            }
            "attribute" => {
                let Some((selector, attribute)) = output.path.split_once('@') else {
                    return fail(
                        &mut report,
                        started,
                        options.flow.steps.len(),
                        "outputs",
                        format!(
                            "output {:?}: attribute path must be selector@attribute",
                            output.name
                        ),
                    );
                };
                executor
                    .execute(
                        "get.attr",
                        json!({"selector": selector, "attribute": attribute}),
                    )
                    .await
            }
            other => {
                return fail(
                    &mut report,
                    started,
                    options.flow.steps.len(),
                    "outputs",
                    format!("output {:?}: unsupported source {:?}", output.name, other),
                );
            }
        };
        match value {
            Ok(value) => {
                report
                    .outputs
                    .insert(output.name.clone(), response_string(value));
            }
            Err(error) => {
                return fail(
                    &mut report,
                    started,
                    options.flow.steps.len(),
                    "outputs",
                    format!(
                        "extract output {:?}: {}",
                        output.name,
                        sanitize_error(&error.message, &inputs)
                    ),
                );
            }
        }
    }
    report.success = true;
    report.duration_ms = started.elapsed().as_millis();
    Ok(report)
}

async fn execute_step_async<E: AsyncExecutor>(
    executor: &mut E,
    step: &Step,
    inputs: &BTreeMap<String, String>,
) -> Result<Value, ExecutionError> {
    match step.action.as_str() {
        "open" => {
            executor
                .execute(
                    "open",
                    json!({"url": substitute(step.field("url"), inputs)}),
                )
                .await
        }
        "find" => executor.execute("find", step_args(step, inputs)).await,
        "click" | "fill" => {
            let reference = executor
                .execute("find", step_args(step, inputs))
                .await?
                .get("ref")
                .and_then(Value::as_str)
                .map(str::to_owned)
                .ok_or_else(|| ExecutionError::new("find returned no element ref"))?;
            executor
                .execute(
                    "scrollintoview",
                    json!({"selector": format!("@{reference}")}),
                )
                .await?;
            let command = step.action.as_str();
            let mut args = json!({"selector": format!("@{reference}")});
            if command == "fill" {
                args["value"] = Value::String(substitute(step.field("value"), inputs));
            }
            executor.execute(command, args).await
        }
        "wait" => {
            let args = if !step.field("url").is_empty() {
                json!({"url": step.field("url")})
            } else if !step.field("visible").is_empty() {
                json!({"visible": step.field("visible")})
            } else {
                json!({"ms": step.field("ms").parse::<u64>().unwrap_or_default()})
            };
            executor.execute("wait", args).await
        }
        "assert" => {
            if !step.field("url").is_empty() {
                let actual = response_string(executor.execute("get.url", json!({})).await?);
                if !glob_match(step.field("url"), &actual) {
                    return Err(ExecutionError::new(format!(
                        "assert url {:?} failed: current url is {actual:?}",
                        step.field("url")
                    )));
                }
                Ok(json!({"expected": step.field("url"), "actual": actual}))
            } else if !step.field("not").is_empty() {
                if executor
                    .execute("find", json!({"kind":"text", "query":step.field("not")}))
                    .await
                    .is_ok()
                {
                    return Err(ExecutionError::new(format!(
                        "assert not {:?} failed: element is present",
                        step.field("not")
                    )));
                }
                Ok(json!({"absent": step.field("not")}))
            } else {
                executor.execute("find", json!({"kind":"text", "query": if !step.field("visible").is_empty() { step.field("visible") } else { step.field("text") }})).await
            }
        }
        "snapshot" => {
            executor
                .execute(
                    "snapshot",
                    json!({"compact": step.field("compact"), "diff": step.field("diff")}),
                )
                .await
        }
        other => Err(ExecutionError::new(format!("unsupported step {other:?}"))),
    }
}

fn fail<T>(
    report: &mut RunReport,
    started: Instant,
    index: usize,
    action: &str,
    message: String,
) -> Result<T, RunError> {
    report.error = message.clone();
    report.duration_ms = started.elapsed().as_millis();
    Err(RunError {
        step_index: index,
        action: action.to_owned(),
        message,
    })
}

fn resolve_inputs(
    flow: &Flow,
    inputs: &BTreeMap<String, String>,
) -> Result<BTreeMap<String, String>, String> {
    let mut resolved = inputs.clone();
    let mut missing = Vec::new();
    for name in &flow.inputs {
        if !resolved.contains_key(name) {
            missing.push(name.clone());
        }
    }
    for step in &flow.steps {
        for value in step.fields.values() {
            for name in input_references(value) {
                if !resolved.contains_key(&name) && !missing.contains(&name) {
                    missing.push(name);
                }
            }
        }
    }
    if missing.is_empty() {
        Ok(std::mem::take(&mut resolved))
    } else {
        Err(format!("missing required inputs: {}", missing.join(", ")))
    }
}

fn input_references(value: &str) -> Vec<String> {
    let mut result = Vec::new();
    let mut rest = value;
    while let Some(start) = rest.find("{{") {
        let tail = &rest[start + 2..];
        let Some(end) = tail.find("}}") else { break };
        let name = tail[..end].trim();
        if !name.is_empty() && !result.iter().any(|item| item == name) {
            result.push(name.to_owned());
        }
        rest = &tail[end + 2..];
    }
    result
}

fn substitute(value: &str, inputs: &BTreeMap<String, String>) -> String {
    let mut value = value.to_owned();
    for (name, replacement) in inputs {
        value = value.replace(&format!("{{{{{name}}}}}"), replacement);
    }
    value
}

fn enforce_domains(flow: &Flow, inputs: &BTreeMap<String, String>) -> Result<(), RunError> {
    for (index, step) in flow.steps.iter().enumerate() {
        if step.action != "open" {
            continue;
        }
        let raw = substitute(step.field("url"), inputs);
        let Ok(parsed) = Url::parse(&raw) else {
            return Err(RunError {
                step_index: index,
                action: "open".to_owned(),
                message: format!("cannot parse open URL {raw:?}"),
            });
        };
        let Some(host) = parsed.host_str() else {
            return Err(RunError {
                step_index: index,
                action: "open".to_owned(),
                message: "open URL has no host".to_owned(),
            });
        };
        if !flow
            .domains
            .iter()
            .any(|pattern| domain_allowed(host, pattern))
        {
            return Err(RunError {
                step_index: index,
                action: "open".to_owned(),
                message: format!(
                    "domain {host:?} is not allowed by flow domains {:?}",
                    flow.domains
                ),
            });
        }
    }
    Ok(())
}

fn domain_allowed(host: &str, pattern: &str) -> bool {
    let host = host.trim_end_matches('.').to_ascii_lowercase();
    let pattern = pattern
        .split(':')
        .next()
        .unwrap_or(pattern)
        .trim_end_matches('.')
        .to_ascii_lowercase();
    pattern == host
        || (pattern
            .strip_prefix("*.")
            .is_some_and(|suffix| host.ends_with(&format!(".{suffix}")) && host != suffix))
}

fn execute_step<E: Executor>(
    executor: &mut E,
    step: &Step,
    inputs: &BTreeMap<String, String>,
) -> Result<Value, ExecutionError> {
    match step.action.as_str() {
        "open" => executor.execute(
            "open",
            json!({"url": substitute(step.field("url"), inputs)}),
        ),
        "find" => executor.execute("find", step_args(step, inputs)),
        "click" | "fill" => {
            let reference = executor.execute("find", step_args(step, inputs))?;
            let reference = reference
                .get("ref")
                .and_then(Value::as_str)
                .ok_or_else(|| ExecutionError::new("find returned no element ref"))?;
            executor.execute(
                "scrollintoview",
                json!({"selector": format!("@{reference}")}),
            )?;
            let command = step.action.as_str();
            let mut args = json!({"selector": format!("@{reference}")});
            if command == "fill" {
                args["value"] = Value::String(substitute(step.field("value"), inputs));
            }
            executor.execute(command, args)
        }
        "wait" => {
            let args = if !step.field("url").is_empty() {
                json!({"url": step.field("url")})
            } else if !step.field("visible").is_empty() {
                json!({"visible": step.field("visible")})
            } else {
                json!({"ms": step.field("ms").parse::<u64>().unwrap_or_default()})
            };
            executor.execute("wait", args)
        }
        "assert" => {
            if !step.field("url").is_empty() {
                let actual = response_string(executor.execute("get.url", json!({}))?);
                if !glob_match(step.field("url"), &actual) {
                    return Err(ExecutionError::new(format!(
                        "assert url {:?} failed: current url is {actual:?}",
                        step.field("url")
                    )));
                }
                Ok(json!({"expected": step.field("url"), "actual": actual}))
            } else if !step.field("not").is_empty() {
                if executor
                    .execute("find", json!({"kind":"text", "query":step.field("not")}))
                    .is_ok()
                {
                    return Err(ExecutionError::new(format!(
                        "assert not {:?} failed: element is present",
                        step.field("not")
                    )));
                }
                Ok(json!({"absent": step.field("not")}))
            } else {
                let query = if !step.field("visible").is_empty() {
                    step.field("visible")
                } else {
                    step.field("text")
                };
                executor.execute("find", json!({"kind":"text", "query":query}))
            }
        }
        "snapshot" => executor.execute(
            "snapshot",
            json!({"compact": step.field("compact"), "diff": step.field("diff")}),
        ),
        other => Err(ExecutionError::new(format!("unsupported step {other:?}"))),
    }
}

fn step_args(step: &Step, inputs: &BTreeMap<String, String>) -> Value {
    let mut args = serde_json::Map::new();
    for (key, value) in &step.fields {
        args.insert(key.clone(), Value::String(substitute(value, inputs)));
    }
    Value::Object(args)
}

fn response_string(value: Value) -> String {
    value
        .as_str()
        .map_or_else(|| value.to_string(), ToOwned::to_owned)
}

fn glob_match(pattern: &str, value: &str) -> bool {
    let mut regex = String::from("^");
    let mut chars = pattern.chars().peekable();
    while let Some(ch) = chars.next() {
        if ch == '*' {
            if chars.peek() == Some(&'*') {
                chars.next();
                regex.push_str(".*");
            } else {
                regex.push_str("[^/]*");
            }
        } else {
            regex.push_str(&regex_escape(ch));
        }
    }
    regex.push('$');
    simple_regex_match(&regex, value)
}

fn regex_escape(ch: char) -> String {
    if ".+()[]{}^$|\\".contains(ch) {
        format!("\\{ch}")
    } else {
        ch.to_string()
    }
}

fn simple_regex_match(pattern: &str, value: &str) -> bool {
    // URL globs only need anchored literals, `.*` and `[^/]*`; this matcher
    // avoids adding a regex dependency to the core crate.
    fn matches(p: &[u8], v: &[u8]) -> bool {
        if p.is_empty() {
            return v.is_empty();
        }
        if p.starts_with(b".*") {
            return matches(&p[2..], v) || (!v.is_empty() && matches(p, &v[1..]));
        }
        if p.starts_with(b"[^/]*") {
            return matches(&p[5..], v) || (!v.is_empty() && v[0] != b'/' && matches(p, &v[1..]));
        }
        if p[0] == b'\\' && p.len() > 1 {
            return !v.is_empty() && p[1] == v[0] && matches(&p[2..], &v[1..]);
        }
        !v.is_empty() && p[0] == v[0] && matches(&p[1..], &v[1..])
    }
    pattern
        .strip_prefix('^')
        .and_then(|p| p.strip_suffix('$'))
        .is_some_and(|p| matches(p.as_bytes(), value.as_bytes()))
}

fn risk_class(action: &str) -> &'static str {
    match action {
        "open" | "wait" => "navigate",
        "find" | "click" | "fill" => "interact",
        _ => "read",
    }
}

fn sanitize_error(message: &str, inputs: &BTreeMap<String, String>) -> String {
    inputs
        .values()
        .filter(|value| !value.is_empty())
        .fold(message.to_owned(), |message, value| {
            message.replace(value, "[REDACTED]")
        })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::flows;

    fn flow() -> Flow {
        flows::parse(
            br#"name: run
version: 1
domains: [example.test]
inputs: [email]
steps:
  - open: {url: https://example.test/start}
  - fill: {label: Email, value: '{{email}}'}
  - assert: {url: '**/start'}
  - click: {label: Continue}
outputs:
  - {name: final_url, from: url}
"#,
            "test",
        )
        .unwrap()
    }

    #[test]
    fn executes_steps_in_order_and_redacts_executor_errors() {
        let mut seen = Vec::new();
        let mut executor = |command: &str, args: Value| {
            seen.push(command.to_owned());
            if command == "find" && args.get("label").is_some() {
                return Ok(json!({"ref":"e1"}));
            }
            if command == "get.url" {
                return Ok(json!("https://example.test/start"));
            }
            Ok(json!({"ok": true}))
        };
        let report = run(
            &mut executor,
            RunOptions {
                flow: flow(),
                inputs: BTreeMap::from([(String::from("email"), String::from("secret-value"))]),
                dry_run: false,
            },
        )
        .unwrap();
        assert!(report.success);
        assert_eq!(
            seen,
            [
                "open",
                "find",
                "scrollintoview",
                "fill",
                "get.url",
                "find",
                "scrollintoview",
                "click",
                "get.url"
            ]
        );
    }

    #[test]
    fn missing_inputs_and_foreign_domains_stop_before_executor() {
        let mut calls = 0;
        let mut executor = |_: &str, _: Value| {
            calls += 1;
            Ok(json!({}))
        };
        let error = run(
            &mut executor,
            RunOptions {
                flow: flow(),
                inputs: BTreeMap::new(),
                dry_run: false,
            },
        )
        .unwrap_err();
        assert!(error.message.contains("missing required inputs"));
        assert_eq!(calls, 0);
    }

    #[test]
    fn glob_match_preserves_path_segment_star() {
        assert!(glob_match("**/done/*", "https://example.test/done/1"));
        assert!(!glob_match("**/done/*", "https://example.test/done/a/b"));
    }
}
