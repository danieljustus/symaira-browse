//! Browser-grade HTML parsing and deterministic DOM cleanup.
use std::collections::{BTreeMap, HashSet};
use std::io::Cursor;

use html5ever::{parse_document, tendril::TendrilSink};
use markup5ever_rcdom::{Handle, NodeData, RcDom};

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Node {
    Document {
        children: Vec<Node>,
    },
    Element {
        tag: String,
        attrs: BTreeMap<String, String>,
        attr_order: Vec<String>,
        children: Vec<Node>,
    },
    Text(String),
    Doctype(String),
    Comment,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DataIsland {
    pub source: String,
    pub json: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Tree {
    pub root: Node,
    pub title: String,
    pub lang: String,
    pub islands: Vec<DataIsland>,
}

#[derive(Debug)]
pub enum ParseError {
    Html(std::io::Error),
}
impl std::fmt::Display for ParseError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Html(error) => write!(f, "parse HTML: {error}"),
        }
    }
}
impl std::error::Error for ParseError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Html(error) => Some(error),
        }
    }
}

pub fn parse(body: &[u8]) -> Result<Tree, ParseError> {
    let dom = parse_document(RcDom::default(), Default::default())
        .from_utf8()
        .read_from(&mut Cursor::new(body))
        .map_err(ParseError::Html)?;
    let root = convert(&dom.document);
    let title = find_title(&root).unwrap_or_default();
    let lang = find_lang(&root).unwrap_or_default();
    let islands = collect_islands(&root);
    Ok(Tree {
        root,
        title,
        lang,
        islands,
    })
}

fn convert(handle: &Handle) -> Node {
    let children = handle.children.borrow().iter().map(convert).collect();
    match &handle.data {
        NodeData::Document => Node::Document { children },
        NodeData::Text { contents } => Node::Text(contents.borrow().to_string()),
        NodeData::Doctype { name, .. } => Node::Doctype(name.to_string()),
        NodeData::Comment { .. } => Node::Comment,
        NodeData::Element { name, attrs, .. } => Node::Element {
            tag: name.local.to_string().to_ascii_lowercase(),
            attrs: attrs
                .borrow()
                .iter()
                .map(|a| {
                    (
                        a.name.local.to_string().to_ascii_lowercase(),
                        a.value.to_string(),
                    )
                })
                .collect(),
            attr_order: attrs
                .borrow()
                .iter()
                .map(|a| a.name.local.to_string().to_ascii_lowercase())
                .collect(),
            children,
        },
        _ => Node::Document { children },
    }
}

pub fn cleanup(root: &mut Node) {
    cleanup_children(root);
}
fn cleanup_children(node: &mut Node) {
    if let Node::Element {
        children,
        attrs,
        attr_order,
        ..
    } = node
    {
        *children = cleaned_children(std::mem::take(children));
        attrs.retain(|key, _| SEMANTIC_ATTRS.iter().any(|allowed| *allowed == key));
        attr_order.retain(|key| attrs.contains_key(key));
    } else if let Node::Document { children } = node {
        *children = cleaned_children(std::mem::take(children));
    }
}
fn cleaned_children(children: Vec<Node>) -> Vec<Node> {
    let mut kept = Vec::with_capacity(children.len());
    for mut child in children {
        if !should_drop(&child) {
            cleanup_children(&mut child);
            kept.push(child);
        }
    }
    kept
}
fn should_drop(node: &Node) -> bool {
    let Node::Element { tag, attrs, .. } = node else {
        return matches!(node, Node::Comment);
    };
    if DROP_TAGS.iter().any(|candidate| *candidate == tag) {
        return true;
    }
    if attrs.contains_key("hidden")
        || attrs
            .get("aria-hidden")
            .is_some_and(|v| v.eq_ignore_ascii_case("true"))
    {
        return true;
    }
    let style = attrs
        .get("style")
        .map(|v| v.to_ascii_lowercase())
        .unwrap_or_default();
    if style.contains("display:none")
        || style.contains("display: none")
        || style.contains("visibility:hidden")
        || style.contains("visibility: hidden")
    {
        return true;
    }
    attrs.get("class").is_some_and(|classes| {
        classes
            .split_whitespace()
            .any(|class| HIDDEN_CLASSES.contains(&class))
    })
}
const DROP_TAGS: &[&str] = &[
    "script", "style", "noscript", "svg", "iframe", "object", "embed", "canvas", "audio", "video",
    "template", "picture",
];
const HIDDEN_CLASSES: &[&str] = &[
    "hidden",
    "sr-only",
    "visually-hidden",
    "invisible",
    "d-none",
    "hide",
];
const SEMANTIC_ATTRS: &[&str] = &[
    "href",
    "src",
    "alt",
    "title",
    "aria-label",
    "aria-describedby",
    "placeholder",
    "name",
    "type",
    "value",
    "checked",
    "selected",
    "disabled",
    "readonly",
    "for",
    "id",
    "role",
    "action",
    "method",
    "enctype",
];

