package safaribidi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// maxAXNodes bounds the synthesized tree. A page is attacker-controlled input,
// and the walk runs inside the page, so the node budget is enforced there
// rather than by trusting the page to be small.
const maxAXNodes = 4000

// AXTree synthesizes an accessibility tree from the live DOM.
//
// WebDriver BiDi has no accessibility module at all — not merely unimplemented
// in Safari, but absent from the protocol — so there is no tree to fetch. The
// static engine faces the same gap and answers it the same way, by deriving
// roles and names from the document (internal/engine/static.AXTree).
//
// What this engine adds over static is the document it derives from: static
// parses the HTML that arrived over the wire, while this walks the DOM Safari
// actually rendered, after scripts ran. On the /spa fixture that is the
// difference between the pre-hydration shell and the hydrated page.
//
// The result is honestly a *derived* tree, not the browser's own. It carries
// computed visibility from getComputedStyle and getBoundingClientRect, which
// is the part a raw-HTML parse cannot know.
func (e *Engine) AXTree(ctx context.Context, page engine.Page) ([]engine.AXNode, error) {
	contextID, err := e.pageContext(page)
	if err != nil {
		return nil, err
	}
	var result struct {
		Type   string `json:"type"`
		Result remote `json:"result"`
	}
	if err := e.call(ctx, "script.evaluate", map[string]any{
		"expression":   fmt.Sprintf(axTreeScript, maxAXNodes),
		"target":       map[string]any{"context": contextID},
		"awaitPromise": false,
	}, &result); err != nil {
		return nil, fmt.Errorf("safari-bidi engine: build accessibility tree: %w", err)
	}
	if result.Type == "exception" {
		return nil, fmt.Errorf("safari-bidi engine: accessibility tree script failed")
	}
	// script.evaluate returns the JSON document as a BiDi string RemoteValue,
	// so it is unwrapped once before it is a document again.
	var encoded string
	if err := json.Unmarshal(result.Result.Value, &encoded); err != nil {
		return nil, fmt.Errorf("safari-bidi engine: decode accessibility payload: %w", err)
	}
	var nodes []json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &nodes); err != nil {
		return nil, fmt.Errorf("safari-bidi engine: decode accessibility nodes: %w", err)
	}
	tree := make([]engine.AXNode, 0, len(nodes))
	for _, node := range nodes {
		tree = append(tree, engine.AXNode{Raw: node})
	}
	return tree, nil
}

// axTreeScript walks the rendered DOM and emits nodes in the same shape the
// snapshot decoder reads from CDP (nodeId, parentId, role, name, childIds,
// backendDOMNodeId, visible, attributes). %d is the node budget.
const axTreeScript = `(() => {
  const LIMIT = %d;
  const out = [];
  const ids = new Map();
  let next = 1;
  const idFor = (el) => {
    let id = ids.get(el);
    if (!id) { id = "bidi-" + (next++); ids.set(el, id); }
    return id;
  };
  const ROLES = {
    A: "link", BUTTON: "button", INPUT: "textbox", SELECT: "combobox",
    TEXTAREA: "textbox", H1: "heading", H2: "heading", H3: "heading",
    H4: "heading", H5: "heading", H6: "heading", IMG: "image", LI: "listitem",
    UL: "list", OL: "list", NAV: "navigation", MAIN: "main", FORM: "form",
    LABEL: "LabelText", TABLE: "table", IFRAME: "Iframe", P: "paragraph",
    SUMMARY: "button", OPTION: "option"
  };
  const roleFor = (el) => {
    const explicit = el.getAttribute("role");
    if (explicit) return explicit;
    if (el.tagName === "INPUT") {
      const type = (el.getAttribute("type") || "text").toLowerCase();
      if (type === "checkbox") return "checkbox";
      if (type === "radio") return "radio";
      if (type === "submit" || type === "button" || type === "reset") return "button";
      if (type === "file") return "button";
      return "textbox";
    }
    return ROLES[el.tagName] || "generic";
  };
  const nameFor = (el) => {
    const aria = el.getAttribute("aria-label");
    if (aria) return aria.trim();
    const labelled = el.getAttribute("aria-labelledby");
    if (labelled) {
      const ref = document.getElementById(labelled);
      if (ref) return (ref.textContent || "").trim().slice(0, 300);
    }
    if (el.tagName === "IMG") return (el.getAttribute("alt") || "").trim();
    if (el.tagName === "INPUT") {
      const type = (el.getAttribute("type") || "text").toLowerCase();
      if (type === "submit" || type === "button" || type === "reset") return (el.value || "").trim();
      const labels = el.labels;
      if (labels && labels.length) return (labels[0].textContent || "").trim().slice(0, 300);
      return (el.getAttribute("placeholder") || "").trim();
    }
    let text = "";
    for (const child of el.childNodes) {
      if (child.nodeType === 3) text += child.nodeValue;
    }
    text = text.trim();
    if (!text && (el.tagName === "BUTTON" || el.tagName === "A" || el.tagName === "LABEL")) {
      text = (el.textContent || "").trim();
    }
    return text.slice(0, 300);
  };
  const visibleFor = (el) => {
    const style = window.getComputedStyle(el);
    if (style.display === "none" || style.visibility === "hidden" || style.visibility === "collapse") return false;
    if (parseFloat(style.opacity || "1") === 0) return false;
    const rect = el.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  };
  const ATTRS = ["id", "class", "name", "type", "href", "src", "alt", "title", "placeholder", "data-testid", "aria-hidden", "disabled", "checked"];
  const walk = (el, parentId) => {
    if (out.length >= LIMIT) return null;
    const id = idFor(el);
    const attributes = {};
    for (const attr of ATTRS) {
      const value = el.getAttribute ? el.getAttribute(attr) : null;
      if (value !== null && value !== undefined) attributes[attr] = String(value).slice(0, 300);
    }
    const node = {
      nodeId: id,
      parentId: parentId || "",
      role: roleFor(el),
      name: nameFor(el),
      value: el.value === undefined ? "" : String(el.value).slice(0, 300),
      ignored: false,
      visible: visibleFor(el),
      backendDOMNodeId: out.length + 1,
      childIds: [],
      isFrameOwner: el.tagName === "IFRAME" || el.tagName === "FRAME",
      attributes: attributes
    };
    out.push(node);
    let children = Array.from(el.children || []);
    if (el.shadowRoot) children = children.concat(Array.from(el.shadowRoot.children || []));
    for (const child of children) {
      const childId = walk(child, id);
      if (childId) node.childIds.push(childId);
    }
    return id;
  };
  if (document.documentElement) walk(document.documentElement, "");
  return JSON.stringify(out);
})()`
