#![deny(unsafe_code)]

use serde_json::Value;

mod generated {
    include!("generated_registry.rs");
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct ToolSpec {
    pub name: &'static str,
    pub canonical: &'static str,
    pub command: &'static str,
    pub profile: &'static str,
}

const SPECS: &[ToolSpec] = &[
    ToolSpec {
        name: "open",
        canonical: "open",
        command: "open",
        profile: "core",
    },
    ToolSpec {
        name: "goto",
        canonical: "open",
        command: "open",
        profile: "core",
    },
    ToolSpec {
        name: "snapshot",
        canonical: "snapshot",
        command: "snapshot",
        profile: "core",
    },
    ToolSpec {
        name: "click",
        canonical: "click",
        command: "click",
        profile: "core",
    },
    ToolSpec {
        name: "fill",
        canonical: "fill",
        command: "fill",
        profile: "core",
    },
    ToolSpec {
        name: "type",
        canonical: "type",
        command: "type",
        profile: "core",
    },
    ToolSpec {
        name: "press",
        canonical: "press",
        command: "press",
        profile: "core",
    },
    ToolSpec {
        name: "wait",
        canonical: "wait",
        command: "wait",
        profile: "core",
    },
    ToolSpec {
        name: "read",
        canonical: "read",
        command: "read",
        profile: "core",
    },
    ToolSpec {
        name: "get",
        canonical: "get",
        command: "get.text",
        profile: "core",
    },
    ToolSpec {
        name: "find",
        canonical: "find",
        command: "find",
        profile: "core",
    },
    ToolSpec {
        name: "back",
        canonical: "back",
        command: "back",
        profile: "nav",
    },
    ToolSpec {
        name: "forward",
        canonical: "forward",
        command: "forward",
        profile: "nav",
    },
    ToolSpec {
        name: "reload",
        canonical: "reload",
        command: "reload",
        profile: "nav",
    },
    ToolSpec {
        name: "fetch_url",
        canonical: "fetch_url",
        command: "fetch.url",
        profile: "core",
    },
    ToolSpec {
        name: "fetch_batch",
        canonical: "fetch_batch",
        command: "fetch.batch",
        profile: "core",
    },
    ToolSpec {
        name: "cache_get",
        canonical: "cache_get",
        command: "cache.get",
        profile: "core",
    },
    ToolSpec {
        name: "wayback_snapshots",
        canonical: "wayback_snapshots",
        command: "wayback.snapshots",
        profile: "core",
    },
];

const PROFILES: &[&str] = &["core", "nav", "state", "network", "debug", "flows"];

#[must_use]
pub fn all_tools_json() -> &'static str {
    generated::ALL_TOOLS_JSON
}

#[must_use]
pub fn specs() -> &'static [ToolSpec] {
    SPECS
}

#[must_use]
pub fn lookup(name: &str) -> Option<&'static ToolSpec> {
    SPECS.iter().find(|spec| spec.name == name)
}

pub fn canonical(name: &str) -> Option<&'static ToolSpec> {
    lookup(name).or_else(|| SPECS.iter().find(|spec| spec.canonical == name))
}

pub fn validate_profile_selection(selection: &str) -> Result<Vec<&str>, String> {
    let selection = if selection.trim().is_empty() {
        "core"
    } else {
        selection
    };
    let mut selected = Vec::new();
    for raw in selection
        .split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
    {
        if raw != "all" && !PROFILES.contains(&raw) {
            return Err(format!("unknown tool profile {raw:?}"));
        }
        if raw == "all" {
            selected.extend(PROFILES.iter().copied());
        } else {
            selected.push(raw);
        }
    }
    if selected.is_empty() {
        selected.push("core");
    }
    Ok(selected)
}

#[allow(clippy::collapsible_if)]
fn object_slices() -> Vec<&'static str> {
    let source = generated::ALL_TOOLS_JSON;
    let bytes = source.as_bytes();
    let mut result = Vec::new();
    let mut start = None;
    let mut depth = 0usize;
    let mut string = false;
    let mut escaped = false;
    for (index, byte) in bytes.iter().copied().enumerate() {
        if string {
            if escaped {
                escaped = false;
            } else if byte == b'\\' {
                escaped = true;
            } else if byte == b'"' {
                string = false;
            }
            continue;
        }
        match byte {
            b'"' => string = true,
            b'{' => {
                if depth == 0 {
                    start = Some(index);
                }
                depth += 1;
            }
            b'}' => {
                depth = depth.saturating_sub(1);
                if depth == 0 {
                    if let Some(begin) = start.take() {
                        result.push(&source[begin..=index]);
                    }
                }
            }
            _ => {}
        }
    }
    result
}

fn tool_name(raw: &str) -> Result<String, String> {
    serde_json::from_str::<Value>(raw)
        .map_err(|error| error.to_string())?
        .get("name")
        .and_then(Value::as_str)
        .map(str::to_owned)
        .ok_or_else(|| "generated MCP tool lacks a name".to_owned())
}

