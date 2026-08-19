package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SnapshotOptions controls the deterministic accessibility-tree renderer.
// Depth is measured from the selected root (root is depth zero); zero means no
// limit. RootNodeID is populated internally after resolving --selector.
type SnapshotOptions struct {
	Interactive bool   `json:"interactive,omitempty"`
	Compact     bool   `json:"compact,omitempty"`
	Depth       int    `json:"depth,omitempty"`
	Selector    string `json:"selector,omitempty"`
	URLs        bool   `json:"urls,omitempty"`
	Diff        bool   `json:"diff,omitempty"`
	Since       string `json:"since,omitempty"`
	RootNodeID  string `json:"-"`
}

// SnapshotResult is the stable output shape for snapshot --json.
type SnapshotResult struct {
	SnapshotID string                 `json:"snapshot_id,omitempty"`
	Tree       string                 `json:"tree"`
	Refs       map[string]SnapshotRef `json:"refs"`
	Hint       string                 `json:"hint,omitempty"`
}

// SnapshotRef describes the protocol-neutral target behind one rendered ref.
type SnapshotRef struct {
	NodeID         string            `json:"node_id"`
	BackendNodeID  int64             `json:"backend_node_id,omitempty"`
	Role           string            `json:"role"`
	Name           string            `json:"name,omitempty"`
	Value          string            `json:"value,omitempty"`
	State          string            `json:"state,omitempty"`
	Visible        bool              `json:"visible"`
	Interactive    bool              `json:"interactive"`
	URL            string            `json:"url,omitempty"`
	RefKey         string            `json:"refkey,omitempty"`
	DOMPath        string            `json:"dom_path,omitempty"`
	SiblingOrdinal int               `json:"sibling_ordinal,omitempty"`
	Attributes     map[string]string `json:"attributes,omitempty"`
}

// AXSelectorResolver resolves a CSS selector to an accessibility node's
// backend DOM node id. It is optional so renderer tests can inject AX nodes
// without a browser or CDP implementation.
type AXSelectorResolver interface {
	AXNodeForSelector(context.Context, Page, string) (string, error)
}

// Snapshot retrieves and renders the current page accessibility tree.
func (s *NavigationService) Snapshot(ctx context.Context, options SnapshotOptions) (SnapshotResult, error) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	return s.captureSnapshot(ctx, options)
}

// RenderSnapshot renders protocol-neutral AXNode payloads into deterministic
// text. Stable session-local ref allocation is applied by NavigationService.
func RenderSnapshot(nodes []AXNode, options SnapshotOptions) (SnapshotResult, error) {
	if options.Depth < 0 {
		return SnapshotResult{}, errors.New("snapshot depth cannot be negative")
	}
	parsed := make(map[string]*snapshotNode, len(nodes))
	order := make([]string, 0, len(nodes))
	for index, node := range nodes {
		parsedNode, err := decodeSnapshotNode(node.Raw, index)
		if err != nil {
			return SnapshotResult{}, fmt.Errorf("decode accessibility node %d: %w", index, err)
		}
		if _, exists := parsed[parsedNode.id]; exists {
			return SnapshotResult{}, fmt.Errorf("duplicate accessibility node id %q", parsedNode.id)
		}
		parsed[parsedNode.id] = parsedNode
		order = append(order, parsedNode.id)
	}
	if len(parsed) == 0 {
		return SnapshotResult{Tree: "", Refs: map[string]SnapshotRef{}}, nil
	}

	children := make(map[string][]string, len(parsed))
	childOf := make(map[string]bool, len(parsed))
	for _, id := range order {
		node := parsed[id]
		for _, childID := range node.childIDs {
			if _, exists := parsed[childID]; !exists || childID == id {
				continue
			}
			children[id] = append(children[id], childID)
			childOf[childID] = true
		}
	}
	// Older or injected payloads may only carry parentId. Add those links while
	// preserving the input order of the AX tree.
	for _, id := range order {
		node := parsed[id]
		if node.parentID != "" && node.parentID != id && parsed[node.parentID] != nil && !contains(children[node.parentID], id) {
			children[node.parentID] = append(children[node.parentID], id)
			childOf[id] = true
		}
	}
	roots := make([]string, 0, len(parsed))
	if options.RootNodeID != "" {
		if parsed[options.RootNodeID] == nil {
			return SnapshotResult{}, fmt.Errorf("snapshot selector resolved to unknown accessibility node %q", options.RootNodeID)
		}
		roots = append(roots, options.RootNodeID)
	} else {
		for _, id := range order {
			if !childOf[id] {
				roots = append(roots, id)
			}
		}
		if len(roots) == 0 {
			roots = append(roots, order...)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return snapshotIDLess(roots[i], roots[j]) })

	for _, node := range parsed {
		node.children = children[node.id]
	}
	assignSnapshotPaths(parsed, roots)
	refs := make(map[string]SnapshotRef)
	lines := make([]string, 0, len(parsed))
	visited := make(map[string]bool, len(parsed))
	refNumber := 0
	for _, rootID := range roots {
		lines = append(lines, renderSnapshotNode(parsed, rootID, options, 0, visited, &refNumber, refs)...)
	}
	return SnapshotResult{Tree: strings.Join(lines, "\n"), Refs: refs}, nil
}

