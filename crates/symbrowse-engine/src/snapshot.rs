use crate::AxNode;
use crate::refs::{normalize_ref_part, ref_key};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::cmp::Ordering;
use std::collections::{BTreeMap, BTreeSet, HashMap};

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct SnapshotOptions {
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub interactive: bool,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub compact: bool,
    #[serde(default, skip_serializing_if = "is_zero_i32")]
    pub depth: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub selector: String,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub urls: bool,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub diff: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub since: String,
    #[serde(skip)]
    pub root_node_id: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct SnapshotResult {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub snapshot_id: String,
    pub tree: String,
    pub refs: BTreeMap<String, SnapshotRef>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub hint: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct SnapshotRef {
    pub node_id: String,
    #[serde(default, skip_serializing_if = "is_zero_i64")]
    pub backend_node_id: i64,
    pub role: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub value: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub state: String,
    pub visible: bool,
    pub interactive: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub url: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub refkey: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub dom_path: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub sibling_ordinal: usize,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub attributes: BTreeMap<String, String>,
}

#[derive(Clone, Debug)]
struct SnapshotNode {
    id: String,
    parent_id: String,
    role: String,
    name: String,
    value: String,
    url: String,
    ignored: bool,
    interactive: bool,
    iframe: bool,
    shadow_root: bool,
    backend_node_id: i64,
    state: BTreeMap<String, String>,
    attributes: BTreeMap<String, String>,
    child_ids: Vec<String>,
    children: Vec<String>,
    visible: bool,
    dom_path: String,
    sibling_ordinal: usize,
}

/// Render raw AX payloads into deterministic text and temporary refs. Pass the
/// result through [`crate::refs::StableRefRegistry::apply`] for session refs.
pub fn render_snapshot(
    nodes: &[AxNode],
    options: &SnapshotOptions,
) -> Result<SnapshotResult, String> {
    let mut parsed = BTreeMap::new();
    let mut order = Vec::with_capacity(nodes.len());
    for (index, raw) in nodes.iter().enumerate() {
        let node = decode_node(raw, index)?;
        if parsed.insert(node.id.clone(), node.clone()).is_some() {
            return Err(format!("duplicate accessibility node id {:?}", node.id));
        }
        order.push(node.id);
    }
    if parsed.is_empty() {
        return Ok(SnapshotResult::default());
    }

    let mut children: HashMap<String, Vec<String>> = HashMap::new();
    let mut child_of = BTreeSet::new();
    for id in &order {
        let node = &parsed[id];
        for child_id in &node.child_ids {
            if parsed.contains_key(child_id) && child_id != id {
                children
                    .entry(id.clone())
                    .or_default()
                    .push(child_id.clone());
                child_of.insert(child_id.clone());
            }
        }
    }
    for id in &order {
        let node = &parsed[id];
        if !node.parent_id.is_empty()
            && node.parent_id != *id
            && parsed.contains_key(&node.parent_id)
            && !children
                .get(&node.parent_id)
                .is_some_and(|items| items.iter().any(|item| item == id))
        {
            children
                .entry(node.parent_id.clone())
                .or_default()
                .push(id.clone());
            child_of.insert(id.clone());
        }
    }
    for (id, node) in &mut parsed {
        node.children = children.remove(id).unwrap_or_default();
    }

    let mut roots = if !options.root_node_id.is_empty() {
        if !parsed.contains_key(&options.root_node_id) {
            return Err(format!(
                "snapshot selector resolved to unknown accessibility node {:?}",
                options.root_node_id
            ));
        }
        vec![options.root_node_id.clone()]
    } else {
        order
            .iter()
            .filter(|id| !child_of.contains(*id))
            .cloned()
            .collect::<Vec<_>>()
    };
    if roots.is_empty() {
        roots = parsed.keys().cloned().collect();
    }
    roots.sort_by(|left, right| snapshot_id_cmp(left, right));
    assign_paths(&mut parsed, &roots);

    let mut refs = BTreeMap::new();
    let mut lines = Vec::new();
    let mut visited = BTreeSet::new();
    let mut ref_number = 0usize;
    for root in roots {
        render_node(
            &parsed,
            &root,
            options,
            0,
            &mut visited,
            &mut ref_number,
            &mut refs,
            &mut lines,
        );
    }
    Ok(SnapshotResult {
        snapshot_id: String::new(),
        tree: lines.join("\n"),
        refs,
        hint: String::new(),
    })
}

fn decode_node(raw: &Value, index: usize) -> Result<SnapshotNode, String> {
    let object = raw
        .as_object()
        .ok_or_else(|| format!("accessibility node {index} is not an object"))?;
    let id = raw_string_id(object.get("nodeId")).unwrap_or_else(|| format!("index-{index}"));
    let raw_role = raw_value(object.get("role"));
    let role = normalize_role(&raw_role);
    let ignored = object
        .get("ignored")
        .and_then(Value::as_bool)
        .unwrap_or(false);
    let mut node = SnapshotNode {
        id,
        parent_id: raw_string_id(object.get("parentId")).unwrap_or_default(),
        role: role.clone(),
        name: raw_value(object.get("name")),
        value: raw_value(object.get("value")),
        url: String::new(),
        ignored,
        interactive: false,
        iframe: object
            .get("isFrameOwner")
            .and_then(Value::as_bool)
            .unwrap_or(false)
            || role.contains("iframe")
            || role == "frame"
            || raw_role.to_ascii_lowercase().contains("frame"),
        shadow_root: object
            .get("isShadowRoot")
            .and_then(Value::as_bool)
            .unwrap_or(false)
            || object
                .get("shadowRoot")
                .and_then(Value::as_bool)
                .unwrap_or(false)
            || object
                .get("shadowBoundary")
                .and_then(Value::as_bool)
                .unwrap_or(false)
            || role.contains("shadow"),
        backend_node_id: object
            .get("backendDOMNodeId")
            .and_then(raw_i64)
            .unwrap_or_default(),
        state: BTreeMap::new(),
        attributes: BTreeMap::new(),
        child_ids: object
            .get("childIds")
            .and_then(Value::as_array)
            .map(|items| {
                items
                    .iter()
                    .filter_map(|item| raw_string_id(Some(item)))
                    .collect()
            })
            .unwrap_or_default(),
        children: Vec::new(),
        visible: object
            .get("visible")
            .and_then(Value::as_bool)
            .unwrap_or(!ignored),
        dom_path: String::new(),
        sibling_ordinal: 0,
    };
    if object.get("frameId").and_then(Value::as_str).is_some()
        && (role == "iframe" || role == "frame")
    {
        node.iframe = true;
    }
    if let Some(attributes) = object.get("attributes").and_then(Value::as_object) {
        for (key, value) in attributes {
            node.attributes
                .insert(key.trim().to_ascii_lowercase(), raw_value(Some(value)));
        }
    }
    for key in ["alt", "title", "placeholder", "testid"] {
        if let Some(value) = object
            .get(key)
            .and_then(Value::as_str)
            .filter(|v| !v.is_empty())
        {
            node.attributes.insert(key.to_owned(), value.to_owned());
        }
    }
    if let Some(properties) = object.get("properties").and_then(Value::as_array) {
        for property in properties {
            let Some(property) = property.as_object() else {
                continue;
            };
            let name = property
                .get("name")
                .and_then(Value::as_str)
                .unwrap_or_default()
                .trim()
                .to_ascii_lowercase();
            let value = raw_value(property.get("value"));
            match name.as_str() {
                "url" | "href" => {
                    if node.url.is_empty() {
                        node.url = value;
                    }
                }
                "focusable" | "editable" | "checked" | "expanded" | "selected" | "disabled"
                | "pressed" => {
                    if (name != "focusable" && name != "editable")
                        || raw_bool(property.get("value"))
                    {
                        node.state.insert(name.clone(), value);
                    }
                    if raw_bool(property.get("value")) && name != "expanded" {
                        node.interactive = true;
                    }
                }
                "type" | "autocomplete" if !value.is_empty() => {
                    node.attributes.insert(name, value);
                }
                _ => {}
            }
        }
    }
    if node.role == "link" && node.url.is_empty() {
        node.url = object
            .get("url")
            .or_else(|| object.get("href"))
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
    }
    node.interactive |= interactive_role(&node.role);
    Ok(node)
}

#[allow(clippy::too_many_arguments)]
fn render_node(
    parsed: &BTreeMap<String, SnapshotNode>,
    id: &str,
    options: &SnapshotOptions,
    depth: usize,
    visited: &mut BTreeSet<String>,
    ref_number: &mut usize,
    refs: &mut BTreeMap<String, SnapshotRef>,
    lines: &mut Vec<String>,
) {
    let Some(node) = parsed.get(id) else { return };
    if visited.contains(id) || (options.depth > 0 && depth as i32 > options.depth) {
        return;
    }
    visited.insert(id.to_owned());
    let include = !node.ignored
        && (!options.interactive || node.interactive || node.iframe || node.shadow_root)
        && (!options.compact || node.interactive || node.iframe || node.shadow_root);
    let mut children_depth = depth;
    if include {
        *ref_number += 1;
        let ref_name = format!("e{ref_number}");
        refs.insert(
            ref_name.clone(),
            SnapshotRef {
                node_id: node.id.clone(),
                backend_node_id: node.backend_node_id,
                role: node.role.clone(),
                name: node.name.clone(),
                value: node.value.clone(),
                state: snapshot_state(&node.state),
                visible: node.visible,
                interactive: node.interactive,
                url: node.url.clone(),
                refkey: ref_key(
                    &node.role,
                    &node.name,
                    &node.dom_path,
                    node.sibling_ordinal as i32,
                ),
                dom_path: node.dom_path.clone(),
                sibling_ordinal: node.sibling_ordinal,
                attributes: node.attributes.clone(),
            },
        );
        let mut line = format!("{}- {}", "  ".repeat(depth), node.role);
        if !node.name.is_empty() {
            line.push_str(&format!(" \"{}\"", node.name.replace('"', "\\\"")));
        } else if !node.value.is_empty() {
            line.push_str(&format!(" = \"{}\"", node.value.replace('"', "\\\"")));
        }
        if node.iframe {
            line.push_str(" [iframe]");
        }
        if node.shadow_root {
            line.push_str(" [shadow-root]");
        }
        if options.urls && !node.url.is_empty() {
            line.push_str(&format!(" ({})", node.url));
        }
        line.push_str(&format!(" [ref={ref_name}]"));
        lines.push(line);
        children_depth += 1;
    }
    for child in &node.children {
        render_node(
            parsed,
            child,
            options,
            children_depth,
            visited,
            ref_number,
            refs,
            lines,
        );
    }
}

fn assign_paths(parsed: &mut BTreeMap<String, SnapshotNode>, roots: &[String]) {
    for (ordinal, root_id) in roots.iter().enumerate() {
        if let Some(node) = parsed.get_mut(root_id) {
            node.sibling_ordinal = ordinal;
            let path = format!("/{}", path_segment(node));
            assign_path(parsed, root_id, path);
        }
    }
}

fn assign_path(parsed: &mut BTreeMap<String, SnapshotNode>, id: &str, path: String) {
    let Some(node) = parsed.get_mut(id) else {
        return;
    };
    node.dom_path = path.clone();
    let mut children = node.children.clone();
    children.sort_by(|left, right| {
        let left_key = parsed
            .get(left)
            .map(|child| {
                format!(
                    "{}{{{}}}",
                    path_segment(child),
                    subtree_signature(parsed, left, &mut BTreeSet::new())
                )
            })
            .unwrap_or_else(|| left.clone());
        let right_key = parsed
            .get(right)
            .map(|child| {
                format!(
                    "{}{{{}}}",
                    path_segment(child),
                    subtree_signature(parsed, right, &mut BTreeSet::new())
                )
            })
            .unwrap_or_else(|| right.clone());
        left_key.cmp(&right_key).then_with(|| left.cmp(right))
    });
    for (ordinal, child_id) in children.iter().enumerate() {
        if let Some(child) = parsed.get_mut(child_id) {
            child.sibling_ordinal = ordinal;
            let child_path = format!("{path}/{}", path_segment(child));
            assign_path(parsed, child_id, child_path);
        }
    }
}

fn subtree_signature(
    parsed: &BTreeMap<String, SnapshotNode>,
    id: &str,
    visiting: &mut BTreeSet<String>,
) -> String {
    if !visiting.insert(id.to_owned()) {
        return "cycle".to_owned();
    }
    let Some(node) = parsed.get(id) else {
        return "missing".to_owned();
    };
    let mut children: Vec<String> = node
        .children
        .iter()
        .map(|child| subtree_signature(parsed, child, visiting))
        .collect();
    children.sort();
    visiting.remove(id);
    format!("{}({})", path_segment(node), children.join(","))
}

fn path_segment(node: &SnapshotNode) -> String {
    let mut segment = normalize_ref_part(&node.role);
    if !node.name.is_empty() {
        segment.push('[');
        segment.push_str(&normalize_ref_part(&node.name));
        segment.push(']');
    }
    segment
}

fn raw_value(value: Option<&Value>) -> String {
    let Some(value) = value else {
        return String::new();
    };
    if value.is_null() {
        return String::new();
    }
    if let Some(inner) = value.as_object().and_then(|object| object.get("value")) {
        return raw_scalar(inner);
    }
    raw_scalar(value)
}

fn raw_scalar(value: &Value) -> String {
    match value {
        Value::Null => String::new(),
        Value::String(value) => value.clone(),
        Value::Bool(value) => value.to_string(),
        Value::Number(value) => value.to_string(),
        Value::Array(_) | Value::Object(_) => value.to_string(),
    }
}

fn raw_string_id(value: Option<&Value>) -> Option<String> {
    let value = raw_value(value);
    (!value.is_empty()).then_some(value)
}

fn raw_i64(value: &Value) -> Option<i64> {
    raw_value(Some(value)).parse().ok()
}

fn raw_bool(value: Option<&Value>) -> bool {
    raw_value(value).eq_ignore_ascii_case("true")
}

fn normalize_role(role: &str) -> String {
    let role = role.trim();
    if role.eq_ignore_ascii_case("rootwebarea") || role.eq_ignore_ascii_case("webarea") {
        "document".to_owned()
    } else if role.is_empty() {
        "generic".to_owned()
    } else {
        role.to_ascii_lowercase()
    }
}

fn interactive_role(role: &str) -> bool {
    matches!(
        role,
        "button"
            | "checkbox"
            | "combobox"
            | "link"
            | "listbox"
            | "menuitem"
            | "option"
            | "radio"
            | "searchbox"
            | "slider"
            | "spinbutton"
            | "switch"
            | "tab"
            | "textbox"
            | "treeitem"
            | "gridcell"
    )
}

fn snapshot_state(state: &BTreeMap<String, String>) -> String {
    state
        .iter()
        .map(|(key, value)| format!("{key}={value}"))
        .collect::<Vec<_>>()
        .join(",")
}

fn snapshot_id_cmp(left: &str, right: &str) -> Ordering {
    match (left.parse::<i64>(), right.parse::<i64>()) {
        (Ok(left), Ok(right)) if left != right => left.cmp(&right),
        _ => left.cmp(right),
    }
}

fn is_zero(value: &usize) -> bool {
    *value == 0
}
fn is_zero_i32(value: &i32) -> bool {
    *value == 0
}
fn is_zero_i64(value: &i64) -> bool {
    *value == 0
}