fn selected(name: &str, profiles: &[&str]) -> bool {
    let canonical = lookup(name).map_or(name, |spec| spec.canonical);
    SPECS
        .iter()
        .any(|spec| spec.canonical == canonical && profiles.contains(&spec.profile))
}

fn with_session(raw: &str, session: &str) -> String {
    let old = serde_json::to_string("session name (default: default)").expect("fixed string");
    let replacement = serde_json::to_string(&format!("session name (default: {session})"))
        .expect("session string");
    raw.replace(&old, &replacement)
}

pub fn tools_list(profile_selection: &str, session: &str, id: &Value) -> Result<String, String> {
    let profiles = validate_profile_selection(profile_selection)?;
    let mut objects = Vec::new();
    for raw in object_slices() {
        let name = tool_name(raw)?;
        if selected(&name, &profiles) {
            objects.push(with_session(raw, session));
        }
    }
    let id = serde_json::to_string(id).map_err(|error| error.to_string())?;
    Ok(format!(
        "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":{{\"tools\":[{}]}}}}",
        objects.join(",")
    ))
}

pub fn validate_arguments(spec: &ToolSpec, args: &Value) -> Result<(), String> {
    let object = args.as_object().ok_or_else(|| format!("invalid arguments for {}: json: cannot unmarshal {} into Go value of type map[string]interface {{}}", spec.name, json_type(args)))?;
    for key in required(spec.name) {
        if object
            .get(*key)
            .and_then(Value::as_str)
            .is_none_or(str::is_empty)
        {
            return Err(format!("missing required argument {key:?}"));
        }
    }
    if spec.name == "fetch_batch"
        && object
            .get("urls")
            .and_then(Value::as_array)
            .is_none_or(Vec::is_empty)
    {
        return Err("missing required argument \"urls\"".to_owned());
    }
    if spec.name == "fetch_batch"
        && object
            .get("urls")
            .and_then(Value::as_array)
            .is_some_and(|urls| urls.iter().any(|url| !url.is_string()))
    {
        return Err("urls entries must be strings".to_owned());
    }
    if spec.name == "get" {
        let kind = object
            .get("kind")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if !matches!(
            kind,
            "text"
                | "html"
                | "value"
                | "attr"
                | "title"
                | "url"
                | "count"
                | "box"
                | "styles"
                | "visible"
                | "enabled"
                | "checked"
        ) {
            return Err(format!(
                "invalid get kind {kind:?}: expected text, html, value, attr, title, url, count, box, styles, visible, enabled, or checked"
            ));
        }
    }
    Ok(())
}

fn required(name: &str) -> &'static [&'static str] {
    match name {
        "open" => &["url"],
        "goto" => &["url"],
        "click" => &["selector"],
        "fill" => &["selector", "value"],
        "type" => &["value"],
        "press" => &["key"],
        "wait" => &["kind"],
        "get" => &["kind"],
        "find" => &["kind", "query"],
        "fetch_url" | "wayback_snapshots" => &["url"],
        "cache_get" => &["cache_id"],
        _ => &[],
    }
}

fn json_type(value: &Value) -> &'static str {
    match value {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generated_registry_has_the_pinned_surface() {
        assert_eq!(object_slices().len(), 18);
        assert_eq!(
            serde_json::from_str::<Value>(
                &tools_list("core", "default", &Value::from(2)).expect("core list")
            )
            .expect("core response")
            .get("result")
            .and_then(|result| result.get("tools"))
            .and_then(Value::as_array)
            .map_or(0, Vec::len),
            15
        );
        assert_eq!(
            serde_json::from_str::<Value>(
                &tools_list("nav", "default", &Value::from(2)).expect("nav list")
            )
            .expect("nav response")
            .get("result")
            .and_then(|result| result.get("tools"))
            .and_then(Value::as_array)
            .map_or(0, Vec::len),
            3
        );
        assert_eq!(
            serde_json::from_str::<Value>(
                &tools_list("all", "default", &Value::from(2)).expect("all list")
            )
            .expect("all response")
            .get("result")
            .and_then(|result| result.get("tools"))
            .and_then(Value::as_array)
            .map_or(0, Vec::len),
            18
        );
    }

    #[test]
    fn aliases_resolve_without_changing_profile_membership() {
        assert_eq!(canonical("goto").expect("alias").canonical, "open");
        assert!(
            !tools_list("core", "default", &Value::from(2))
                .expect("core list")
                .contains("\"name\":\"back\"")
        );
    }

    #[test]
    fn fetch_batch_rejects_non_string_urls_like_go() {
        let spec = lookup("fetch_batch").unwrap();
        assert_eq!(
            validate_arguments(spec, &serde_json::json!({"urls":[1]})),
            Err("urls entries must be strings".to_owned())
        );
        assert!(validate_arguments(spec, &serde_json::json!({"urls":[""]})).is_ok());
    }
}
