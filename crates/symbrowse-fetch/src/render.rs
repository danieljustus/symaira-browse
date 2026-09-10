//! Deterministic document, Markdown, JSON and metadata rendering.
use std::collections::BTreeMap;

use serde::{Deserialize, Serialize, de::DeserializeOwned, ser::Serializer};

use crate::dom::{self, Node, Tree};
use crate::semantic::{self, Category};

fn serialize_vec_as_null<T, S>(value: &[T], serializer: S) -> Result<S::Ok, S::Error>
where
    T: Serialize,
    S: Serializer,
{
    if value.is_empty() {
        serializer.serialize_none()
    } else {
        value.serialize(serializer)
    }
}

fn deserialize_null_vec<'de, T, D>(deserializer: D) -> Result<Vec<T>, D::Error>
where
    T: DeserializeOwned,
    D: serde::Deserializer<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(deserializer)?.unwrap_or_default())
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct Element {
    #[serde(rename = "id", skip_serializing_if = "String::is_empty")]
    pub agent_id: String,
    pub category: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub tag: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub text: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub attrs: BTreeMap<String, String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub children: Vec<Element>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct DataIsland {
    pub source: String,
    #[serde(rename = "json")]
    pub json: serde_json::Value,
    #[serde(skip)]
    pub raw_json: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct EscalationHint {
    pub tool: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mcp_tool: String,
    pub reason: String,
    pub command: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
pub struct Document {
    pub url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub final_url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub lang: String,
    #[serde(
        serialize_with = "serialize_vec_as_null",
        deserialize_with = "deserialize_null_vec"
    )]
    pub content: Vec<Element>,
    #[serde(
        serialize_with = "serialize_vec_as_null",
        deserialize_with = "deserialize_null_vec"
    )]
    pub interactive: Vec<Element>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub islands: Vec<DataIsland>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub escalate: Option<EscalationHint>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
#[serde(default)]
pub struct Meta {
    pub final_url: String,
    pub status_code: u16,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub lang: String,
    pub char_count: usize,
    pub est_tokens: usize,
    pub truncated: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub protocol: String,
    pub likely_client_rendered: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub escalate: Option<EscalationHint>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BuildResult {
    pub document: Document,
    pub truncated: bool,
}

pub fn build_document(
    tree: &Tree,
    content: &Node,
    url: impl Into<String>,
    max_chars: usize,
) -> BuildResult {
    let mut builder = Builder {
        next_id: 0,
        seen: 0,
        max_chars,
        truncated: false,
        content: Vec::new(),
        interactive: Vec::new(),
    };
    builder.walk(content);
    let islands = tree
        .islands
        .iter()
        .filter_map(|island| {
            serde_json::from_str(&island.json)
                .ok()
                .map(|json| DataIsland {
                    source: island.source.clone(),
                    raw_json: island.json.clone(),
                    json,
                })
        })
        .collect();
    BuildResult {
        document: Document {
            url: url.into(),
            final_url: String::new(),
            title: tree.title.clone(),
            lang: tree.lang.clone(),
            content: builder.content,
            interactive: builder.interactive,
            islands,
            escalate: None,
        },
        truncated: builder.truncated,
    }
}

struct Builder {
    next_id: usize,
    seen: usize,
    max_chars: usize,
    truncated: bool,
    content: Vec<Element>,
    interactive: Vec<Element>,
}

impl Builder {
    fn walk(&mut self, node: &Node) {
        if self.truncated {
            return;
        }
        if let Some(category) = semantic::classify(node) {
            if category == Category::Text && is_container(node) {
                for child in children(node) {
                    self.walk(child);
                }
                return;
            }
            let element = self.build_element(node, category);
            if category.interactive() {
                self.interactive.push(element.clone());
            }
            self.content.push(element);
            return;
        }
        for child in children(node) {
            self.walk(child);
        }
    }

    fn build_element(&mut self, node: &Node, category: Category) -> Element {
        let (tag, attrs) = match node {
            Node::Element { tag, attrs, .. } => (tag.clone(), attrs.clone()),
            _ => (String::new(), BTreeMap::new()),
        };
        let agent_id = if category.interactive() {
            self.next_id += 1;
            format!("@e{}", self.next_id)
        } else {
            String::new()
        };
        let text = self.consume(compress_whitespace(&dom::text_content(children(node))));
        let mut element = Element {
            agent_id,
            category: category.as_str().into(),
            tag,
            text,
            attrs,
            children: Vec::new(),
        };
        if category == Category::Form {
            for child in children(node) {
                if let Some(child_category) = semantic::classify(child)
                    && child_category != Category::Form
                {
                    let nested = self.build_element(child, child_category);
                    if child_category.interactive() {
                        self.interactive.push(nested.clone());
                    }
                    element.children.push(nested);
                }
            }
        }
        element
    }

