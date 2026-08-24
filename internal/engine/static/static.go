package static

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/fetch/dom"
	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/danieljustus/symaira-browse/internal/fetch/pipeline"
)

// Capability names reported by the engine.
const (
	CapabilityReadOnly  = "read-only"
	CapabilityNoJS      = "no-javascript"
	CapabilityNoRender  = "no-rendering"
	CapabilityNoNetwork = "no-network-control"
)

// Engine is a static-HTML engine. It fetches pages through the absorbed
// fetch pipeline and answers inspection expressions natively; every other
// operation returns a capability error.
type Engine struct {
	mu     sync.Mutex
	client fetch.Client

	// guard carries the fetch-hardening options (SSRF, robots) and pipeline
	// capabilities absorbed from symfetch (repo consolidation step 5).
	guard GuardOptions

	url        string
	title      string
	document   *html.Node
	rawBody    []byte
	statusCode int
	lastResult *pipeline.Result
	closed     bool
}

// New creates an unstarted static engine with hardened defaults.
func New() *Engine {
	return NewWithGuard(GuardOptions{
		SSRFEnabled:   true,
		RobotsEnabled: true,
	})
}

// NewWithGuard creates an unstarted static engine with explicit guard
// options. Tests use this to fetch local test servers (SSRF guard off);
// production callers should prefer New()'s hardened defaults.
func NewWithGuard(g GuardOptions) *Engine {
	client := g.Client
	if client == nil {
		client, _ = fetch.New(fetch.ProfileHonest)
	}
	return &Engine{
		client: client,
		guard:  g,
	}
}

// Capabilities lists what this engine cannot do, for docs/engines.md and
// diagnostics.
func Capabilities() []string {
	return []string{CapabilityReadOnly, CapabilityNoJS, CapabilityNoRender, CapabilityNoNetwork}
}

// Launch is a no-op: there is no process to start.
func (e *Engine) Launch(context.Context) error { return nil }

// NewContext returns a static context handle.
func (e *Engine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "static"}, nil
}

// NewPage returns a static page handle.
func (e *Engine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "static-page"}, nil
}

// capturingMaterializer intercepts the materialized DOM tree and raw response body
// from the pipeline while preserving the full DOM tree for static inspections.
type capturingMaterializer struct {
	unfilteredDoc *html.Node
	rawBody       []byte
	statusCode    int
}

func (m *capturingMaterializer) Materialize(_ context.Context, resp *fetch.Response) (*dom.Tree, error) {
	m.rawBody = resp.Body
	m.statusCode = resp.StatusCode
	unfiltered, _ := html.Parse(bytes.NewReader(resp.Body))
	m.unfilteredDoc = unfiltered
	return dom.Parse(resp.Body)
}

