package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// FinderKind selects the semantic attribute used by a find request.
type FinderKind string

const (
	FindRole        FinderKind = "role"
	FindText        FinderKind = "text"
	FindLabel       FinderKind = "label"
	FindPlaceholder FinderKind = "placeholder"
	FindAlt         FinderKind = "alt"
	FindTitle       FinderKind = "title"
	FindTestID      FinderKind = "testid"
)

type FinderAction string

const (
	FindClick      FinderAction = "click"
	FindFill       FinderAction = "fill"
	FindCheck      FinderAction = "check"
	FindHover      FinderAction = "hover"
	FindTextAction FinderAction = "text"
	FindRef        FinderAction = "ref"
	FindFirst      FinderAction = "first"
	FindLast       FinderAction = "last"
	FindNth        FinderAction = "nth"
)

type FindRequest struct {
	Kind   FinderKind   `json:"kind"`
	Query  string       `json:"query"`
	Action FinderAction `json:"action"`
	Value  string       `json:"value,omitempty"`
	Name   string       `json:"name,omitempty"`
	Exact  bool         `json:"exact,omitempty"`
	Index  int          `json:"index,omitempty"`
}

type FindMatch struct {
	Ref  string      `json:"ref"`
	Role string      `json:"role"`
	Name string      `json:"name,omitempty"`
	Node SnapshotRef `json:"node"`
}

type FindResult struct {
	Kind    FinderKind   `json:"kind"`
	Query   string       `json:"query"`
	Action  FinderAction `json:"action"`
	Ref     string       `json:"ref,omitempty"`
	Value   string       `json:"value,omitempty"`
	Matches []FindMatch  `json:"matches,omitempty"`
}

type FindError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Matches []FindMatch `json:"matches,omitempty"`
}

func (e *FindError) Error() string { return e.Message }

func (s *NavigationService) Find(ctx context.Context, request FindRequest) (FindResult, error) {
	if err := validateFindRequest(request); err != nil {
		return FindResult{}, err
	}
	snapshot, err := s.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		return FindResult{}, err
	}
	matches := findMatches(snapshot.Refs, request)
	if len(matches) == 0 {
		return FindResult{}, &FindError{Code: "not_found", Message: fmt.Sprintf("find %s %q matched no elements", request.Kind, request.Query)}
	}
	if request.Action == FindFirst || request.Action == FindLast || request.Action == FindNth {
		selected, err := selectFindMatch(matches, request)
		if err != nil {
			return FindResult{}, err
		}
		return FindResult{Kind: request.Kind, Query: request.Query, Action: request.Action, Ref: selected.Ref, Matches: []FindMatch{selected}}, nil
	}
	if len(matches) > 1 {
		return FindResult{}, &FindError{Code: "ambiguous", Message: fmt.Sprintf("find %s %q matched %d elements; use --name, --exact, first, last, or nth", request.Kind, request.Query, len(matches)), Matches: matches}
	}
	selected := matches[0]
	result := FindResult{Kind: request.Kind, Query: request.Query, Action: request.Action, Ref: selected.Ref, Matches: matches}
	if request.Action == FindRef {
		return result, nil
	}
	if request.Action == FindTextAction {
		result.Value = selected.Node.Name
		if result.Value == "" {
			result.Value = selected.Node.Value
		}
		return result, nil
	}
	interaction := InteractionRequest{Action: InteractionAction(request.Action), Selector: "@" + selected.Ref, Value: request.Value}
	if _, err := s.Interact(ctx, interaction); err != nil {
		return FindResult{}, err
	}
	return result, nil
}

func validateFindRequest(request FindRequest) error {
	if strings.TrimSpace(request.Query) == "" {
		return &FindError{Code: "invalid_find", Message: "find query is required"}
	}
	switch request.Kind {
	case FindRole, FindText, FindLabel, FindPlaceholder, FindAlt, FindTitle, FindTestID:
	default:
		return &FindError{Code: "invalid_find", Message: fmt.Sprintf("unsupported find kind %q", request.Kind)}
	}
	switch request.Action {
	case FindClick, FindFill, FindCheck, FindHover, FindTextAction, FindRef, FindFirst, FindLast, FindNth:
	default:
		return &FindError{Code: "invalid_find", Message: fmt.Sprintf("unsupported find action %q", request.Action)}
	}
	if request.Action == FindFill && request.Value == "" {
		return &FindError{Code: "invalid_find", Message: "find fill value is required"}
	}
	if request.Action == FindNth && request.Index < 0 {
		return &FindError{Code: "invalid_find", Message: "find nth index cannot be negative"}
	}
	return nil
}

func findMatches(refs map[string]SnapshotRef, request FindRequest) []FindMatch {
	keys := make([]string, 0, len(refs))
	for ref := range refs {
		keys = append(keys, ref)
	}
	sort.Strings(keys)
	matches := make([]FindMatch, 0)
	for _, ref := range keys {
		node := refs[ref]
		candidate := findCandidate(node, request.Kind)
		if !matchFindText(candidate, request.Query, request.Exact) {
			continue
		}
		if request.Name != "" && !matchFindText(node.Name, request.Name, request.Exact) {
			continue
		}
		matches = append(matches, FindMatch{Ref: ref, Role: node.Role, Name: node.Name, Node: node})
	}
	return matches
}

func findCandidate(node SnapshotRef, kind FinderKind) string {
	switch kind {
	case FindRole:
		return node.Role
	case FindText, FindLabel:
		if node.Name != "" {
			return node.Name
		}
		return node.Value
	case FindPlaceholder, FindAlt, FindTitle, FindTestID:
		return node.Attributes[string(kind)]
	default:
		return ""
	}
}

func matchFindText(candidate, query string, exact bool) bool {
	if exact {
		return candidate == query
	}
	return strings.Contains(strings.ToLower(candidate), strings.ToLower(query))
}

func selectFindMatch(matches []FindMatch, request FindRequest) (FindMatch, error) {
	index := 0
	switch request.Action {
	case FindLast:
		index = len(matches) - 1
	case FindNth:
		index = request.Index
	}
	if index < 0 || index >= len(matches) {
		return FindMatch{}, &FindError{Code: "index_out_of_range", Message: fmt.Sprintf("find %s index %d is out of range for %d matches", request.Action, index, len(matches)), Matches: matches}
	}
	return matches[index], nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
