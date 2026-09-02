package injection

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

//go:embed patterns.txt
var embeddedPatterns string

// ScanWarning is one heuristic detection, matching the documented
// warnings[{kind, severity, ref, excerpt}] shape (see docs/injection.md).
type ScanWarning struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Ref      string `json:"ref"`
	Excerpt  string `json:"excerpt"`
}

// Detection kinds emitted by the scanner.
const (
	KindHiddenText   = "hidden_text"
	KindImperative   = "imperative"
	KindAriaMismatch = "aria_mismatch"
	KindAttribute    = "attribute"
	KindComment      = "comment"
	KindMeta         = "meta"
)

// ScanOptions controls one scan run.
type ScanOptions struct {
	// PatternsFile optionally replaces the embedded pattern list. The
	// format matches patterns.txt: one phrase per line, # comments.
	PatternsFile string
}

// Scan runs the injection heuristics over a page's HTML. The page content is
// treated as hostile input: nothing is removed or rewritten, only reported.
func Scan(pageHTML string, options ScanOptions) ([]ScanWarning, error) {
	matcher, err := loadMatcher(options.PatternsFile)
	if err != nil {
		return nil, err
	}
	document, err := html.Parse(strings.NewReader(pageHTML))
	if err != nil {
		return nil, fmt.Errorf("parse page html: %w", err)
	}
	styleRules := collectStyleRules(document)
	scanner := &scanner{
		matcher:    matcher,
		styleRules: styleRules,
		styleIndex: newStyleRuleIndex(styleRules),
		styleCache: make(map[*html.Node]map[string]string),
	}
	scanner.walk(document, nil, "", false)
	return scanner.warnings, nil
}

// scanner carries the per-run state.
type scanner struct {
	matcher    *patternMatcher
	styleRules map[string][]string // selector -> declarations (from <style> blocks)
	styleIndex styleRuleIndex
	styleCache map[*html.Node]map[string]string
	warnings   []ScanWarning
}

func (s *scanner) warn(kind, severity, ref, excerpt string) {
	excerpt = strings.TrimSpace(excerpt)
	if excerpt == "" {
		return
	}
	if len(excerpt) > 120 {
		excerpt = excerpt[:120] + "…"
	}
	s.warnings = append(s.warnings, ScanWarning{Kind: kind, Severity: severity, Ref: ref, Excerpt: excerpt})
}