    fn consume(&mut self, text: String) -> String {
        if self.max_chars == 0 {
            return text;
        }
        let remaining = self.max_chars.saturating_sub(self.seen);
        if remaining == 0 {
            self.truncated = true;
            return String::new();
        }
        let count = text.chars().count();
        if count > remaining {
            self.truncated = true;
            self.seen += remaining;
            return text.chars().take(remaining).collect::<String>() + "…";
        }
        self.seen += count;
        text
    }
}

fn children(node: &Node) -> &[Node] {
    match node {
        Node::Document { children } | Node::Element { children, .. } => children,
        _ => &[],
    }
}
fn is_container(node: &Node) -> bool {
    matches!(node, Node::Element { tag, .. } if matches!(tag.as_str(), "article" | "section" | "main" | "aside" | "header" | "footer" | "nav" | "div" | "span"))
}
fn compress_whitespace(text: &str) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// Render the selected HTML subtree into readable Markdown.
pub fn markdown(doc: &Document, content: Option<&Node>, include_links: bool) -> String {
    let mut out = String::new();
    if let Some(node) = content {
        render_node(node, &mut out, 0);
        trim_blank_lines(&mut out);
        if !out.is_empty() {
            out.push_str("\n\n");
        }
    }
    if !doc.interactive.is_empty() {
        out.push_str("## Interactive Elements\n\n");
        for element in &doc.interactive {
            out.push_str(&format!(
                "- **{}** `{}`",
                element.agent_id, element.category
            ));
            if !element.text.is_empty() {
                out.push_str(&format!(": {}", element.text));
            }
            if let Some(value) = element.attrs.get("href") {
                out.push_str(&format!(" → {value}"));
            }
            if let Some(value) = element.attrs.get("placeholder") {
                out.push_str(&format!(" [{value}]"));
            }
            if let Some(value) = element.attrs.get("type") {
                out.push_str(&format!(" ({value})"));
            }
            out.push('\n');
        }
        out.push('\n');
    }
    if include_links {
        let mut links = Vec::new();
        collect_element_links(&doc.content, &mut links);
        if !links.is_empty() {
            out.push_str("## Links\n\n");
            for (text, href) in links {
                out.push_str(&format!("- [{text}]({href})\n"));
            }
            out.push('\n');
        }
    }
    if !doc.islands.is_empty() {
        out.push_str("## Data\n\n");
        for island in &doc.islands {
            let json = if island.raw_json.is_empty() {
                serde_json::to_string(&island.json)
            } else {
                Ok(island.raw_json.clone())
            };
            if let Ok(json) = json {
                out.push_str(&format!(
                    "```json\n// Source: {}\n{}\n```\n\n",
                    island.source, json
                ));
            }
        }
    }
    out
}

fn render_node(node: &Node, out: &mut String, list_depth: usize) {
    match node {
        Node::Text(text) => out.push_str(&escape_text(text)),
        Node::Doctype(_) | Node::Comment => {}
        Node::Document { children } => {
            for child in children {
                render_node(child, out, list_depth);
            }
        }
        Node::Element {
            tag,
            attrs,
            children,
            ..
        } => match tag.as_str() {
            tag if matches!(tag, "h1" | "h2" | "h3" | "h4" | "h5" | "h6") => {
                let level = tag[1..].parse::<usize>().unwrap_or(1);
                out.push_str(&format!("{} ", "#".repeat(level)));
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push_str("\n\n");
            }
            "p" => {
                if !out.is_empty() && !out.ends_with("\n\n") {
                    out.push_str("\n\n");
                }
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push_str("\n\n");
            }
            "form" => render_form_children(children, out, list_depth),
            "article" | "main" | "header" | "footer" | "aside" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push_str("\n\n");
            }
            "section" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
            }
            "div" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push('\n');
            }
            "strong" | "b" => {
                out.push_str("**");
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push_str("**");
            }
            "em" | "i" => {
                out.push('*');
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push('*');
            }
            "code" => {
                out.push('`');
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push('`');
            }
            "pre" => {
                out.push_str("```\n");
                out.push_str(dom::text_content(children).trim());
                out.push_str("\n```\n\n");
            }
            "a" => {
                let text = dom::text_content(children).trim().to_owned();
                if let Some(href) = attrs.get("href") {
                    out.push('[');
                    if text.is_empty() {
                        out.push_str(href);
                    } else {
                        out.push_str(&escape_text(&text));
                    }
                    out.push_str("](");
                    out.push_str(href);
                    out.push(')');
                } else {
                    for child in children {
                        render_node(child, out, list_depth);
                    }
                }
            }
            "img" => {
                if let Some(src) = attrs.get("src") {
                    out.push_str(&format!(
                        "![{}]({src})",
                        attrs.get("alt").cloned().unwrap_or_default()
                    ));
                }
            }
            "br" => {
                out.push_str("  \n");
            }
            "ul" | "ol" => {
                for child in children {
                    render_list_item(child, out, list_depth);
                }
                out.push('\n');
            }
            "li" => {
                out.push_str("- ");
                for child in children {
                    render_node(child, out, list_depth + 1);
                }
                out.push('\n');
            }
            "button" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
            }
            "input" | "textarea" => {}
            "option" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push(' ');
            }
            "html" | "body" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
            }
            "head" | "title" | "meta" | "link" | "script" | "style" | "noscript" | "svg"
            | "iframe" | "object" | "embed" | "canvas" | "audio" | "video" | "template"
            | "picture" => {}
            "blockquote" => {
                out.push_str("> ");
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push_str("\n\n");
            }
            "dt" | "dd" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push_str("\n\n");
            }
            "table" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
                out.push_str("\n\n");
            }
            "tbody" | "thead" | "tfoot" | "tr" | "th" | "td" => {
                for child in children {
                    render_node(child, out, list_depth);
                }
            }
            _ => {
                for child in children {
                    render_node(child, out, list_depth);
                }
            }
        },
    }
}
fn render_form_children(children: &[Node], out: &mut String, depth: usize) {
    let mut rendered = String::new();
    for (index, child) in children.iter().enumerate() {
        render_node(child, &mut rendered, depth);
        if !matches!(child, Node::Text(_))
            && children.get(index + 1).is_some_and(
                |next| matches!(next, Node::Text(text) if text.chars().any(char::is_whitespace)),
            )
            && children
                .get(index + 2)
                .is_some_and(|next| !matches!(next, Node::Text(_)))
        {
            rendered.push(' ');
        }
    }
    out.push_str(&rendered.split_whitespace().collect::<Vec<_>>().join(" "));
}