type snapshotNode struct {
	id             string
	parentID       string
	role           string
	name           string
	description    string
	value          string
	url            string
	ignored        bool
	interactive    bool
	iframe         bool
	shadowRoot     bool
	backendNodeID  int64
	state          map[string]string
	attributes     map[string]string
	visible        bool
	childIDs       []string
	children       []string
	domPath        string
	siblingOrdinal int
	detached       bool
}

func decodeSnapshotNode(raw json.RawMessage, index int) (*snapshotNode, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("empty node payload")
	}
	var payload struct {
		NodeID         json.RawMessage   `json:"nodeId"`
		ParentID       json.RawMessage   `json:"parentId"`
		Role           json.RawMessage   `json:"role"`
		Name           json.RawMessage   `json:"name"`
		Description    json.RawMessage   `json:"description"`
		Value          json.RawMessage   `json:"value"`
		Ignored        bool              `json:"ignored"`
		BackendNodeID  json.RawMessage   `json:"backendDOMNodeId"`
		ChildIDs       []json.RawMessage `json:"childIds"`
		FrameID        string            `json:"frameId"`
		IsFrameOwner   bool              `json:"isFrameOwner"`
		IsShadowRoot   bool              `json:"isShadowRoot"`
		ShadowRoot     bool              `json:"shadowRoot"`
		ShadowBoundary bool              `json:"shadowBoundary"`
		Detached       bool              `json:"detached"`
		IsDetached     bool              `json:"isDetached"`
		Properties     []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"properties"`
		Visible     *bool             `json:"visible"`
		Attributes  map[string]string `json:"attributes"`
		Alt         string            `json:"alt"`
		Title       string            `json:"title"`
		Placeholder string            `json:"placeholder"`
		TestID      string            `json:"testid"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	id := rawStringID(payload.NodeID)
	if id == "" {
		id = fmt.Sprintf("index-%d", index)
	}
	role := normalizeSnapshotRole(rawValue(payload.Role))
	name := rawValue(payload.Name)
	value := rawValue(payload.Value)
	node := &snapshotNode{
		id:            id,
		parentID:      rawStringID(payload.ParentID),
		role:          role,
		name:          name,
		description:   rawValue(payload.Description),
		value:         value,
		ignored:       payload.Ignored,
		backendNodeID: rawInt64(payload.BackendNodeID),
		state:         make(map[string]string),
		visible:       !payload.Ignored,
		attributes:    make(map[string]string),
		childIDs:      make([]string, 0, len(payload.ChildIDs)),
		detached:      payload.Detached || payload.IsDetached,
		iframe:        payload.IsFrameOwner || strings.Contains(role, "iframe") || role == "frame" || strings.Contains(strings.ToLower(rawValue(payload.Role)), "frame"),
		shadowRoot:    payload.IsShadowRoot || payload.ShadowRoot || payload.ShadowBoundary || strings.Contains(role, "shadow"),
	}
	if payload.FrameID != "" && (role == "iframe" || role == "frame") {
		node.iframe = true
	}
	for _, childID := range payload.ChildIDs {
		if id := rawStringID(childID); id != "" {
			node.childIDs = append(node.childIDs, id)
		}
	}
	if payload.Visible != nil {
		node.visible = *payload.Visible
	}
	for key, value := range payload.Attributes {
		node.attributes[strings.ToLower(strings.TrimSpace(key))] = value
	}
	for key, value := range map[string]string{"alt": payload.Alt, "title": payload.Title, "placeholder": payload.Placeholder, "testid": payload.TestID} {
		if value != "" {
			node.attributes[key] = value
		}
	}
	for _, property := range payload.Properties {
		propertyName := strings.ToLower(strings.TrimSpace(property.Name))
		propertyValue := rawValue(property.Value)
		switch propertyName {
		case "url", "href":
			if node.url == "" {
				node.url = propertyValue
			}
		case "focusable", "editable", "checked", "expanded", "selected", "disabled", "pressed":
			if propertyName != "focusable" && propertyName != "editable" || propertyBoolean(property.Value) {
				node.state[propertyName] = propertyValue
			}
			if propertyBoolean(property.Value) && propertyName != "expanded" {
				node.interactive = true
			}
		case "type", "autocomplete":
			if propertyValue != "" {
				node.attributes[propertyName] = propertyValue
			}
		}
	}
	if node.role == "link" && node.url == "" {
		// Some CDP revisions expose href as a direct field instead of an AX
		// property; a second, intentionally narrow pass handles that shape.
		var direct struct{ URL, Href string }
		_ = json.Unmarshal(raw, &direct)
		if direct.URL != "" {
			node.url = direct.URL
		} else {
			node.url = direct.Href
		}
	}
	node.interactive = node.interactive || interactiveSnapshotRole(node.role)
	return node, nil
}