// walk traverses the tree. ancestors carries the visibility-relevant
// elements above the current node (for inherited display/visibility); ref is
// the locator of the nearest element ancestor (text nodes report their
// parent element). hiddenSubtree is true once an ancestor was detected as
// hidden: everything below it is hidden too, and its text must not be
// double-reported as an imperative.
func (s *scanner) walk(node *html.Node, ancestors []*html.Node, ref string, hiddenSubtree bool) {
	if node.Type == html.CommentNode {
		if pattern := s.matchPattern(node.Data); pattern != "" {
			s.warn(KindComment, "low", "html-comment", pattern+" in an HTML comment")
		}
	}
	if node.Type == html.TextNode && !hiddenSubtree {
		if pattern := s.matchPattern(node.Data); pattern != "" {
			s.warn(KindImperative, "high", ref, pattern)
		}
	}
	if node.Type == html.ElementNode {
		ref = s.refFor(node)
		switch strings.ToLower(node.Data) {
		case "meta":
			s.scanMeta(node)
		case "style":
			// Rules were collected in collectStyleRules; nothing to scan.
		}
		s.scanAttributes(node, ref)
		if hidden, hiddenKind := s.hidden(node, ancestors); hidden {
			hiddenSubtree = true
			if text := s.elementText(node); text != "" {
				s.warn(hiddenKind, "medium", ref, text)
			}
		}
		if isInteractive(node) {
			s.scanAriaLabel(node, ref)
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		nextAncestors := ancestors
		if node.Type == html.ElementNode {
			nextAncestors = append(nextAncestors, node)
		}
		s.walk(child, nextAncestors, ref, hiddenSubtree)
	}
}

// scanMeta checks <meta> content for agent-directed imperatives.
func (s *scanner) scanMeta(node *html.Node) {
	name, content := "", ""
	for _, attr := range node.Attr {
		switch strings.ToLower(attr.Key) {
		case "name", "property":
			name = attr.Val
		case "content":
			content = attr.Val
		}
	}
	if name == "" || content == "" {
		return
	}
	if pattern := s.matchPattern(content); pattern != "" {
		s.warn(KindMeta, "medium", "meta[name="+name+"]", pattern+" in meta content")
	}
}

// scanAttributes checks alt and title attributes for imperatives.
func (s *scanner) scanAttributes(node *html.Node, ref string) {
	for _, attr := range node.Attr {
		switch strings.ToLower(attr.Key) {
		case "alt", "title":
			if pattern := s.matchPattern(attr.Val); pattern != "" {
				s.warn(KindAttribute, "medium", ref, pattern+" in "+strings.ToLower(attr.Key)+" attribute")
			}
		}
	}
}

// scanAriaLabel flags interactive elements whose visible text disagrees with
// their accessible name — the classic click-redirect attack.
func (s *scanner) scanAriaLabel(node *html.Node, ref string) {
	var label string
	hasLabel := false
	for _, attr := range node.Attr {
		if strings.ToLower(attr.Key) == "aria-label" {
			label = attr.Val
			hasLabel = true
		}
	}
	if !hasLabel || strings.TrimSpace(label) == "" {
		return
	}
	visible := normalizeText(s.elementText(node))
	accessible := normalizeText(label)
	if visible == "" {
		// Icon-only controls legitimately rely on the accessible name.
		return
	}
	if accessible != visible && !strings.Contains(accessible, visible) && !strings.Contains(visible, accessible) {
		s.warn(KindAriaMismatch, "high", ref, fmt.Sprintf("visible %q vs aria-label %q", visible, accessible))
	}
}

// hidden decides whether an element is hidden by any of the B-15 variants:
// display:none, visibility:hidden, font-size:0, opacity:0, off-viewport
// positioning, or foreground≈background color. The decision considers the
// element's own inline style, matching <style> rules, and inherited state
// from ancestors. It is a heuristic, not a full CSS cascade — documented
// limitation.
func (s *scanner) hidden(node *html.Node, ancestors []*html.Node) (bool, string) {
	for _, ancestor := range ancestors {
		styles := s.stylesFor(ancestor)
		if styles["display"] == "none" {
			return true, KindHiddenText
		}
		if styles["visibility"] == "hidden" {
			return true, KindHiddenText
		}
	}
	styles := s.stylesFor(node)
	if styles["display"] == "none" {
		return true, KindHiddenText
	}
	if styles["visibility"] == "hidden" {
		return true, KindHiddenText
	}
	fontSize := styles["font-size"]
	if fontSize == "0" || fontSize == "0px" || fontSize == "0em" || fontSize == "0pt" || fontSize == "0rem" {
		return true, KindHiddenText
	}
	opacity := styles["opacity"]
	if opacity == "0" || opacity == "0.0" || opacity == "0%" || opacity == "0.00" {
		return true, KindHiddenText
	}
	position := styles["position"]
	if position == "absolute" || position == "fixed" {
		if negativeOffset(styles["left"]) || negativeOffset(styles["top"]) {
			return true, KindHiddenText
		}
	}
	foreground := styles["color"]
	background := styles["background-color"]
	if foreground != "" && background != "" && colorsEqual(foreground, background) {
		return true, KindHiddenText
	}
	return false, ""
}

func (s *scanner) stylesFor(node *html.Node) map[string]string {
	if node == nil {
		return nil
	}
	if s.styleCache == nil {
		s.styleCache = make(map[*html.Node]map[string]string)
	}
	if len(s.styleIndex.rules) == 0 && len(s.styleRules) > 0 {
		s.styleIndex = newStyleRuleIndex(s.styleRules)
	}
	if styles, ok := s.styleCache[node]; ok {
		return styles
	}
	styles := make(map[string]string)
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, "style") {
			for property, value := range parseDeclarations(attr.Val) {
				if _, exists := styles[property]; !exists {
					styles[property] = value
				}
			}
		}
	}
	for _, rule := range s.styleIndex.matching(node) {
		for property, value := range rule.declarations {
			if _, exists := styles[property]; !exists {
				styles[property] = value
			}
		}
	}
	s.styleCache[node] = styles
	return styles
}

// elementText collects the text content of an element (excluding script and
// style content).
func (s *scanner) elementText(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}
	if node.Type == html.ElementNode {
		switch strings.ToLower(node.Data) {
		case "script", "style", "noscript", "template":
			return ""
		}
	}
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(s.elementText(child))
	}
	return builder.String()
}