fn find_title(node: &Node) -> Option<String> {
    if let Node::Element { tag, children, .. } = node
        && tag == "title"
    {
        let text = text_content(children).trim().to_owned();
        if !text.is_empty() {
            return Some(text);
        }
    }
    children(node).iter().find_map(find_title)
}
fn find_lang(node: &Node) -> Option<String> {
    if let Node::Element { tag, attrs, .. } = node
        && tag == "html"
        && let Some(lang) = attrs.get("lang")
    {
        return Some(lang.clone());
    }
    children(node).iter().find_map(find_lang)
}
fn collect_islands(node: &Node) -> Vec<DataIsland> {
    let mut out = Vec::new();
    collect_islands_into(node, &mut out);
    out
}
fn collect_islands_into(node: &Node, out: &mut Vec<DataIsland>) {
    if let Node::Element {
        tag,
        attrs,
        children,
        ..
    } = node
        && tag == "script"
    {
        let source = attrs.get("type").map(String::as_str).unwrap_or("");
        let id = attrs.get("id").map(String::as_str).unwrap_or("");
        let lower_id = id.to_ascii_lowercase();
        if source.eq_ignore_ascii_case("application/ld+json")
            || lower_id == "__next_data__"
            || lower_id.contains("preloaded")
            || lower_id.contains("initial-state")
        {
            let json = text_content(children).trim().to_owned();
            if !json.is_empty() && serde_json::from_str::<serde_json::Value>(&json).is_ok() {
                out.push(DataIsland {
                    source: if source.eq_ignore_ascii_case("application/ld+json") {
                        "ld+json".into()
                    } else if !id.is_empty() {
                        id.into()
                    } else {
                        "script".into()
                    },
                    json,
                });
            }
        }
    }
    for child in children(node) {
        collect_islands_into(child, out);
    }
}
fn children(node: &Node) -> &[Node] {
    match node {
        Node::Document { children } | Node::Element { children, .. } => children,
        _ => &[],
    }
}
pub fn text_content(node: &[Node]) -> String {
    let mut out = String::new();
    for child in node {
        match child {
            Node::Text(text) => {
                out.push_str(text);
                out.push(' ');
            }
            Node::Document { children } | Node::Element { children, .. } => {
                out.push_str(&text_content(children))
            }
            Node::Doctype(_) | Node::Comment => {}
        }
    }
    out
}
pub fn attr<'a>(node: &'a Node, name: &str) -> Option<&'a str> {
    match node {
        Node::Element { attrs, .. } => attrs.get(&name.to_ascii_lowercase()).map(String::as_str),
        _ => None,
    }
}

/// A selector error returned by the bounded static selector implementation.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SelectorError(pub String);
impl std::fmt::Display for SelectorError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "selector {:?} matched no elements", self.0)
    }
}
impl std::error::Error for SelectorError {}

