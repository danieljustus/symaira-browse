#![deny(unsafe_code)]

//! Deterministic batch planning and execution contracts.

use std::time::Instant;

use serde::Serialize;
use serde_json::Value;

use crate::output::{write_yaml_field, write_yaml_sequence_item, yaml_scalar};

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum RiskClass {
    Read,
    Navigate,
    Interact,
    Submit,
    Eval,
    Credential,
    Download,
    Upload,
    NetworkMock,
    Unknown,
}

#[derive(Debug, Eq, PartialEq, Serialize)]
pub struct PlanItem {
    pub command: String,
    pub risk_class: RiskClass,
}

#[derive(Debug, PartialEq, Serialize)]
pub struct ResultItem {
    pub command: String,
    pub success: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    pub duration_ms: u128,
}

#[derive(Debug, Default, PartialEq, Serialize)]
pub struct Report {
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub results: Vec<ResultItem>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub plan: Vec<PlanItem>,
    #[serde(skip_serializing_if = "is_false")]
    pub bailed: bool,
}

#[derive(Debug, PartialEq)]
pub struct ItemOutput {
    pub stdout: String,
    pub error: Option<String>,
}

pub fn run<F>(commands: &[String], dry_run: bool, bail: bool, mut execute: F) -> Report
where
    F: FnMut(&[String]) -> ItemOutput,
{
    if dry_run {
        return Report {
            plan: commands
                .iter()
                .map(|command| PlanItem {
                    command: command.clone(),
                    risk_class: classify(&command_name(command)),
                })
                .collect(),
            ..Report::default()
        };
    }

    let mut report = Report::default();
    for command in commands {
        let argv = match tokenize(command) {
            Ok(argv) if !argv.is_empty() => argv,
            Ok(_) => {
                report.results.push(failed(command, "empty command"));
                if bail {
                    report.bailed = true;
                    break;
                }
                continue;
            }
            Err(error) => {
                report.results.push(failed(command, &error));
                if bail {
                    report.bailed = true;
                    break;
                }
                continue;
            }
        };

        let started = Instant::now();
        let output = execute(&argv);
        let duration_ms = started.elapsed().as_millis();
        if let Some(error) = output.error {
            report.results.push(ResultItem {
                command: command.clone(),
                success: false,
                data: None,
                error: Some(error),
                duration_ms,
            });
            if bail {
                report.bailed = true;
                break;
            }
            continue;
        }
        report.results.push(ResultItem {
            command: command.clone(),
            success: true,
            data: decode_output(
                &output.stdout,
                argv.iter().any(|argument| argument == "--json"),
            ),
            error: None,
            duration_ms,
        });
    }
    report
}

fn failed(command: &str, error: &str) -> ResultItem {
    ResultItem {
        command: command.to_owned(),
        success: false,
        data: None,
        error: Some(error.to_owned()),
        duration_ms: 0,
    }
}

fn decode_output(output: &str, structured: bool) -> Option<Value> {
    let trimmed = output.trim_end_matches('\n');
    if trimmed.is_empty() {
        return None;
    }
    if structured {
        return serde_json::from_str(trimmed)
            .ok()
            .or_else(|| Some(Value::String(trimmed.to_owned())));
    }
    Some(Value::String(trimmed.to_owned()))
}

pub fn tokenize(input: &str) -> Result<Vec<String>, String> {
    let mut args = Vec::new();
    let mut current = String::new();
    let mut quote = None;
    let mut started = false;

    for character in input.chars() {
        if let Some(delimiter) = quote {
            if character == delimiter {
                quote = None;
            } else {
                current.push(character);
            }
            continue;
        }
        match character {
            '\'' | '"' => {
                quote = Some(character);
                started = true;
            }
            ' ' | '\t' => {
                if started {
                    args.push(std::mem::take(&mut current));
                    started = false;
                }
            }
            _ => {
                current.push(character);
                started = true;
            }
        }
    }
    if quote.is_some() {
        return Err("unbalanced quotes in command".to_owned());
    }
    if started {
        args.push(current);
    }
    Ok(args)
}

fn command_name(command: &str) -> String {
    tokenize(command)
        .ok()
        .and_then(|arguments| arguments.into_iter().next())
        .unwrap_or_default()
}

