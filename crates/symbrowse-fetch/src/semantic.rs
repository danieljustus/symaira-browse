//! Semantic block selection used before document rendering.
use crate::dom::{self, Node};

pub const DEFAULT_THRESHOLD: usize = 120;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Category {
    Button,
    Link,
    Input,
    Select,
    Textarea,
    Form,
    Text,
    Image,
}

impl Category {
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Button => "button",
            Self::Link => "link",
            Self::Input => "input",
            Self::Select => "select",
            Self::Textarea => "textarea",
            Self::Form => "form",
            Self::Text => "text",
            Self::Image => "image",
        }
    }
    pub const fn interactive(self) -> bool {
        matches!(
            self,
            Self::Button | Self::Link | Self::Input | Self::Select | Self::Textarea | Self::Form
        )
    }
}

pub fn classify(node: &Node) -> Option<Category> {
    let Node::Element { tag, .. } = node else {
        return None;
    };
    let role = dom::attr(node, "role")
        .unwrap_or_default()
        .to_ascii_lowercase();

    match tag.as_str() {
        "button" => Some(Category::Button),
        "a" if dom::attr(node, "href").is_some_and(|v| !v.is_empty()) => Some(Category::Link),
        "a" => Some(Category::Text),
        "input" => match dom::attr(node, "type")
            .unwrap_or_default()
            .to_ascii_lowercase()
            .as_str()
        {
            "submit" | "button" | "reset" => Some(Category::Button),
            "image" => Some(Category::Image),
            _ => Some(Category::Input),
        },
        "select" => Some(Category::Select),
        "textarea" => Some(Category::Textarea),
        "form" => Some(Category::Form),
        "img" if dom::attr(node, "alt").is_some_and(|v| !v.is_empty()) => Some(Category::Image),
        "h1" | "h2" | "h3" | "h4" | "h5" | "h6" | "p" | "li" | "td" | "th" | "dt" | "dd"
        | "blockquote" | "pre" | "code" | "figcaption" | "article" | "section" | "main"
        | "aside" | "header" | "footer" | "nav" => Some(Category::Text),
        _ => match role.as_str() {
            "button" => Some(Category::Button),
            "link" => Some(Category::Link),
            "textbox" | "searchbox" | "spinbutton" => Some(Category::Input),
            "combobox" | "listbox" => Some(Category::Select),
            _ => None,
        },
    }
}

#[derive(Clone, Copy, Debug)]
pub struct BlockScore<'a> {
    pub node: &'a Node,
    pub text_len: usize,
    pub link_len: usize,
    pub score: f64,
}

pub fn score_blocks(root: &Node) -> Vec<BlockScore<'_>> {
    let mut blocks = Vec::new();
    walk_blocks(root, &mut blocks);
    blocks.sort_by(|a, b| b.score.total_cmp(&a.score));
    blocks
}

pub fn best_block(root: &Node, threshold: usize) -> &Node {
    let blocks = score_blocks(root);
    if let Some(best) = blocks.iter().find(|block| block.text_len >= threshold) {
        return best.node;
    }
    if let Some(best) = blocks.iter().find(|block| block.text_len >= threshold / 2) {
        return best.node;
    }
    root
}

fn walk_blocks<'a>(node: &'a Node, out: &mut Vec<BlockScore<'a>>) {
    if let Node::Element { tag, children, .. } = node
        && matches!(
            tag.as_str(),
            "div"
                | "article"
                | "section"
                | "main"
                | "aside"
                | "p"
                | "table"
                | "ul"
                | "ol"
                | "dl"
                | "blockquote"
        )
    {
        let text_len = dom::text_content(children)
            .chars()
            .filter(|c| !c.is_whitespace())
            .count();
        if text_len > 20 {
            let link_len = link_text_len(node);
            let density = link_len as f64 / text_len as f64;
            let weight = match class_bias(node) {
                1 => 1.5,
                -1 => 0.3,
                _ => 1.0,
            };
            out.push(BlockScore {
                node,
                text_len,
                link_len,
                score: text_len as f64 * (1.0 - density) * weight,
            });
        }
    }
    for child in node_children(node) {
        walk_blocks(child, out);
    }
}

fn node_children(node: &Node) -> &[Node] {
    match node {
        Node::Document { children } | Node::Element { children, .. } => children,
        _ => &[],
    }
}

fn link_text_len(node: &Node) -> usize {
    if let Node::Element { tag, children, .. } = node
        && tag == "a"
    {
        return dom::text_content(children)
            .chars()
            .filter(|c| !c.is_whitespace())
            .count();
    }
    node_children(node).iter().map(link_text_len).sum()
}

fn class_bias(node: &Node) -> i8 {
    let value = [dom::attr(node, "id"), dom::attr(node, "class")]
        .into_iter()
        .flatten()
        .collect::<Vec<_>>()
        .join(" ")
        .to_ascii_lowercase();
    if [
        "article", "main", "content", "post", "body", "entry", "text", "story", "blog", "news",
    ]
    .iter()
    .any(|p| value.contains(p))
    {
        1
    } else if [
        "sidebar",
        "footer",
        "nav",
        "menu",
        "banner",
        "ad-",
        "advert",
        "cookie",
        "consent",
        "share",
        "related",
        "comment",
        "promo",
        "widget",
        "header-nav",
    ]
    .iter()
    .any(|p| value.contains(p))
    {
        -1
    } else {
        0
    }
}