// refFor builds a stable-ish locator for an element: #id, tag.class, or
// tag:nth-of-type among siblings.
func (s *scanner) refFor(node *html.Node) string {
	tag := strings.ToLower(node.Data)
	for _, attr := range node.Attr {
		if attr.Key == "id" && attr.Val != "" {
			return "#" + attr.Val
		}
	}
	var classes []string
	for _, attr := range node.Attr {
		if attr.Key == "class" && attr.Val != "" {
			classes = append(classes, strings.Fields(attr.Val)...)
		}
	}
	if len(classes) > 0 {
		return tag + "." + strings.Join(classes, ".")
	}
	if node.Parent != nil {
		index := 0
		for sibling := node.Parent.FirstChild; sibling != nil; sibling = sibling.NextSibling {
			if sibling == node {
				break
			}
			if sibling.Type == html.ElementNode && strings.ToLower(sibling.Data) == tag {
				index++
			}
		}
		if index > 0 {
			return fmt.Sprintf("%s:nth-of-type(%d)", tag, index+1)
		}
	}
	return tag
}

// isInteractive reports whether an element is a clickable/operable control.
func isInteractive(node *html.Node) bool {
	switch strings.ToLower(node.Data) {
	case "button", "input", "select", "textarea", "summary", "option":
		return true
	case "a":
		for _, attr := range node.Attr {
			if strings.ToLower(attr.Key) == "href" {
				return true
			}
		}
	}
	for _, attr := range node.Attr {
		switch strings.ToLower(attr.Key) {
		case "role":
			switch attr.Val {
			case "button", "link", "tab", "menuitem", "checkbox", "radio", "switch", "combobox":
				return true
			}
		case "tabindex":
			if attr.Val != "-1" {
				return true
			}
		}
	}
	return false
}

type ahoNode struct {
	children map[rune]*ahoNode
	fail     *ahoNode
	output   string
}

type patternMatcher struct {
	root *ahoNode
}

type customMatcherEntry struct {
	matcher *patternMatcher
	size    int64
	modTime time.Time
}

var (
	embeddedMatcher     *patternMatcher
	embeddedMatcherOnce sync.Once
	embeddedMatcherErr  error

	customMatcherMu    sync.RWMutex
	customMatcherCache = make(map[string]customMatcherEntry)
)

// matchPattern reports the first pattern contained in text, or "".
func (s *scanner) matchPattern(text string) string {
	if s.matcher == nil || s.matcher.root == nil {
		return ""
	}
	normalized := normalizeText(text)
	curr := s.matcher.root
	for _, r := range normalized {
		for curr != s.matcher.root && curr.children[r] == nil {
			curr = curr.fail
		}
		if next := curr.children[r]; next != nil {
			curr = next
		}
		if curr.output != "" {
			return curr.output
		}
	}
	return ""
}

// loadMatcher returns the compiled pattern matcher: the embedded file by default,
// or a custom file whose path is supplied. Parsed pattern sets and compiled matchers
// are cached across Scan calls.
func loadMatcher(path string) (*patternMatcher, error) {
	if path == "" {
		embeddedMatcherOnce.Do(func() {
			var patterns []string
			patterns, embeddedMatcherErr = parsePatternList(embeddedPatterns)
			if embeddedMatcherErr != nil {
				return
			}
			embeddedMatcher, embeddedMatcherErr = compileMatcher(patterns)
		})
		if embeddedMatcherErr != nil {
			return nil, embeddedMatcherErr
		}
		return embeddedMatcher, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read injection pattern file: %w", err)
	}

	customMatcherMu.RLock()
	entry, ok := customMatcherCache[path]
	customMatcherMu.RUnlock()
	if ok && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
		return entry.matcher, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read injection pattern file: %w", err)
	}

	patterns, err := parsePatternList(string(raw))
	if err != nil {
		return nil, err
	}
	matcher, err := compileMatcher(patterns)
	if err != nil {
		return nil, err
	}

	customMatcherMu.Lock()
	customMatcherCache[path] = customMatcherEntry{matcher: matcher, size: info.Size(), modTime: info.ModTime()}
	customMatcherMu.Unlock()
	return matcher, nil
}

// parsePatternList parses lines from a pattern file source.
func parsePatternList(source string) ([]string, error) {
	var patterns []string
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, normalizeText(line))
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("injection pattern list is empty")
	}
	return patterns, nil
}

// compileMatcher builds a compiled Aho-Corasick automaton from the pattern list.
func compileMatcher(patterns []string) (*patternMatcher, error) {
	root := &ahoNode{children: make(map[rune]*ahoNode)}
	for _, p := range patterns {
		curr := root
		for _, r := range p {
			next, ok := curr.children[r]
			if !ok {
				next = &ahoNode{children: make(map[rune]*ahoNode)}
				curr.children[r] = next
			}
			curr = next
		}
		if curr.output == "" {
			curr.output = p
		}
	}

	var queue []*ahoNode
	for _, child := range root.children {
		child.fail = root
		queue = append(queue, child)
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		for r, child := range curr.children {
			f := curr.fail
			for f != nil && f.children[r] == nil {
				f = f.fail
			}
			if f == nil {
				child.fail = root
			} else {
				child.fail = f.children[r]
			}
			if child.output == "" && child.fail != nil && child.fail.output != "" {
				child.output = child.fail.output
			}
			queue = append(queue, child)
		}
	}

	return &patternMatcher{root: root}, nil
}