// Navigate fetches the URL through the absorbed fetch pipeline and parses the HTML document.
func (e *Engine) Navigate(ctx context.Context, _ engine.Page, target string) (engine.NavigationResult, error) {
	e.mu.Lock()
	closed := e.closed
	client := e.client
	guard := e.guard
	e.mu.Unlock()
	if closed {
		return engine.NavigationResult{}, errors.New("static engine: engine is closed")
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return engine.NavigationResult{}, fmt.Errorf("static engine: invalid URL %q: %w", target, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return engine.NavigationResult{}, fmt.Errorf("static engine: unsupported scheme %q (http/https only)", parsed.Scheme)
	}

	if client == nil {
		client, err = fetch.New(fetch.ProfileHonest)
		if err != nil {
			return engine.NavigationResult{}, fmt.Errorf("static engine: initialize fetch client: %w", err)
		}
		e.mu.Lock()
		e.client = client
		e.mu.Unlock()
	}

	popts := guard.pipelineOptions()
	mat := &capturingMaterializer{}

	result, err := pipeline.Run(ctx, client, mat, target, popts)
	if err != nil {
		return engine.NavigationResult{}, fmt.Errorf("static engine: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.url = target
	if result.Meta.FinalURL != "" {
		e.url = result.Meta.FinalURL
	}
	e.title = result.Meta.Title
	if e.title == "" && mat.unfilteredDoc != nil {
		e.title = extractTitle(mat.unfilteredDoc)
	}
	e.document = mat.unfilteredDoc
	e.rawBody = mat.rawBody
	e.statusCode = result.Meta.StatusCode
	if e.statusCode == 0 && mat.statusCode != 0 {
		e.statusCode = mat.statusCode
	}
	if e.statusCode == 0 {
		e.statusCode = http.StatusOK
	}
	e.lastResult = result
	return engine.NavigationResult{FrameID: "static", LoaderID: "static"}, nil
}

// Evaluate answers the known inspection expressions natively. Any other
// JavaScript fails with an explicit capability error.
func (e *Engine) Evaluate(_ context.Context, _ engine.Page, expression string) (engine.EvaluationResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.document == nil {
		return engine.EvaluationResult{}, errors.New("static engine: no page loaded; open a URL first")
	}
	if result, ok, err := e.evaluateInspection(expression); ok {
		if err != nil {
			return engine.EvaluationResult{ExceptionText: err.Error()}, nil
		}
		raw, _ := json.Marshal(result)
		return engine.EvaluationResult{Value: raw, Type: "string"}, nil
	}
	return engine.EvaluationResult{}, &CapabilityError{Message: "javascript evaluation is not supported by the static engine (issue #64); only inspection expressions are answered"}
}

// evaluateInspection handles the exact expressions produced by the engine
// inspection layer (chrome/inspection.go) for the static document.
func (e *Engine) evaluateInspection(expression string) (any, bool, error) {
	switch strings.TrimSpace(expression) {
	case "document.title":
		return e.title, true, nil
	case "location.href":
		return e.url, true, nil
	case "document.documentElement.outerHTML":
		return renderOuterHTML(e.document), true, nil
	}
	// querySelector-based expressions: (function(){const e=document.querySelector("sel");...})()
	selector, rest, ok := parseQuerySelector(expression)
	if !ok {
		return nil, false, nil
	}
	node := querySelector(e.document, selector)
	if node == nil {
		return nil, true, errors.New("selector did not match an element")
	}
	switch {
	case strings.Contains(rest, ".innerText") || strings.Contains(rest, ".textContent"):
		return textContent(node), true, nil
	case strings.Contains(rest, ".innerHTML"):
		return renderInnerHTML(node), true, nil
	case strings.Contains(rest, ".getAttribute("):
		attribute := extractAttribute(rest)
		value := getAttribute(node, attribute)
		if value == nil {
			return nil, true, nil
		}
		return *value, true, nil
	case strings.Contains(rest, ".value"):
		return getAttribute(node, "value"), true, nil
	}
	return nil, false, nil
}

// AXTree builds a minimal accessibility tree from the DOM so snapshots are
// possible on the static engine.
func (e *Engine) AXTree(_ context.Context, _ engine.Page) ([]engine.AXNode, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.document == nil {
		return nil, errors.New("static engine: no page loaded")
	}
	nodes := make([]engine.AXNode, 0)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			role := axRole(node)
			if role != "" {
				name := accessibleName(node)
				payload := map[string]any{
					"role":             role,
					"name":             name,
					"nodeId":           fmt.Sprintf("static-%p", node),
					"backendDOMNodeId": int64(len(nodes) + 1),
					"childIds":         []string{},
					"ignored":          false,
				}
				raw, _ := json.Marshal(payload)
				nodes = append(nodes, engine.AXNode{Raw: raw})
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(e.document)
	return nodes, nil
}

// Screenshot is not supported by the static engine.
func (e *Engine) Screenshot(context.Context, engine.Page) ([]byte, error) {
	return nil, &CapabilityError{Message: "screenshots are not supported by the static engine (no rendering)"}
}

// Close releases the engine.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.document = nil
	e.rawBody = nil
	e.lastResult = nil
	if e.client != nil {
		_ = e.client.Close()
	}
	return nil
}

// NavigationState answers the navigation state without JavaScript: the
// static engine knows the current URL and always reports a completed load.
func (e *Engine) NavigationState(_ context.Context, _ engine.Page) (engine.NavigationState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.document == nil {
		return engine.NavigationState{}, errors.New("static engine: no page loaded")
	}
	status := e.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	return engine.NavigationState{
		URL:         e.url,
		HTTPStatus:  status,
		ReadyState:  "complete",
		NetworkIdle: true,
	}, nil
}

// LastResult returns the pipeline result from the most recent navigation, if available.
func (e *Engine) LastResult() *pipeline.Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastResult
}

// Inspect implements the engine.InspectionEngine capability against the
// static DOM: title/url/html are answered from the parsed document and
// selector-based kinds from a native querySelector walk. Ref-based
// inspections (live DOM node ids) are not supported by a JS-free reader and
// fail with an explicit capability error instead of wrong results (#64).
func (e *Engine) Inspect(_ context.Context, _ engine.Page, request engine.InspectionRequest, target *engine.InteractionTarget) (engine.InspectionResult, error) {
	if target != nil {
		return engine.InspectionResult{}, &CapabilityError{Message: "ref-based inspection is not supported by the static engine (issue #64); use selector-based get commands"}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.document == nil {
		return engine.InspectionResult{}, errors.New("static engine: no page loaded; open a URL first")
	}
	jsonString := func(value string) json.RawMessage {
		raw, _ := json.Marshal(value)
		return raw
	}
	switch request.Kind {
	case engine.InspectTitle:
		return engine.InspectionResult{Kind: request.Kind, Value: jsonString(e.title)}, nil
	case engine.InspectURL:
		return engine.InspectionResult{Kind: request.Kind, Value: jsonString(e.url)}, nil
	case engine.InspectHTML:
		if len(e.rawBody) > 0 {
			return engine.InspectionResult{Kind: request.Kind, Value: jsonString(string(e.rawBody))}, nil
		}
		return engine.InspectionResult{Kind: request.Kind, Value: jsonString(renderOuterHTML(e.document))}, nil
	}
	selector := strings.TrimSpace(request.Selector)
	if selector == "" {
		return engine.InspectionResult{}, &engine.InspectionError{Code: "invalid_inspection", Message: fmt.Sprintf("get %s requires a selector", request.Kind)}
	}
	if request.Kind == engine.InspectCount {
		return engine.InspectionResult{Kind: request.Kind, Selector: selector, Value: jsonString(strconv.Itoa(countMatches(e.document, selector)))}, nil
	}
	node := querySelector(e.document, selector)
	if node == nil {
		return engine.InspectionResult{}, &engine.InspectionError{Code: "invalid_inspection", Message: fmt.Sprintf("selector %q did not match an element", selector)}
	}
	switch request.Kind {
	case engine.InspectText:
		return engine.InspectionResult{Kind: request.Kind, Selector: selector, Value: jsonString(textContent(node))}, nil
	case engine.InspectValue:
		value := getAttribute(node, "value")
		if value == nil {
			empty := ""
			value = &empty
		}
		return engine.InspectionResult{Kind: request.Kind, Selector: selector, Value: jsonString(*value)}, nil
	case engine.InspectAttr:
		if strings.TrimSpace(request.Attribute) == "" {
			return engine.InspectionResult{}, &engine.InspectionError{Code: "invalid_inspection", Message: "get attr requires an attribute name"}
		}
		value := getAttribute(node, request.Attribute)
		if value == nil {
			return engine.InspectionResult{}, &engine.InspectionError{Code: "invalid_inspection", Message: fmt.Sprintf("element has no attribute %q", request.Attribute)}
		}
		return engine.InspectionResult{Kind: request.Kind, Selector: selector, Value: jsonString(*value)}, nil
	}
	return engine.InspectionResult{}, &CapabilityError{Message: fmt.Sprintf("inspection kind %q is not supported by the static engine (issue #64)", request.Kind)}
}

// CapabilityError is the explicit failure for unsupported operations.
type CapabilityError struct{ Message string }

func (e *CapabilityError) Error() string { return e.Message }

// ---- HTML helpers ---------------------------------------------------------

func extractTitle(document *html.Node) string {
	var title string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if title != "" {
			return
		}
		if node.Type == html.ElementNode && node.Data == "title" {
			title = strings.TrimSpace(textContent(node))
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return title
}

func renderOuterHTML(document *html.Node) string {
	var builder strings.Builder
	for child := document.FirstChild; child != nil; child = child.NextSibling {
		renderNodeHTML(child, &builder)
	}
	return builder.String()
}

func renderInnerHTML(node *html.Node) string {
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		renderNodeHTML(child, &builder)
	}
	return builder.String()
}

func renderNodeHTML(node *html.Node, builder *strings.Builder) {
	switch node.Type {
	case html.TextNode:
		builder.WriteString(node.Data)
	case html.ElementNode:
		builder.WriteString("<" + node.Data)
		for _, attribute := range node.Attr {
			builder.WriteString(" " + attribute.Key + `="` + html.EscapeString(attribute.Val) + `"`)
		}
		builder.WriteString(">")
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			renderNodeHTML(child, builder)
		}
		builder.WriteString("</" + node.Data + ">")
	case html.CommentNode:
		// comments are dropped
	}
}

func textContent(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.TrimSpace(builder.String())
}

func getAttribute(node *html.Node, key string) *string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			value := attribute.Val
			return &value
		}
	}
	return nil
}

