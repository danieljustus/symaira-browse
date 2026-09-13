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
    let mut output = redact_url_credentials(input);
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
                let end = if output[value_start..].starts_with(REDACTED) {
                    // Preserve an existing marker, but scrub any appended value.
                    value_end(&output, value_start + REDACTED.len())
                } else {
                    value_end(&output, value_start)
                };
                if end <= value_start {
                    cursor = value_start;
                    continue;
                }
                output.replace_range(value_start..end, REDACTED);
                cursor = value_start + REDACTED.len();
            }
        }
    }
    output
}

fn value_end(text: &str, start: usize) -> usize {
    if let Some(end) = query_value_end(text, start) {
        return end;
    }
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

// URL query delimiters must not truncate generic password/token values.
// Apostrophes are legal URL data, including in userinfo and query values.
fn query_value_end(text: &str, start: usize) -> Option<usize> {
    // Keep the outer URL's query context when a public parameter contains a
    // nested URL. Whitespace/angle brackets separate it from surrounding prose.
    let token_start = text[..start]
        .rfind(|ch: char| ch.is_ascii_whitespace() || matches!(ch, '<' | '>'))
        .map_or(0, |offset| offset + 1);
    let scheme_end = token_start + text[token_start..start].find("://")?;
    let authority_start = scheme_end + 3;
    let prefix = &text[authority_start..start];
    if prefix.contains('#') || !prefix.contains('?') {
        return None;
    }
    let scheme_start = text[..scheme_end]
        .rfind(|ch: char| !ch.is_ascii_alphanumeric() && !matches!(ch, '+' | '-' | '.'))
        .map_or(0, |offset| offset + 1);
    let double_quoted_url = scheme_start > 0 && text.as_bytes()[scheme_start - 1] == b'"';
    // A leading value quote owns whitespace until its matching close. Debug
    // formatting adds an escape layer: \" closes a debug-quoted value, while
    // \\\" represents an escaped quote inside it and must not end that value.
    let quote_offset = text[start..].bytes().take_while(|b| *b == b'\\').count();
    let mut value_quote = text
        .as_bytes()
        .get(start + quote_offset)
        .copied()
        .filter(|b| matches!(b, b'"' | b'\''));
    let mut end = text.len();
    for (offset, ch) in text[start..].char_indices() {
        let index = start + offset;
        let escapes = if matches!(ch, '"' | '\'') {
            text[..index]
                .bytes()
                .rev()
                .take_while(|b| *b == b'\\')
                .count()
        } else {
            0
        };
        // A quote is URL data unless it closes a surrounding double quote.
        // Debug-formatted targets escape embedded quotes; count backslashes so
        // those quotes cannot expose a secret suffix as ordinary message text.
        // Only punctuation may follow the closing quote before a prose boundary.
        let closing_quote = double_quoted_url
            && (offset > quote_offset || text[..start].ends_with(REDACTED))
            && ch == '"'
            && escapes % 2 == 0
            && text[index + 1..]
                .chars()
                .take_while(|next| !next.is_ascii_whitespace() && !matches!(next, '<' | '>'))
                .all(|next| matches!(next, ',' | ';' | ')' | '}' | ']'));
        if closing_quote
            || (value_quote.is_none() && ch.is_ascii_whitespace())
            || matches!(ch, '&' | '#' | '<' | '>')
        {
            end = index;
            break;
        }
        if offset > quote_offset
            && value_quote.is_some_and(|quote| ch == char::from(quote))
            && escapes % (2 * (quote_offset + 1)) == quote_offset
        {
            value_quote = None;
        }
    }
    // Preserve a surrounding single quote, but never stop at an apostrophe
    // inside a value or just before a query delimiter. Unquoted URL values may
    // themselves end in apostrophes.
    if scheme_start > 0
        && text.as_bytes()[scheme_start - 1] == b'\''
        && end > start
        && text.as_bytes()[end - 1] == b'\''
        && !matches!(text.as_bytes().get(end), Some(b'&' | b'#'))
    {
        end -= 1;
    }
    Some(end)
}

fn redact_url_credentials(input: &str) -> String {
    let mut output = input.to_owned();
    let mut cursor = 0;
    while let Some(relative) = output[cursor..].find("://") {
        let scheme_end = cursor + relative;
        let authority_start = scheme_end + 3;
        let authority_end = output[authority_start..]
            .find(|ch: char| {
                ch.is_ascii_whitespace() || matches!(ch, '/' | '?' | '#' | '"' | '<' | '>')
            })
            .map_or(output.len(), |offset| authority_start + offset);
        if let Some(at) = output[authority_start..authority_end].rfind('@') {
            let userinfo_end = authority_start + at;
            output.replace_range(authority_start..userinfo_end, REDACTED);
            cursor = authority_start + REDACTED.len() + 1;
            continue;
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
    fn quoted_query_whitespace_never_exposes_secret_suffixes() {
        for (url, expected) in [
            (
                r#"https://blocked.example/?token="prefix private-token"&view=public"#,
                "https://blocked.example/?token=[REDACTED]&view=public",
            ),
            (
                r#"https://blocked.example/?token=" private-token"&view=public"#,
                "https://blocked.example/?token=[REDACTED]&view=public",
            ),
            (
                r#"https://blocked.example/?token=[REDACTED]"prefix private-token"&view=public"#,
                "https://blocked.example/?token=[REDACTED]&view=public",
            ),
            (
                "https://blocked.example/?token='prefix private-token'&view=public#section",
                "https://blocked.example/?token=[REDACTED]&view=public#section",
            ),
            (
                r#"https://blocked.example/?token="prefix ' private-token""#,
                "https://blocked.example/?token=[REDACTED]",
            ),
            (
                r#"https://blocked.example/?token="prefix\" private-token"&view=public"#,
                "https://blocked.example/?token=[REDACTED]&view=public",
            ),
            (
                "https://blocked.example/?token=\"prefix\t\nprivate-token\"&view=public",
                "https://blocked.example/?token=[REDACTED]&view=public",
            ),
            (
                r#"https://blocked.example/?next=https://public.example/path?view=public&token="prefix private-token"&mode=public#section"#,
                "https://blocked.example/?next=https://public.example/path?view=public&token=[REDACTED]&mode=public#section",
            ),
        ] {
            for (input, expected) in [
                (url.to_owned(), expected.to_owned()),
                (
                    format!("blocked Document {url} (2 requests)"),
                    format!("blocked Document {expected} (2 requests)"),
                ),
                (
                    format!("target {url:?} denied"),
                    format!("target {expected:?} denied"),
                ),
                (format!("'{url}'"), format!("'{expected}'")),
            ] {
                let mut output = input.clone();
                for pass in 1..=3 {
                    output = redact_str(&output);
                    assert_eq!(output, expected, "pass {pass}: {input}");
                }
            }
        }
        for input in [
            r#"https://blocked.example/?token="prefix private-token"#,
            r#"https://blocked.example/?token=\"prefix private-token"#,
        ] {
            let expected = "https://blocked.example/?token=[REDACTED]";
            assert_eq!(redact_str(input), expected);
            assert_eq!(redact_str(expected), expected);
        }
    }

    #[test]
    fn double_quotes_are_redacted_in_their_url_value_context() {
        for (input, expected) in [
            (
                r#"https://blocked.example/?token="private-token"&view=public"#,
                "https://blocked.example/?token=[REDACTED]&view=public",
            ),
            (
                r#"https://blocked.example/?token=prefix"private-token"#,
                "https://blocked.example/?token=[REDACTED]",
            ),
            (
                r#"target "https://blocked.example/?token=prefix\"private-token" denied"#,
                r#"target "https://blocked.example/?token=[REDACTED]" denied"#,
            ),
            (
                r#"target "https://blocked.example/?token=\"private-token\"&view=public#section" denied"#,
                r#"target "https://blocked.example/?token=[REDACTED]&view=public#section" denied"#,
            ),
            (
                r#"target "https://blocked.example/?token=prefix",private-token" denied"#,
                r#"target "https://blocked.example/?token=[REDACTED]" denied"#,
            ),
            (
                r#"target "https://blocked.example/?token=prefix\\" denied"#,
                r#"target "https://blocked.example/?token=[REDACTED]" denied"#,
            ),
            (
                r#""https://blocked.example/?token=private-token", reader's note"#,
                r#""https://blocked.example/?token=[REDACTED]", reader's note"#,
            ),
            (
                r#"https://blocked.example/reader"s?view="public"&token="private-token"&mode=public#section"#,
                r#"https://blocked.example/reader"s?view="public"&token=[REDACTED]&mode=public#section"#,
            ),
        ] {
            assert_eq!(redact_str(input), expected, "{input}");
            assert_eq!(redact_str(expected), expected, "idempotence: {input}");
        }
        for safe in [
            r##"reader's "public" note: https://example.com/reader"s?view="public"#section"##,
            r#""https://example.com/?view=public" password=prefix#private-secret"#,
        ] {
            let expected = safe.replace("prefix#private-secret", REDACTED);
            assert_eq!(redact_str(safe), expected);
        }
    }

    #[test]
    fn nested_url_queries_preserve_public_data_and_are_idempotent() {
        for (input, expected) in [
            (
                "https://blocked.example/?next=https://public.example/path&token=private-token&view=public#section",
                "https://blocked.example/?next=https://public.example/path&token=[REDACTED]&view=public#section",
            ),
            (
                r#"target "https://blocked.example/?next=https://public.example/path?view=public&token=private-token&mode=public#section" denied"#,
                r#"target "https://blocked.example/?next=https://public.example/path?view=public&token=[REDACTED]&mode=public#section" denied"#,
            ),
        ] {
            assert_eq!(redact_str(input), expected, "{input}");
            assert_eq!(redact_str(expected), expected, "idempotence: {input}");
        }
    }

    #[test]
    fn apostrophes_and_hashes_are_redacted_in_their_value_context() {
        for (input, expected) in [
            (
                "https://user:p'private-password@blocked.example/path?view=public",
                "https://[REDACTED]@blocked.example/path?view=public",
            ),
            (
                "https://blocked.example/path?token=prefix'private-token&view=public#section",
                "https://blocked.example/path?token=[REDACTED]&view=public#section",
            ),
            (
                "'https://blocked.example/path?token=prefix'private-token'",
                "'https://blocked.example/path?token=[REDACTED]'",
            ),
            (
                "https://blocked.example/path?token='private-token'&view=public",
                "https://blocked.example/path?token=[REDACTED]&view=public",
            ),
            ("password=prefix#private-secret", "password=[REDACTED]"),
            ("password=prefix'private-secret", "password=[REDACTED]"),
            ("password=prefix&private-secret", "password=[REDACTED]"),
            ("password=[REDACTED]#private-secret", "password=[REDACTED]"),
            (
                "password='prefix#private-secret' reader's note",
                "password=[REDACTED] reader's note",
            ),
        ] {
            assert_eq!(redact_str(input), expected, "{input}");
            assert_eq!(redact_str(expected), expected, "idempotence: {input}");
        }
        let safe = "reader's note: https://example.com/reader's?view=reader's#section";
        assert_eq!(redact_str(safe), safe);
    }

    #[test]
    fn url_warning_redaction_preserves_public_data_and_is_idempotent() {
        for input in [
            "https://private-user:private-password@blocked.example?token=private-token&view=public",
            "https://private-user@blocked.example?token=private-token&view=public",
            "https://blocked.example?token=[REDACTED]private-token&view=public",
        ] {
            let output = Redactor.redact_str(input);
            for secret in ["private-user", "private-password", "private-token"] {
                assert!(!output.contains(secret), "{output}");
            }
            assert!(
                output.contains("blocked.example?token=[REDACTED]&view=public"),
                "{output}"
            );
            assert_eq!(Redactor.redact_str(&output), output);
        }
        let safe = "blocked Document https://blocked.example/path?view=public (2 requests)";
        assert_eq!(Redactor.redact_str(safe), safe);
    }

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
