use serde_json::{Map, Value};
use std::collections::BTreeMap;

const REDACTED: &str = "[REDACTED]";
const SECRET_KEYS: &[&str] = &[
    "password",
    "passwd",
    "pass",
    "secret",
    "token",
    "api_key",
    "apikey",
    "access_key",
    "auth",
    "authorization",
    "cookie",
    "set-cookie",
    "client_secret",
    "private_key",
    "encryption_key",
    "credential",
    "credentials",
];

/// Stateless secret scrubber shared by CLI, daemon errors and MCP metadata.
#[derive(Clone, Copy, Debug, Default)]
pub struct Redactor;

impl Redactor {
    #[must_use]
    pub fn redact_str(self, input: &str) -> String {
        redact_str(input)
    }
    #[must_use]
    pub fn redact_json(self, value: &Value) -> Value {
        redact_json(value)
    }
    #[must_use]
    pub fn redact_args(self, args: &[String]) -> Vec<String> {
        redact_args(args)
    }
    #[must_use]
    pub fn redact_env(self, env: &[(String, String)]) -> BTreeMap<String, String> {
        redact_env(env)
    }
}

#[must_use]
pub fn redact_str(input: &str) -> String {
    let mut output = input.to_owned();
    for key in SECRET_KEYS {
        for separator in ["=", ":", " "] {
            let mut cursor = 0;
            while let Some(relative) = output[cursor..]
                .to_ascii_lowercase()
                .find(&format!("{key}{separator}"))
            {
                let start = cursor + relative;
                let mut value_start = start + key.len() + separator.len();
                while output
                    .as_bytes()
                    .get(value_start)
                    .is_some_and(u8::is_ascii_whitespace)
                {
                    value_start += 1;
                }
                let end = value_end(&output, value_start);
                if end <= value_start {
                    cursor = value_start;
                    continue;
                }
                output.replace_range(value_start..end, REDACTED);
                cursor = value_start + REDACTED.len();
            }
        }
    }
    redact_url_credentials(&output)
}

fn value_end(text: &str, start: usize) -> usize {
    let bytes = text.as_bytes();
    let mut end = start;
    let quoted = bytes
        .get(start)
        .copied()
        .is_some_and(|b| b == b'"' || b == b'\'');
    let quote = bytes.get(start).copied();
    if quoted {
        end += 1;
    }
    while end < bytes.len() {
        let byte = bytes[end];
        if quoted && Some(byte) == quote {
            return end + 1;
        }
        if !quoted
            && matches!(
                byte,
                b' ' | b'\t' | b'\r' | b'\n' | b',' | b'}' | b']' | b';'
            )
        {
            break;
        }
        end += 1;
    }
    end
}

fn redact_url_credentials(input: &str) -> String {
    let mut output = input.to_owned();
    let mut cursor = 0;
    while let Some(relative) = output[cursor..].find("://") {
        let scheme_end = cursor + relative;
        let authority_start = scheme_end + 3;
        let authority_end = output[authority_start..]
            .find(['/', ' ', '\n', '\r', '"', '\''])
            .map_or(output.len(), |offset| authority_start + offset);
        if let Some(at) = output[authority_start..authority_end].find('@') {
            let userinfo_end = authority_start + at;
            if let Some(colon) = output[authority_start..userinfo_end].find(':') {
                let secret_start = authority_start + colon + 1;
                output.replace_range(secret_start..userinfo_end, REDACTED);
                cursor = secret_start + REDACTED.len();
                continue;
            }
        }
        cursor = authority_end;
    }
    output
}

#[must_use]
pub fn redact_args(args: &[String]) -> Vec<String> {
    let mut output = Vec::with_capacity(args.len());
    let mut redact_next = false;
    for arg in args {
        let lower = arg.to_ascii_lowercase();
        if redact_next {
            output.push(REDACTED.to_owned());
            redact_next = false;
        } else if SECRET_KEYS
            .iter()
            .any(|key| lower == format!("--{key}") || lower == format!("-{key}"))
        {
            output.push(arg.clone());
            redact_next = true;
        } else if SECRET_KEYS.iter().any(|key| {
            lower.starts_with(&format!("--{key}=")) || lower.starts_with(&format!("{key}="))
        }) {
            let split = arg.find('=').unwrap_or(arg.len());
            output.push(format!("{}={REDACTED}", &arg[..split]));
        } else {
            output.push(redact_str(arg));
        }
    }
    output
}

#[must_use]
pub fn redact_env(env: &[(String, String)]) -> BTreeMap<String, String> {
    env.iter()
        .map(|(key, value)| {
            let lower = key.to_ascii_lowercase();
            let secret =
                SECRET_KEYS.iter().any(|name| lower.contains(name)) || lower.ends_with("_key");
            (
                key.clone(),
                if secret {
                    REDACTED.to_owned()
                } else {
                    redact_str(value)
                },
            )
        })
        .collect()
}

#[must_use]
pub fn redact_json(value: &Value) -> Value {
    match value {
        Value::Object(object) => {
            let mut result = Map::new();
            for (key, value) in object {
                let secret = SECRET_KEYS.iter().any(|name| {
                    key.eq_ignore_ascii_case(name) || key.to_ascii_lowercase().contains(name)
                });
                result.insert(
                    key.clone(),
                    if secret {
                        Value::String(REDACTED.into())
                    } else {
                        redact_json(value)
                    },
                );
            }
            Value::Object(result)
        }
        Value::Array(values) => Value::Array(values.iter().map(redact_json).collect()),
        Value::String(text) => Value::String(redact_str(text)),
        _ => value.clone(),
    }
}

pub(crate) fn redact_error(mut error: crate::DaemonError) -> crate::DaemonError {
    error.message = redact_str(&error.message);
    error.hint = redact_str(&error.hint);
    error.resume_hint = redact_str(&error.resume_hint);
    error.details = error.details.as_ref().map(redact_json);
    error
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn corpus_secrets_do_not_survive_any_surface() {
        let text = "password=topsecret token: bearer-secret https://user:pw@example.com/x";
        let output = redact_str(text);
        for secret in ["topsecret", "bearer-secret", "pw"] {
            assert!(!output.contains(secret), "{output}");
        }
        let args = redact_args(&[
            "--token".into(),
            "abc".into(),
            "--url=https://u:p@example.com".into(),
        ]);
        assert_eq!(args[1], REDACTED);
        assert!(!args.join(" ").contains("abc"));
        let json = redact_json(
            &serde_json::json!({"password":"abc", "nested":{"api_key":"xyz"}, "ok":true}),
        );
        assert!(!json.to_string().contains("abc"));
        assert!(!json.to_string().contains("xyz"));
    }
}
