#![deny(unsafe_code)]

//! Unified JSON, YAML and human output envelope contracts.

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::error::ErrorCode;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Format {
    Text,
    Json,
    Yaml,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct Warning {
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub severity: String,
    pub message: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub r#ref: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub excerpt: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct ErrorPayload {
    pub code: ErrorCode,
    pub message: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub hint: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub details: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub retryable: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub requires_user_confirmation: Option<bool>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub resume_hint: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct Envelope {
    pub success: bool,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub data: Value,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<Warning>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ErrorPayload>,
}

impl Envelope {
    #[must_use]
    pub fn ok(data: Value, warnings: Vec<Warning>) -> Self {
        Self {
            success: true,
            data,
            warnings,
            error: None,
        }
    }

    #[must_use]
    pub fn failure(code: ErrorCode, message: impl Into<String>) -> Self {
        Self {
            success: false,
            data: Value::Null,
            warnings: Vec::new(),
            error: Some(ErrorPayload {
                code,
                message: message.into(),
                hint: String::new(),
                details: None,
                retryable: None,
                requires_user_confirmation: None,
                resume_hint: String::new(),
            }),
        }
    }

    /// Renders the selected public output format with a trailing newline.
    pub fn render(&self, format: Format) -> Result<String, RenderError> {
        match format {
            Format::Json => {
                let mut output = serde_json::to_string(self).map_err(RenderError::Json)?;
                output.push('\n');
                Ok(output)
            }
            Format::Yaml => render_yaml(self),
            Format::Text => Ok(render_human(self)),
        }
    }
}

#[derive(Debug)]
pub enum RenderError {
    Json(serde_json::Error),
}

impl core::fmt::Display for RenderError {
    fn fmt(&self, formatter: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            Self::Json(error) => write!(formatter, "serialise envelope as json: {error}"),
        }
    }
}

impl std::error::Error for RenderError {}

fn render_yaml(envelope: &Envelope) -> Result<String, RenderError> {
    let mut output = format!("success: {}\n", envelope.success);
    write_yaml_field(&mut output, "data", &envelope.data, 0);
    write_yaml_warnings(&mut output, &envelope.warnings);
    write_yaml_error(&mut output, envelope.error.as_ref())?;
    Ok(output)
}

fn write_yaml_warnings(output: &mut String, warnings: &[Warning]) {
    if warnings.is_empty() {
        output.push_str("warnings: []\n");
        return;
    }
    output.push_str("warnings:\n");
    for warning in warnings {
        output.push_str("    - kind: ");
        output.push_str(&yaml_string(&warning.kind));
        output.push('\n');
        for (key, value) in [
            ("severity", warning.severity.as_str()),
            ("message", warning.message.as_str()),
            ("ref", warning.r#ref.as_str()),
            ("excerpt", warning.excerpt.as_str()),
        ] {
            if !value.is_empty() {
                output.push_str("      ");
                output.push_str(key);
                output.push_str(": ");
                output.push_str(&yaml_string(value));
                output.push('\n');
            }
        }
    }
}

fn write_yaml_error(output: &mut String, error: Option<&ErrorPayload>) -> Result<(), RenderError> {
    let Some(error) = error else {
        output.push_str("error: null\n");
        return Ok(());
    };
    output.push_str("error:\n");
    let code = serde_json::to_value(error.code).map_err(RenderError::Json)?;
    write_yaml_field(output, "code", &code, 4);
    write_yaml_field(output, "message", &Value::String(error.message.clone()), 4);
    if !error.hint.is_empty() {
        write_yaml_field(output, "hint", &Value::String(error.hint.clone()), 4);
    }
    write_yaml_field(
        output,
        "details",
        error
            .details
            .as_ref()
            .unwrap_or(&Value::Object(Default::default())),
        4,
    );
    write_yaml_field(
        output,
        "retryable",
        &error.retryable.map_or(Value::Null, Value::Bool),
        4,
    );
    write_yaml_field(
        output,
        "requiresuserconfirmation",
        &error
            .requires_user_confirmation
            .map_or(Value::Null, Value::Bool),
        4,
    );
    write_yaml_field(
        output,
        "resumehint",
        &Value::String(error.resume_hint.clone()),
        4,
    );
    Ok(())
}

pub(crate) fn write_yaml_field(output: &mut String, key: &str, value: &Value, indent: usize) {
    output.push_str(&" ".repeat(indent));
    output.push_str(&yaml_key(key));
    output.push(':');
    match value {
        Value::Object(values) if !values.is_empty() => {
            output.push('\n');
            for (child_key, child_value) in values {
                write_yaml_field(output, child_key, child_value, indent + 4);
            }
        }
        Value::Array(values) if !values.is_empty() => {
            output.push('\n');
            for item in values {
                write_yaml_sequence_item(output, item, indent + 4);
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

pub(crate) fn write_yaml_sequence_item(output: &mut String, value: &Value, indent: usize) {
    output.push_str(&" ".repeat(indent));
    output.push('-');
    match value {
        Value::Object(values) if !values.is_empty() => {
            let mut fields = values.iter();
            if let Some((key, first)) = fields.next() {
                output.push(' ');
                output.push_str(&yaml_key(key));
                output.push(':');
                if first.is_object() || first.is_array() {
                    output.push('\n');
                    write_yaml_children(output, first, indent + 4);
                } else {
                    output.push(' ');
                    output.push_str(&yaml_scalar(first));
                    output.push('\n');
                }
            }
            for (key, child) in fields {
                write_yaml_field(output, key, child, indent + 2);
            }
        }
        _ => {
            output.push(' ');
            output.push_str(&yaml_scalar(value));
            output.push('\n');
        }
    }
}

fn write_yaml_children(output: &mut String, value: &Value, indent: usize) {
    match value {
        Value::Object(values) => {
            for (key, child) in values {
                write_yaml_field(output, key, child, indent);
            }
        }
        Value::Array(values) => {
            for child in values {
                write_yaml_sequence_item(output, child, indent);
            }
        }
        _ => {}
    }
}

fn yaml_key(value: &str) -> String {
    if needs_yaml_quotes(value) {
        serde_json::to_string(value).expect("string serialization cannot fail")
    } else {
        value.to_owned()
    }
}

pub(crate) fn yaml_scalar(value: &Value) -> String {
    match value {
        Value::Null => "null".to_owned(),
        Value::Bool(value) => value.to_string(),
        Value::Number(value) => value.to_string(),
        Value::String(value) => yaml_string(value),
        Value::Array(_) | Value::Object(_) => "null".to_owned(),
    }
}

fn yaml_string(value: &str) -> String {
    if value.starts_with('@') || value.starts_with('`') {
        return format!("'{}'", value.replace('\'', "''"));
    }
    if needs_yaml_quotes(value) {
        return serde_json::to_string(value).expect("string serialization cannot fail");
    }
    value.to_owned()
}

fn needs_yaml_quotes(value: &str) -> bool {
    if value.is_empty() || value.contains(['\n', '\r', '\t']) || value.contains(": ") {
        return true;
    }
    matches!(
        value.to_ascii_lowercase().as_str(),
        "y" | "yes" | "n" | "no" | "true" | "false" | "on" | "off" | "null" | "~"
    )
}

fn render_human(envelope: &Envelope) -> String {
    if !envelope.success {
        return match &envelope.error {
            Some(error) => format!("{}\n", error.message),
            None => "error\n".to_owned(),
        };
    }
    if envelope.data.is_null() {
        return "ok\n".to_owned();
    }
    if let Some(text) = envelope.data.as_str() {
        return format!("{text}\n");
    }
    if let Some(marker) = truncation_marker(&envelope.data) {
        return format!(
            "{}\n\n… [truncated: {} of {} tokens] …\n\n{}\n\nfull output: {}\n",
            marker.head, marker.tokens_returned, marker.tokens_total, marker.foot, marker.hint
        );
    }
    let mut output = serde_json::to_string_pretty(&envelope.data)
        .expect("serde_json::Value serialization cannot fail");
    output.push('\n');
    output
}

struct TruncationMarker<'a> {
    head: &'a str,
    foot: &'a str,
    hint: &'a str,
    tokens_returned: u64,
    tokens_total: u64,
}

fn truncation_marker(value: &Value) -> Option<TruncationMarker<'_>> {
    let fields = value.as_object()?;
    if !fields.get("truncated")?.as_bool()? {
        return None;
    }
    Some(TruncationMarker {
        head: fields
            .get("head")
            .and_then(Value::as_str)
            .unwrap_or_default(),
        foot: fields
            .get("foot")
            .and_then(Value::as_str)
            .unwrap_or_default(),
        hint: fields
            .get("hint")
            .and_then(Value::as_str)
            .unwrap_or_default(),
        tokens_returned: fields
            .get("tokens_returned")
            .and_then(Value::as_u64)
            .unwrap_or_default(),
        tokens_total: fields
            .get("tokens_total")
            .and_then(Value::as_u64)
            .unwrap_or_default(),
    })
}