fn render_list_item(node: &Node, out: &mut String, depth: usize) {
    if let Node::Element { tag, children, .. } = node
        && tag == "li"
    {
        out.push_str(&"  ".repeat(depth));
        out.push_str("- ");
        for child in children {
            render_node(child, out, depth + 1);
        }
        out.push('\n');
        return;
    }
    render_node(node, out, depth);
}
fn escape_text(text: &str) -> String {
    let leading = text.chars().next().is_some_and(char::is_whitespace);
    let trailing = text.chars().next_back().is_some_and(char::is_whitespace);
    let mut core = text
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
        .replace('\\', "\\\\")
        .replace('*', "\\*");
    if let Some(rest) = core.strip_prefix('#') {
        core = format!("\\#{rest}");
    }
    if core.is_empty() {
        return if text.is_empty() {
            String::new()
        } else {
            " ".into()
        };
    }
    format!(
        "{}{}{}",
        if leading { " " } else { "" },
        core,
        if trailing { " " } else { "" }
    )
}
fn trim_blank_lines(out: &mut String) {
    loop {
        let previous = out.clone();
        *out = out
            .replace("\n\n\n", "\n\n")
            .replace(" \n\n", "\n\n")
            .replace("\n\n ", "\n\n");
        if *out == previous {
            break;
        }
    }
    *out = out.trim().to_owned();
}
fn collect_element_links(elements: &[Element], out: &mut Vec<(String, String)>) {
    for element in elements {
        if element.category == "link"
            && let Some(href) = element.attrs.get("href")
            && !href.starts_with('#')
            && !href.is_empty()
        {
            out.push((
                if element.text.is_empty() {
                    href.clone()
                } else {
                    element.text.clone()
                },
                href.clone(),
            ));
        }
        collect_element_links(&element.children, out);
    }
}