var querySelectorPattern = regexp.MustCompile(`document\.querySelector\("([^"]*)"\)`)

func parseQuerySelector(expression string) (selector, rest string, ok bool) {
	match := querySelectorPattern.FindStringSubmatch(expression)
	if len(match) != 2 {
		return "", "", false
	}
	return match[1], expression, true
}

func querySelector(root *html.Node, selector string) *html.Node {
	// Support the selectors used by symbrowse inspections: tag, #id, .class
	// and [attr=value].
	parts := strings.Split(selector, " ")
	if len(parts) > 1 {
		// descendant chains are resolved by walking in order
		var current = root
		for _, part := range parts {
			current = firstMatch(current, part)
			if current == nil {
				return nil
			}
		}
		return current
	}
	return firstMatch(root, selector)
}

// countMatches counts every element matching the selector (descendant chains
// included), mirroring document.querySelectorAll(...).length.
func countMatches(root *html.Node, selector string) int {
	parts := strings.Split(selector, " ")
	if len(parts) > 1 {
		total := 0
		var walk func(*html.Node)
		walk = func(node *html.Node) {
			if node.Type == html.ElementNode && matchesChain(node, parts) {
				total++
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
		}
		walk(root)
		return total
	}
	total := 0
	tag, id, class, attribute := parseSimpleSelector(selector)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && simpleMatches(node, tag, id, class, attribute) {
			total++
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return total
}

// matchesChain reports whether node matches the last part of a descendant
// selector chain and has ancestors matching the remaining parts in order.
func matchesChain(node *html.Node, parts []string) bool {
	if !matchesSimple(node, parts[len(parts)-1]) {
		return false
	}
	index := len(parts) - 2
	for ancestor := node.Parent; ancestor != nil && index >= 0; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && matchesSimple(ancestor, parts[index]) {
			index--
		}
	}
	return index < 0
}

// matchesSimple reports whether node matches one simple selector part.
func matchesSimple(node *html.Node, selector string) bool {
	tag, id, class, attribute := parseSimpleSelector(selector)
	return simpleMatches(node, tag, id, class, attribute)
}

func firstMatch(root *html.Node, selector string) *html.Node {
	tag, id, class, attribute := parseSimpleSelector(selector)
	var found *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if found != nil {
			return
		}
		if node.Type == html.ElementNode && simpleMatches(node, tag, id, class, attribute) {
			found = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return found
}

func parseSimpleSelector(selector string) (tag, id, class, attribute string) {
	selector = strings.TrimSpace(selector)
	if strings.HasPrefix(selector, "[") && strings.HasSuffix(selector, "]") {
		inner := strings.TrimSuffix(strings.TrimPrefix(selector, "["), "]")
		if key, value, ok := strings.Cut(inner, "="); ok {
			return "", "", "", key + "=" + strings.Trim(value, `"'`)
		}
		return "", "", "", inner
	}
	for _, part := range strings.FieldsFunc(selector, func(r rune) bool { return r == '#' || r == '.' }) {
		_ = part
	}
	// Split #id / .class from the tag.
	rest := selector
	if index := strings.IndexAny(rest, "#."); index >= 0 {
		rest = selector[:index]
	}
	if strings.HasPrefix(selector, "#") {
		return "", strings.TrimPrefix(selector, "#"), "", ""
	}
	if strings.HasPrefix(selector, ".") {
		return "", "", strings.TrimPrefix(selector, "."), ""
	}
	return rest, "", "", ""
}

func simpleMatches(node *html.Node, tag, id, class, attribute string) bool {
	if tag != "" && node.Data != tag {
		return false
	}
	hasID, hasClass := "", ""
	for _, attr := range node.Attr {
		switch attr.Key {
		case "id":
			hasID = attr.Val
		case "class":
			hasClass = attr.Val
		}
	}
	if id != "" && hasID != id {
		return false
	}
	if class != "" && !containsWord(hasClass, class) {
		return false
	}
	if attribute != "" {
		key, value, hasValue := strings.Cut(attribute, "=")
		actual := getAttribute(node, key)
		if actual == nil {
			return false
		}
		if hasValue && *actual != value {
			return false
		}
	}
	return true
}

func containsWord(haystack, needle string) bool {
	for _, word := range strings.Fields(haystack) {
		if word == needle {
			return true
		}
	}
	return false
}

func extractAttribute(rest string) string {
	index := strings.Index(rest, ".getAttribute(")
	if index < 0 {
		return ""
	}
	tail := rest[index+len(".getAttribute("):]
	end := strings.Index(tail, ")")
	if end < 0 {
		return ""
	}
	return strings.Trim(tail[:end], `"'`)
}

func axRole(node *html.Node) string {
	switch node.Data {
	case "a", "button", "input", "select", "textarea", "h1", "h2", "h3", "p", "img", "li", "ul", "ol", "nav", "main", "form", "label", "table", "iframe":
		return node.Data
	default:
		return ""
	}
}

func accessibleName(node *html.Node) string {
	if value := getAttribute(node, "aria-label"); value != nil && *value != "" {
		return *value
	}
	if node.Data == "img" {
		if value := getAttribute(node, "alt"); value != nil {
			return *value
		}
	}
	return textContent(node)
}

var _ engine.Engine = (*Engine)(nil)