func renderSnapshotNode(parsed map[string]*snapshotNode, id string, options SnapshotOptions, depth int, visited map[string]bool, refNumber *int, refs map[string]SnapshotRef) []string {
	node := parsed[id]
	if node == nil || visited[id] || (options.Depth > 0 && depth > options.Depth) {
		return nil
	}
	visited[id] = true
	include := !node.ignored && (!options.Interactive || node.interactive || node.iframe || node.shadowRoot) && (!options.Compact || node.interactive || node.iframe || node.shadowRoot)
	childrenDepth := depth
	result := make([]string, 0)
	if include {
		*refNumber++
		ref := fmt.Sprintf("e%d", *refNumber)
		refs[ref] = SnapshotRef{
			NodeID: node.id, BackendNodeID: node.backendNodeID, Role: node.role, Name: node.name,
			Value: node.value, State: snapshotState(node.state), Visible: node.visible, Interactive: node.interactive, URL: node.url, RefKey: RefKey(node.role, node.name, node.domPath, node.siblingOrdinal),
			DOMPath: node.domPath, SiblingOrdinal: node.siblingOrdinal,
			Attributes: cloneStringMap(node.attributes),
		}
		line := strings.Repeat("  ", depth) + "- " + node.role
		if node.name != "" {
			line += " \"" + strings.ReplaceAll(node.name, "\"", "\\\"") + "\""
		}
		if node.value != "" && node.name == "" {
			line += " = \"" + strings.ReplaceAll(node.value, "\"", "\\\"") + "\""
		}
		if node.iframe {
			line += " [iframe]"
		}
		if node.shadowRoot {
			line += " [shadow-root]"
		}
		if options.URLs && node.url != "" {
			line += " (" + node.url + ")"
		}
		line += " [ref=" + ref + "]"
		result = append(result, line)
		childrenDepth++
	}
	for _, childID := range node.children {
		result = append(result, renderSnapshotNode(parsed, childID, options, childrenDepth, visited, refNumber, refs)...)
	}
	return result
}

func rawValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var object struct {
		Value json.RawMessage `json:"value"`
	}
	if raw[0] == '{' && json.Unmarshal(raw, &object) == nil && len(object.Value) > 0 {
		return rawScalar(object.Value)
	}
	return rawScalar(raw)
}

func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		return fmt.Sprint(value)
	}
	return ""
}

func rawStringID(raw json.RawMessage) string { return rawScalar(raw) }

func rawInt64(raw json.RawMessage) int64 {
	value, err := strconv.ParseInt(rawScalar(raw), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func propertyBoolean(raw json.RawMessage) bool {
	value := strings.EqualFold(rawValue(raw), "true")
	if value {
		return true
	}
	var object struct {
		Value any `json:"value"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return strings.EqualFold(fmt.Sprint(object.Value), "true")
	}
	return false
}

func normalizeSnapshotRole(role string) string {
	role = strings.TrimSpace(role)
	if strings.EqualFold(role, "rootwebarea") || strings.EqualFold(role, "webarea") {
		return "document"
	}
	if role == "" {
		return "generic"
	}
	return strings.ToLower(role)
}

func interactiveSnapshotRole(role string) bool {
	switch role {
	case "button", "checkbox", "combobox", "link", "listbox", "menuitem", "option", "radio", "searchbox", "slider", "spinbutton", "switch", "tab", "textbox", "treeitem", "gridcell":
		return true
	default:
		return false
	}
}

func snapshotIDLess(left, right string) bool {
	leftNumber, leftErr := strconv.ParseInt(left, 10, 64)
	rightNumber, rightErr := strconv.ParseInt(right, 10, 64)
	if leftErr == nil && rightErr == nil && leftNumber != rightNumber {
		return leftNumber < rightNumber
	}
	return left < right
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