pub fn classify(command: &str) -> RiskClass {
    match command {
        "snapshot" | "screenshot" | "a11y" | "read" | "console.list" | "console.clear"
        | "errors.list" | "errors.clear" | "get.text" | "get.html" | "get.value" | "get.attr"
        | "get.title" | "get.url" | "get.count" | "get.box" | "get.styles" | "is.visible"
        | "is.enabled" | "is.checked" | "find" | "session.list" | "session.info"
        | "daemon.status" | "state.list" | "state.show" | "journal.tail" | "journal.show"
        | "policy.explain" | "oob.status" | "cookies.list" | "storage.list" | "trace.replay"
        | "watch" | "cache.get" | "fetch.url" | "fetch.batch" | "wayback.snapshots"
        | "downloads.list" | "network.requests" | "network.request" | "network.har" => {
            RiskClass::Read
        }
        "open" | "goto" | "back" | "forward" | "reload" => RiskClass::Navigate,
        "click" | "dblclick" | "fill" | "type" | "press" | "hover" | "focus" | "select"
        | "check" | "uncheck" | "scroll" | "scrollintoview" | "wait" | "state.save"
        | "state.load" | "state.clear" | "state.clean" | "cookies.set" | "cookies.clear"
        | "storage.set" | "storage.clear" | "set.viewport" | "set.device" | "set.geo"
        | "set.media" | "set.user-agent" => RiskClass::Interact,
        "submit" => RiskClass::Submit,
        "eval" => RiskClass::Eval,
        "auth.login" => RiskClass::Credential,
        "download" | "download.setdir" => RiskClass::Download,
        "upload" => RiskClass::Upload,
        "network.route" | "network.unroute" | "set.headers" | "set.offline" => {
            RiskClass::NetworkMock
        }
        _ => RiskClass::Unknown,
    }
}

#[must_use]
pub fn render_yaml(report: &Report) -> String {
    let mut output = String::from("success: true\ndata:\n");
    if report.results.is_empty() {
        output.push_str("    results: []\n");
    } else {
        output.push_str("    results:\n");
        for item in &report.results {
            output.push_str("        - command: ");
            output.push_str(&yaml_scalar(&Value::String(item.command.clone())));
            output.push('\n');
            output.push_str(&format!("          success: {}\n", item.success));
            write_result_value(
                &mut output,
                "data",
                item.data.as_ref().unwrap_or(&Value::Null),
            );
            write_result_value(
                &mut output,
                "error",
                &Value::String(item.error.clone().unwrap_or_default()),
            );
            output.push_str(&format!("          durationms: {}\n", item.duration_ms));
        }
    }
    if report.plan.is_empty() {
        output.push_str("    plan: []\n");
    } else {
        output.push_str("    plan:\n");
        for item in &report.plan {
            output.push_str("        - command: ");
            output.push_str(&yaml_scalar(&Value::String(item.command.clone())));
            output.push('\n');
            output.push_str("          riskclass: ");
            output.push_str(risk_class_name(item.risk_class));
            output.push('\n');
        }
    }
    output.push_str(&format!(
        "    bailed: {}\nwarnings: []\nerror: null\n",
        report.bailed
    ));
    output
}

fn write_result_value(output: &mut String, key: &str, value: &Value) {
    output.push_str("          ");
    output.push_str(key);
    output.push(':');
    match value {
        Value::Object(values) if !values.is_empty() => {
            output.push('\n');
            for (child_key, child_value) in values {
                write_yaml_field(output, child_key, child_value, 12);
            }
        }
        Value::Array(values) if !values.is_empty() => {
            output.push('\n');
            for child in values {
                write_yaml_sequence_item(output, child, 12);
            }
        }
        Value::Object(_) => output.push_str(" {}\n"),
        Value::Array(_) => output.push_str(" []\n"),
        _ => {
            output.push(' ');
            output.push_str(&yaml_scalar(value));
            output.push('\n');
        }
    }
}