/// Select elements using the bounded CSS subset used by the Go pipeline.
///
/// The Go implementation delegates selectors to goquery.  This parser keeps
/// the same useful, deterministic subset without accepting arbitrary CSS: tag,
/// id, one or more classes, attribute operators, comma groups, descendants and
/// the child (`>`) combinator.  A selector is capped at 64 components so an
/// untrusted selector cannot turn into an unbounded tree walk.
pub fn select(root: &Node, selector: &str) -> Result<Vec<Node>, SelectorError> {
    let groups = parse_selector_groups(selector)?;
    let mut matches = Vec::new();
    for group in groups {
        collect_selected(root, &group, 0, true, &mut matches);
    }
    // CSS querySelectorAll returns document order and never returns an element
    // twice when it matches two comma-separated groups.
    let mut seen = HashSet::new();
    let unique = matches
        .into_iter()
        .filter_map(|(identity, node)| seen.insert(identity).then_some(node))
        .collect::<Vec<_>>();
    if unique.is_empty() {
        Err(SelectorError(selector.to_owned()))
    } else {
        Ok(unique)
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum Combinator {
    Descendant,
    Child,
}

fn parse_selector_groups(selector: &str) -> Result<Vec<Vec<(Combinator, String)>>, SelectorError> {
    let mut groups = Vec::new();
    for group in selector.split(',') {
        let mut tokens = Vec::new();
        let mut current = String::new();
        let mut pending = Combinator::Descendant;
        let mut chars = group.trim().chars().peekable();
        while let Some(ch) = chars.next() {
            if ch == '>' {
                if !current.trim().is_empty() {
                    tokens.push((pending, current.trim().to_owned()));
                    current.clear();
                }
                pending = Combinator::Child;
                continue;
            }
            if ch.is_whitespace() {
                if !current.trim().is_empty() {
                    tokens.push((pending, current.trim().to_owned()));
                    current.clear();
                    pending = Combinator::Descendant;
                }
                while chars.peek().is_some_and(|next| next.is_whitespace()) {
                    chars.next();
                }
                if chars.peek() == Some(&'>') {
                    chars.next();
                    pending = Combinator::Child;
                }
                continue;
            }
            current.push(ch);
        }
        if !current.trim().is_empty() {
            tokens.push((pending, current.trim().to_owned()));
        }
        if tokens.is_empty()
            || tokens.len() > 64
            || tokens.iter().any(|(_, value)| value.is_empty())
        {
            return Err(SelectorError(selector.to_owned()));
        }
        groups.push(tokens);
    }
    if groups.is_empty() || groups.len() > 32 {
        return Err(SelectorError(selector.to_owned()));
    }
    Ok(groups)
}

fn collect_selected(
    node: &Node,
    parts: &[(Combinator, String)],
    index: usize,
    _is_root: bool,
    out: &mut Vec<(*const Node, Node)>,
) {
    if matches!(node, Node::Element { .. }) && selector_matches(node, &parts[index].1) {
        if index + 1 == parts.len() {
            out.push((std::ptr::from_ref(node), node.clone()));
        } else {
            match parts[index + 1].0 {
                Combinator::Child => {
                    for child in children(node) {
                        if matches!(child, Node::Element { .. })
                            && selector_matches(child, &parts[index + 1].1)
                        {
                            collect_selected(child, parts, index + 1, false, out);
                        }
                    }
                }
                Combinator::Descendant => {
                    collect_descendants(node, parts, index + 1, out);
                }
            }
        }
    }
    // The first selector may begin at any descendant.  Once a component has
    // matched, its relation-specific branch above owns the remaining walk.
    if index == 0 {
        for child in children(node) {
            collect_selected(child, parts, 0, false, out);
        }
    }
}

fn collect_descendants(
    node: &Node,
    parts: &[(Combinator, String)],
    index: usize,
    out: &mut Vec<(*const Node, Node)>,
) {
    for child in children(node) {
        if matches!(child, Node::Element { .. }) && selector_matches(child, &parts[index].1) {
            collect_selected(child, parts, index, false, out);
        }
        if index < parts.len() {
            collect_descendants(child, parts, index, out);
        }
    }
}

fn selector_matches(node: &Node, selector: &str) -> bool {
    let Node::Element { tag, attrs, .. } = node else {
        return false;
    };
    let mut base_end = selector.len();
    let mut attributes = Vec::new();
    let bytes = selector.as_bytes();
    let mut cursor = 0;
    while cursor < bytes.len() {
        if bytes[cursor] == b'[' {
            base_end = cursor.min(base_end);
            let Some(end) = selector[cursor + 1..].find(']') else {
                return false;
            };
            let end = cursor + 1 + end;
            attributes.push(&selector[cursor + 1..end]);
            cursor = end + 1;
        } else {
            cursor += 1;
        }
    }
    let base = &selector[..base_end];
    if attributes
        .iter()
        .any(|attribute| !attribute_matches(attrs, attribute))
    {
        return false;
    }
    let rest = base;
    let (tag_name, mut rest) = if let Some(pos) = rest.find(['#', '.']) {
        (&rest[..pos], &rest[pos..])
    } else {
        let tag_name = rest;
        (tag_name, "")
    };
    if !tag_name.is_empty() && tag != &tag_name.to_ascii_lowercase() {
        return false;
    }
    while !rest.is_empty() {
        let marker = rest.as_bytes()[0] as char;
        let tail = &rest[1..];
        let end = tail.find(['#', '.']).unwrap_or(tail.len());
        let value = &tail[..end];
        if value.is_empty() {
            return false;
        }
        match marker {
            '#' if attrs.get("id").is_none_or(|actual| actual != value) => return false,
            '.' if !attrs
                .get("class")
                .is_some_and(|actual| actual.split_whitespace().any(|item| item == value)) =>
            {
                return false;
            }
            '#' | '.' => {}
            _ => return false,
        }
        rest = &tail[end..];
    }
    true
}

fn attribute_matches(attrs: &BTreeMap<String, String>, expression: &str) -> bool {
    let operators = ["!=", "~=", "^=", "$=", "*=", "|=", "="];
    let (name, op, expected) = operators
        .iter()
        .find_map(|operator| {
            expression.split_once(operator).map(|(name, value)| {
                (
                    name.trim(),
                    *operator,
                    Some(value.trim_matches(['\"', '\''])),
                )
            })
        })
        .unwrap_or((expression.trim(), "", None));
    let actual = attrs.get(&name.to_ascii_lowercase());
    if op.is_empty() {
        return actual.is_some();
    }
    let Some(actual) = actual else {
        return op == "!=";
    };
    let expected = expected.unwrap_or_default();
    match op {
        "=" => actual == expected,
        "!=" => actual != expected,
        "~=" => actual.split_whitespace().any(|word| word == expected),
        "^=" => actual.starts_with(expected),
        "$=" => actual.ends_with(expected),
        "*=" => actual.contains(expected),
        "|=" => actual == expected || actual.starts_with(&format!("{expected}-")),
        _ => false,
    }
}

/// Serialize the cleaned tree in the stable form used by the Go fixture generator.
pub fn serialize(root: &Node) -> String {
    let mut out = String::new();
    serialize_into(root, &mut out);
    out
}
fn serialize_into(node: &Node, out: &mut String) {
    match node {
        Node::Document { children } => {
            for child in children {
                serialize_into(child, out);
            }
        }
        Node::Text(text) => out.push_str(&escape(text, false)),
        Node::Doctype(name) => {
            out.push_str("<!DOCTYPE ");
            out.push_str(name);
            out.push('>');
        }
        Node::Comment => {}
        Node::Element {
            tag,
            attrs,
            attr_order,
            children,
        } => {
            out.push('<');
            out.push_str(tag);
            for key in attr_order {
                if let Some(value) = attrs.get(key) {
                    out.push(' ');
                    out.push_str(key);
                    out.push_str("=\"");
                    out.push_str(&escape(value, true));
                    out.push('"');
                }
            }
            if children.is_empty()
                && [
                    "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta",
                    "param", "source", "track", "wbr",
                ]
                .contains(&tag.as_str())
            {
                out.push_str("/>");
                return;
            }
            out.push('>');
            for child in children {
                serialize_into(child, out);
            }
            out.push_str("</");
            out.push_str(tag);
            out.push('>');
        }
    }
}
fn escape(text: &str, attribute: bool) -> String {
    let mut out = String::with_capacity(text.len());
    for ch in text.chars() {
        match ch {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&#34;"),
            '\'' if attribute => out.push_str("&#39;"),
            _ => out.push(ch),
        }
    }
    out
}
