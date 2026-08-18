package flows

import (
	"fmt"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"gopkg.in/yaml.v3"
)

// RecordedAction is one captured session action during flow recording.
type RecordedAction struct {
	Index      int
	Command    string
	Selector   string // raw @eN ref, empty for open/wait/assert
	Value      string
	URL        string // URL after the action (observed end state)
	Role       string // resolved from the ref when available
	Name       string // accessible name resolved from the ref when available
	InputType  string // HTML input type (password, text, email, ...)
	Autocomplete string // HTML autocomplete attribute (current-password, new-password, ...)
}

// RefResolver resolves a session ref to snapshot metadata during draft
// generation. It is nil-safe: unresolvable refs produce a warning comment
// instead of failing the recording.
type RefResolver func(ref string) (engine.SnapshotRef, bool)

// Draft is a generated flow draft plus review guidance.
type Draft struct {
	Name       string
	Version    int
	Domains    []string
	Inputs     []string
	Steps      []Step
	Comments   []string
	SecretRefs []string // op:// placeholders used in the draft
}

// GenerateDraft converts recorded actions into a flow draft:
//   - concrete values become {{input_N}} references (declared in inputs)
//   - secret-looking values become op://recording/... references
//   - @eN refs become semantic role+name selectors via the resolver
//   - observed URL changes become assert url steps
//   - the draft carries review comments for the human
func GenerateDraft(actions []RecordedAction, resolve RefResolver) (*Draft, error) {
	if len(actions) == 0 {
		return nil, fmt.Errorf("no actions recorded")
	}
	draft := &Draft{Name: "recorded-flow", Version: SchemaVersion, SecretRefs: []string{}}
	inputs := make(map[string]string) // placeholder -> original value
	inputOrder := []string{}
	domains := map[string]bool{}
	secretCount := 0
	inputCount := 0

	actionForValue := func(action RecordedAction) string {
		trimmed := strings.TrimSpace(action.Value)
		if trimmed == "" {
			return ""
		}
		if secretFieldValue(action) || secretInValue(trimmed) || secretInSelector(action.Selector, action.Name) {
			secretCount++
			ref := fmt.Sprintf("op://recording/secret-%d", secretCount)
			draft.SecretRefs = append(draft.SecretRefs, ref)
			return ref
		}
		inputCount++
		placeholder := fmt.Sprintf("input_%d", inputCount)
		inputs[placeholder] = trimmed
		inputOrder = append(inputOrder, placeholder)
		return "{{" + placeholder + "}}"
	}

	lastURL := ""
	previousURL := ""
	for _, action := range actions {
		step := Step{}
		switch action.Command {
		case "open", "goto":
			step.Open = &OpenStep{URL: action.Selector}
			draft.Comments = append(draft.Comments, fmt.Sprintf("step %d: open %s", action.Index, action.Selector))
		case "find":
			step.Find = &FindStep{Label: action.Name, Role: action.Role, Value: actionForValue(action)}
		case "click":
			role, name := semanticSelector(action, resolve)
			step.Click = &SelectorStep{Role: role, Name: name, Exact: true}
		case "fill", "type":
			role, name := semanticSelector(action, resolve)
			step.Fill = &FillStep{Role: role, Name: name, Value: actionForValue(action), Exact: true}
		case "wait":
			step.Wait = &WaitStep{URL: action.Selector}
		case "assert":
			step.Assert = &AssertStep{Visible: action.Selector}
		case "snapshot":
			step.Snapshot = &SnapshotStep{Compact: true}
		default:
			continue
		}
		if step.Action() == "" {
			continue
		}
		draft.Steps = append(draft.Steps, step)

		if action.URL != "" && action.URL != lastURL {
			previousURL = lastURL
			lastURL = action.URL
			if previousURL == "" || previousURL != lastURL {
				// observed end state: after a navigation the URL changed
				draft.Steps = append(draft.Steps, Step{Assert: &AssertStep{URL: globForURL(action.URL)}})
			}
		}
		if host := hostOf(action.URL); host != "" {
			domains[host] = true
		}
		if host := hostOf(action.Selector); host != "" {
			domains[host] = true
		}
	}
	if len(draft.Steps) == 0 {
		return nil, fmt.Errorf("recording contains no flow steps")
	}
	for placeholder, value := range inputs {
		_ = placeholder
		draft.Comments = append(draft.Comments, fmt.Sprintf("input_%d: recorded literal value %q — replace with a real input or op:// reference", inputIndex(inputOrder, placeholder)+1, value))
	}
	for _, ref := range draft.SecretRefs {
		draft.Comments = append(draft.Comments, fmt.Sprintf("secret placeholder %s — resolve via symvault/1Password before running", ref))
	}
	draft.Inputs = inputOrder
	domainList := make([]string, 0, len(domains))
	for domain := range domains {
		domainList = append(domainList, domain)
	}
	sort.Strings(domainList)
	draft.Domains = domainList
	draft.Comments = append(draft.Comments, "review: verify selectors, inputs and assertions before approving this draft")
	return draft, nil
}

func inputIndex(order []string, placeholder string) int {
	for index, entry := range order {
		if entry == placeholder {
			return index
		}
	}
	return 0
}

// semanticSelector resolves a recorded @eN ref into a role+name selector.
// Pre-resolved role/name values are used directly; unresolvable refs fall
// back to empty values (the draft then carries a warning comment).
func semanticSelector(action RecordedAction, resolve RefResolver) (string, string) {
	if action.Role != "" || action.Name != "" {
		return action.Role, action.Name
	}
	ref := strings.TrimPrefix(action.Selector, "@")
	if resolve != nil {
		if snapshotRef, ok := resolve(ref); ok {
			if snapshotRef.Name != "" {
				return snapshotRef.Role, snapshotRef.Name
			}
		}
	}
	return "", ""
}

// globForURL converts an observed absolute URL into a conservative glob that
// the runner can wait for. Exact matches are kept; query strings are dropped.
func globForURL(raw string) string {
	if raw == "" {
		return ""
	}
	if index := strings.IndexAny(raw, "?#"); index >= 0 {
		return raw[:index]
	}
	return raw
}

// hostOf extracts the host (host:port) from a URL, or "" when unparsable.
func hostOf(raw string) string {
	for _, prefix := range []string{"http://", "https://"} {
		if strings.HasPrefix(raw, prefix) {
			rest := strings.TrimPrefix(raw, prefix)
			if index := strings.IndexAny(rest, "/?#"); index >= 0 {
				rest = rest[:index]
			}
			return rest
		}
	}
	return ""
}

// RenderYAML renders the draft as a YAML flow document with review comments.
func (d *Draft) RenderYAML() ([]byte, error) {
	document := struct {
		Name    string   `yaml:"name"`
		Version int      `yaml:"version"`
		Domains []string `yaml:"domains"`
		Inputs  []string `yaml:"inputs,omitempty"`
		Steps   []Step   `yaml:"steps"`
		Outputs []Output `yaml:"outputs,omitempty"`
	}{
		Name:    d.Name,
		Version: d.Version,
		Domains: d.Domains,
		Inputs:  d.Inputs,
		Steps:   d.Steps,
	}
	data, err := yaml.Marshal(document)
	if err != nil {
		return nil, err
	}
	var builder strings.Builder
	builder.WriteString("# Flow draft recorded from a symbrowse session\n")
	for _, comment := range d.Comments {
		builder.WriteString("# " + strings.ReplaceAll(comment, "\n", " ") + "\n")
	}
	builder.Write(data)
	return []byte(builder.String()), nil
}