// normalizeText lowercases and collapses whitespace for matching.
func normalizeText(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// indexedStyleRule stores parsed selector parts and declarations so CSS
// matching does not repeat string splitting or declaration parsing per property.
type indexedStyleRule struct {
	selector     string
	parts        []string
	declarations map[string]string
}

type styleRuleIndex struct {
	rules   []indexedStyleRule
	byID    map[string][]int
	byClass map[string][]int
	byTag   map[string][]int
}

func newStyleRuleIndex(rules map[string][]string) styleRuleIndex {
	selectors := make([]string, 0, len(rules))
	for selector := range rules {
		selectors = append(selectors, selector)
	}
	sort.Strings(selectors)
	index := styleRuleIndex{
		byID:    make(map[string][]int),
		byClass: make(map[string][]int),
		byTag:   make(map[string][]int),
	}
	for _, selector := range selectors {
		parts := strings.Fields(selector)
		if len(parts) == 0 {
			continue
		}
		declarations := make(map[string]string)
		for _, declaration := range rules[selector] {
			property, value, ok := parseDeclaration(declaration)
			if ok {
				if _, exists := declarations[property]; !exists {
					declarations[property] = value
				}
			}
		}
		ruleIndex := len(index.rules)
		index.rules = append(index.rules, indexedStyleRule{selector: selector, parts: parts, declarations: declarations})
		rightmost := parts[len(parts)-1]
		switch {
		case strings.HasPrefix(rightmost, "#"):
			index.byID[strings.TrimPrefix(rightmost, "#")] = append(index.byID[strings.TrimPrefix(rightmost, "#")], ruleIndex)
		case strings.HasPrefix(rightmost, "."):
			index.byClass[strings.TrimPrefix(rightmost, ".")] = append(index.byClass[strings.TrimPrefix(rightmost, ".")], ruleIndex)
		default:
			index.byTag[strings.ToLower(rightmost)] = append(index.byTag[strings.ToLower(rightmost)], ruleIndex)
		}
	}
	return index
}

func (i styleRuleIndex) matching(node *html.Node) []indexedStyleRule {
	if node == nil || len(i.rules) == 0 {
		return nil
	}
	candidateSet := make(map[int]struct{})
	for _, attr := range node.Attr {
		switch strings.ToLower(attr.Key) {
		case "id":
			for _, ruleIndex := range i.byID[attr.Val] {
				candidateSet[ruleIndex] = struct{}{}
			}
		case "class":
			for _, className := range strings.Fields(attr.Val) {
				for _, ruleIndex := range i.byClass[className] {
					candidateSet[ruleIndex] = struct{}{}
				}
			}
		}
	}
	for _, ruleIndex := range i.byTag[strings.ToLower(node.Data)] {
		candidateSet[ruleIndex] = struct{}{}
	}
	candidates := make([]int, 0, len(candidateSet))
	for ruleIndex := range candidateSet {
		candidates = append(candidates, ruleIndex)
	}
	sort.Ints(candidates)
	matched := make([]indexedStyleRule, 0, len(candidates))
	for _, ruleIndex := range candidates {
		rule := i.rules[ruleIndex]
		if selectorPartsMatch(node, rule.parts) {
			matched = append(matched, rule)
		}
	}
	return matched
}

func parseDeclarations(source string) map[string]string {
	values := make(map[string]string)
	for _, declaration := range strings.Split(source, ";") {
		property, value, ok := parseDeclaration(declaration)
		if ok {
			if _, exists := values[property]; !exists {
				values[property] = value
			}
		}
	}
	return values
}

func parseDeclaration(declaration string) (string, string, bool) {
	colon := strings.IndexByte(declaration, ':')
	if colon <= 0 {
		return "", "", false
	}
	property := strings.ToLower(strings.TrimSpace(declaration[:colon]))
	value := strings.TrimSpace(declaration[colon+1:])
	if property == "" || value == "" {
		return "", "", false
	}
	return property, value, true
}

// collectStyleRules extracts selector -> declarations maps from <style>
// blocks. Selector support is deliberately simple: id, class, element, and
// single-descendant combinations; @media and complex selectors are ignored
// (documented limitation).
func collectStyleRules(document *html.Node) map[string][]string {
	rules := make(map[string][]string)
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && strings.ToLower(node.Data) == "style" {
			var css string
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.TextNode {
					css += child.Data
				}
			}
			for _, chunk := range splitStyleBlocks(css) {
				selector, declarations, ok := parseStyleBlock(chunk)
				if !ok {
					continue
				}
				for _, part := range strings.Split(selector, ",") {
					part = strings.TrimSpace(part)
					if part != "" {
						rules[part] = append(rules[part], declarations...)
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return rules
}

// splitStyleBlocks splits a stylesheet into "{ selector { declarations } }"
// chunks. Nested braces are not expected in the supported subset.
func splitStyleBlocks(css string) []string {
	var chunks []string
	for {
		open := strings.Index(css, "{")
		if open < 0 {
			return chunks
		}
		close := strings.Index(css[open:], "}")
		if close < 0 {
			return chunks
		}
		close += open
		chunks = append(chunks, css[:close+1])
		css = css[close+1:]
	}
}

// parseStyleBlock extracts selector and declarations from one block.
func parseStyleBlock(block string) (selector string, declarations []string, ok bool) {
	open := strings.Index(block, "{")
	close := strings.Index(block, "}")
	if open < 0 || close <= open {
		return "", nil, false
	}
	selector = strings.TrimSpace(block[:open])
	if selector == "" || strings.ContainsAny(selector, "@:>+~") {
		return "", nil, false
	}
	body := block[open+1 : close]
	for _, declaration := range strings.Split(body, ";") {
		declaration = strings.TrimSpace(declaration)
		if declaration != "" {
			declarations = append(declarations, declaration)
		}
	}
	return selector, declarations, true
}

func selectorPartsMatch(node *html.Node, parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	current := node
	for index := len(parts) - 1; index >= 0; index-- {
		part := parts[index]
		matched := false
		for current != nil && current.Type == html.ElementNode {
			if simpleSelectorMatches(current, part) {
				matched = true
				if index == 0 {
					return true
				}
				break
			}
			current = current.Parent
		}
		if !matched {
			return false
		}
		if current != nil {
			current = current.Parent
		}
	}
	return false
}

// simpleSelectorMatches matches #id, .class, or tag selectors.
func simpleSelectorMatches(node *html.Node, selector string) bool {
	if strings.HasPrefix(selector, "#") {
		for _, attr := range node.Attr {
			if attr.Key == "id" && attr.Val == strings.TrimPrefix(selector, "#") {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(selector, ".") {
		class := strings.TrimPrefix(selector, ".")
		for _, attr := range node.Attr {
			if attr.Key == "class" {
				for _, token := range strings.Fields(attr.Val) {
					if token == class {
						return true
					}
				}
			}
		}
		return false
	}
	return strings.EqualFold(node.Data, selector)
}

var offsetPattern = regexp.MustCompile(`^-(\d+)(px|em|rem|pt|%)?$`)

// negativeOffset reports whether a length value is a strongly negative
// offset (off-viewport placement).
func negativeOffset(value string) bool {
	value = strings.TrimSpace(value)
	match := offsetPattern.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	amount, err := strconv.Atoi(match[1])
	if err != nil {
		return false
	}
	return amount >= 5000
}

// colorsEqual compares two CSS colors (hex or a small named set) with a
// small tolerance. Unparsable colors are treated as different.
func colorsEqual(a, b string) bool {
	ra, ga, ba, ok := parseColor(a)
	if !ok {
		return false
	}
	rb, gb, bb, ok := parseColor(b)
	if !ok {
		return false
	}
	const tolerance = 10
	return abs(ra-rb) <= tolerance && abs(ga-gb) <= tolerance && abs(ba-bb) <= tolerance
}

var hexColorPattern = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

var namedColors = map[string][3]int{
	"white": {255, 255, 255}, "black": {0, 0, 0},
	"red": {255, 0, 0}, "green": {0, 128, 0}, "blue": {0, 0, 255},
	"gray": {128, 128, 128}, "grey": {128, 128, 128},
	"silver": {192, 192, 192}, "yellow": {255, 255, 0},
	"transparent": {0, 0, 0},
}

func parseColor(value string) (r, g, b int, ok bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if match := hexColorPattern.FindStringSubmatch(value); match != nil {
		hex := match[1]
		if len(hex) == 3 {
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		}
		raw, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return int(raw >> 16 & 0xff), int(raw >> 8 & 0xff), int(raw & 0xff), true
	}
	if rgb, exists := namedColors[value]; exists {
		return rgb[0], rgb[1], rgb[2], true
	}
	return 0, 0, 0, false
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