pub fn json(doc: &Document) -> Result<String, serde_json::Error> {
    serde_json::to_string_pretty(doc)
}

pub fn metadata_header(meta: &Meta, output: &str) -> String {
    let mut header = format!(
        "> **{}** · {} · ~{} tokens",
        meta.title, meta.status_code, meta.est_tokens
    );
    if meta.truncated {
        header.push_str(" · ⚠ truncated");
    }
    if meta.likely_client_rendered {
        header.push_str(" · ⚠ likely client-rendered");
    }
    header.push_str("\n> ");
    header.push_str(&meta.final_url);
    if let Some(hint) = &meta.escalate {
        header.push_str(&format!(
            "\n> ⚠ {} — use symbrowse for JS-rendered pages: {}",
            hint.reason, hint.command
        ));
    }
    header.push_str("\n\n");
    header.push_str(output);
    header
}

pub fn frontmatter_at(meta: &Meta, doc: &Document, fetched_at: &str) -> String {
    let mut out = String::from("---\n");
    if !meta.title.is_empty() {
        out.push_str(&format!("title: {}\n", yaml_scalar(&meta.title)));
    }
    out.push_str(&format!("url: {}\n", yaml_scalar(&doc.url)));
    if !meta.final_url.is_empty() && meta.final_url != doc.url {
        out.push_str(&format!("final_url: {}\n", yaml_scalar(&meta.final_url)));
    }
    out.push_str(&format!("fetched_at: {fetched_at}\n"));
    if !meta.lang.is_empty() {
        out.push_str(&format!("lang: {}\n", yaml_scalar(&meta.lang)));
    }
    out.push_str(&format!("tokens_est: {}\n", meta.est_tokens));
    if let Some(kind) = schema_type(doc) {
        out.push_str(&format!("schema_type: {}\n", yaml_scalar(&kind)));
    }
    out.push_str("---\n\n");
    out
}
fn yaml_scalar(value: &str) -> String {
    if value
        .chars()
        .all(|c| c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | '.' | '/' | ':'))
    {
        value.into()
    } else {
        format!("\"{}\"", value.replace('"', "\\\""))
    }
}
fn schema_type(doc: &Document) -> Option<String> {
    for island in &doc.islands {
        if island.source == "ld+json" {
            if let Some(value) = island.json.get("@type").and_then(serde_json::Value::as_str) {
                return Some(value.into());
            }
            if let Some(value) = island.json.get("type").and_then(serde_json::Value::as_str) {
                return Some(value.into());
            }
            if let Some(value) = island
                .json
                .get("@graph")
                .and_then(serde_json::Value::as_array)
                .and_then(|a| a.first())
                .and_then(|v| v.get("@type"))
                .and_then(serde_json::Value::as_str)
            {
                return Some(value.into());
            }
        }
    }
    None
}

pub const TRUNCATION_MARKER: &str = "\n\n… [truncated: character budget reached]";

/// Limit a rendered body by Unicode scalar values without splitting UTF-8.
pub fn truncate_runes(body: &str, max_chars: usize) -> (String, bool) {
    if max_chars == 0 || body.chars().count() <= max_chars {
        return (body.to_owned(), false);
    }
    let marker_len = TRUNCATION_MARKER.chars().count();
    let room = max_chars.saturating_sub(marker_len);
    let head: String = body.chars().take(room).collect();
    (format!("{}{}", head.trim_end(), TRUNCATION_MARKER), true)
}

/// Compose the deterministic metadata/frontmatter controls around a bounded body.
pub fn bounded_markdown(
    meta: &mut Meta,
    doc: &Document,
    body: &str,
    max_chars: usize,
    with_frontmatter: bool,
    fetched_at: &str,
) -> String {
    let (bounded, cut) = truncate_runes(body, max_chars);
    meta.truncated |= cut;
    meta.char_count = bounded.chars().count();
    meta.est_tokens = meta.char_count / 4;
    let mut output = if meta.escalate.is_some() || meta.truncated || meta.likely_client_rendered {
        metadata_header(meta, &bounded)
    } else {
        bounded
    };
    if with_frontmatter {
        output = format!("{}{}", frontmatter_at(meta, doc, fetched_at), output);
    }
    output
}