const fn risk_class_name(class: RiskClass) -> &'static str {
    match class {
        RiskClass::Read => "read",
        RiskClass::Navigate => "navigate",
        RiskClass::Interact => "interact",
        RiskClass::Submit => "submit",
        RiskClass::Eval => "eval",
        RiskClass::Credential => "credential",
        RiskClass::Download => "download",
        RiskClass::Upload => "upload",
        RiskClass::NetworkMock => "network-mock",
        RiskClass::Unknown => "unknown",
    }
}

const fn is_false(value: &bool) -> bool {
    !*value
}

#[cfg(test)]
mod tests {
    use super::{
        ItemOutput, PlanItem, Report, ResultItem, RiskClass, classify, render_yaml, run, tokenize,
    };
    use serde_json::json;

    #[test]
    fn tokenizes_quoted_values_and_rejects_unbalanced_quotes() {
        assert_eq!(
            tokenize("fill @e1 \"hello world\"").unwrap(),
            ["fill", "@e1", "hello world"]
        );
        assert_eq!(
            tokenize("type @e2 'hello world'").unwrap(),
            ["type", "@e2", "hello world"]
        );
        assert!(tokenize("\"unbalanced").is_err());
    }

    #[test]
    fn dry_run_plans_without_executing() {
        let commands = vec!["click @e1".to_owned(), "version".to_owned()];
        let mut executed = false;
        let report = run(&commands, true, false, |_| {
            executed = true;
            ItemOutput {
                stdout: String::new(),
                error: None,
            }
        });
        assert!(!executed);
        assert_eq!(report.plan[0].risk_class, RiskClass::Interact);
        assert_eq!(report.plan[1].risk_class, RiskClass::Unknown);
        assert!(report.results.is_empty());
    }

    #[test]
    fn failure_continues_or_bails_as_requested() {
        let commands = vec!["open https://example.com".to_owned(), "snapshot".to_owned()];
        let continue_report = run(&commands, false, false, |argv| ItemOutput {
            stdout: format!("ok:{}", argv[0]),
            error: (argv[0] == "open").then(|| "boom".to_owned()),
        });
        assert_eq!(continue_report.results.len(), 2);
        assert!(!continue_report.results[0].success);
        assert!(!continue_report.bailed);

        let bail_report = run(&commands, false, true, |_| ItemOutput {
            stdout: String::new(),
            error: Some("boom".to_owned()),
        });
        assert_eq!(bail_report.results.len(), 1);
        assert!(bail_report.bailed);
    }

    #[test]
    fn decodes_json_and_plain_text_results() {
        let commands = vec!["version --json".to_owned(), "version".to_owned()];
        let report = run(&commands, false, false, |argv| ItemOutput {
            stdout: if argv.contains(&"--json".to_owned()) {
                "{\"tool\":\"symbrowse\"}\n".to_owned()
            } else {
                "symbrowse dev\n".to_owned()
            },
            error: None,
        });
        assert_eq!(report.results[0].data, Some(json!({"tool": "symbrowse"})));
        assert_eq!(report.results[1].data, Some(json!("symbrowse dev")));
    }

    #[test]
    fn classification_matches_go_examples() {
        assert_eq!(classify("read"), RiskClass::Read);
        assert_eq!(classify("open"), RiskClass::Navigate);
        assert_eq!(classify("click"), RiskClass::Interact);
        assert_eq!(classify("version"), RiskClass::Unknown);
    }

    #[test]
    fn yaml_retains_go_struct_field_names_and_zero_values() {
        let report = Report {
            results: vec![ResultItem {
                command: "version --json".to_owned(),
                success: true,
                data: Some(json!({"schema_version": 8, "tool": "symbrowse"})),
                error: None,
                duration_ms: 7,
            }],
            plan: vec![PlanItem {
                command: "read https://example.com".to_owned(),
                risk_class: RiskClass::Read,
            }],
            bailed: false,
        };
        assert_eq!(
            render_yaml(&report),
            "success: true\ndata:\n    results:\n        - command: version --json\n          success: true\n          data:\n            schema_version: 8\n            tool: symbrowse\n          error: \"\"\n          durationms: 7\n    plan:\n        - command: read https://example.com\n          riskclass: read\n    bailed: false\nwarnings: []\nerror: null\n"
        );
    }
}
