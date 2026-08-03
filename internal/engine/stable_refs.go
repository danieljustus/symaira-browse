package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const staleRefHint = "run snapshot --diff to refresh the page reference map"

// RefTombstone records why a previously issued reference can no longer be
// used. Tombstones remain addressable for the lifetime of a navigation service
// so a dead reference never becomes an unrelated live element.
type RefTombstone struct {
	RefKey string `json:"refkey"`
	Ref    string `json:"ref"`
	Role   string `json:"role"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

type stableRefRecord struct {
	key       string
	ref       string
	snapshot  SnapshotRef
	tombstone *RefTombstone
	permanent bool
}

type stableRefRegistry struct {
	byKey   map[string]*stableRefRecord
	byRef   map[string]*stableRefRecord
	current map[string]*stableRefRecord
	next    int
}

func newStableRefRegistry() *stableRefRegistry {
	return &stableRefRegistry{
		byKey:   make(map[string]*stableRefRecord),
		byRef:   make(map[string]*stableRefRecord),
		current: make(map[string]*stableRefRecord),
		next:    1,
	}
}

// RefKey computes the content-addressed identity used by snapshot refs. NUL
// separators make the concatenation unambiguous while retaining the ordered
// role/name/path/ordinal contract from ARCHITEKTUR.md.
func RefKey(role, accessibleName, normalizedDOMPath string, siblingOrdinal int) string {
	payload := strings.Join([]string{
		normalizeRefPart(role),
		normalizeRefPart(accessibleName),
		normalizeRefPart(normalizedDOMPath),
		fmt.Sprintf("%d", siblingOrdinal),
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func normalizeRefPart(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func (s *NavigationService) applyStableSnapshot(result SnapshotResult) SnapshotResult {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	if s.refRegistry == nil {
		s.refRegistry = newStableRefRegistry()
	}
	result = s.refRegistry.apply(result)
	s.refs = make(map[string]SnapshotRef, len(result.Refs))
	for ref, snapshotRef := range result.Refs {
		s.refs[ref] = snapshotRef
	}
	return result
}

func (s *NavigationService) invalidateSnapshotRefs(reason string) {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	if s.refRegistry == nil {
		s.refRegistry = newStableRefRegistry()
	}
	s.refRegistry.invalidate(reason)
	s.snapshotEpoch++
	s.refs = make(map[string]SnapshotRef)
}

func (r *stableRefRegistry) apply(result SnapshotResult) SnapshotResult {
	if len(r.current) > 0 {
		seen := make(map[string]struct{}, len(result.Refs))
		for _, snapshotRef := range result.Refs {
			if snapshotRef.RefKey != "" {
				seen[snapshotRef.RefKey] = struct{}{}
			}
		}
		for key, record := range r.current {
			if _, ok := seen[key]; !ok {
				r.tombstone(record, "removed", false)
			}
		}
	}

	oldToNew := make(map[string]string, len(result.Refs))
	stableRefs := make(map[string]SnapshotRef, len(result.Refs))
	keys := make([]string, 0, len(result.Refs))
	for _, snapshotRef := range result.Refs {
		keys = append(keys, snapshotRef.RefKey)
	}
	sort.Strings(keys)
	for _, key := range keys {
		oldRef := ""
		var snapshotRef SnapshotRef
		for candidate, candidateRef := range result.Refs {
			if candidateRef.RefKey == key {
				oldRef = candidate
				snapshotRef = candidateRef
				break
			}
		}
		if oldRef == "" {
			continue
		}
		record := r.byKey[key]
		if record == nil || record.permanent {
			record = r.newRecord(key)
		} else if record.tombstone != nil {
			record.tombstone = nil
		}
		record.snapshot = snapshotRef
		r.current[key] = record
		stableRefs[record.ref] = snapshotRef
		oldToNew[oldRef] = record.ref
	}
	for key, record := range r.current {
		if _, ok := stableRefs[record.ref]; !ok {
			delete(r.current, key)
		}
	}
	result.Refs = stableRefs
	result.Tree = replaceSnapshotRefs(result.Tree, oldToNew)
	return result
}

func (r *stableRefRegistry) newRecord(key string) *stableRefRecord {
	ref := fmt.Sprintf("e%d", r.next)
	r.next++
	record := &stableRefRecord{key: key, ref: ref}
	r.byKey[key] = record
	r.byRef[ref] = record
	return record
}

func (r *stableRefRegistry) tombstone(record *stableRefRecord, reason string, permanent bool) {
	if record == nil {
		return
	}
	if record.tombstone == nil {
		record.tombstone = &RefTombstone{
			RefKey: record.key,
			Ref:    record.ref,
			Role:   record.snapshot.Role,
			Name:   record.snapshot.Name,
			Reason: reason,
		}
	} else {
		record.tombstone.Reason = reason
	}
	record.permanent = permanent
	delete(r.current, record.key)
}

func (r *stableRefRegistry) invalidate(reason string) {
	keys := make([]string, 0, len(r.current))
	for key := range r.current {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r.tombstone(r.current[key], reason, reason == "navigated")
	}
}

func (r *stableRefRegistry) resolve(ref string) (SnapshotRef, *RefTombstone, bool) {
	record, ok := r.byRef[ref]
	if !ok {
		return SnapshotRef{}, nil, false
	}
	if record.tombstone != nil {
		copy := *record.tombstone
		return SnapshotRef{}, &copy, true
	}
	return record.snapshot, nil, true
}

func replaceSnapshotRefs(tree string, replacements map[string]string) string {
	for oldRef, newRef := range replacements {
		if oldRef != newRef {
			tree = strings.ReplaceAll(tree, "[ref="+oldRef+"]", "[ref="+newRef+"]")
		}
	}
	return tree
}

func assignSnapshotPaths(parsed map[string]*snapshotNode, roots []string) {
	for ordinal, rootID := range roots {
		if node := parsed[rootID]; node != nil {
			node.siblingOrdinal = ordinal
			assignSnapshotPath(parsed, rootID, "/"+snapshotPathSegment(node))
		}
	}
}

func assignSnapshotPath(parsed map[string]*snapshotNode, id, path string) {
	node := parsed[id]
	if node == nil {
		return
	}
	node.domPath = path
	children := append([]string(nil), node.children...)
	sort.SliceStable(children, func(i, j int) bool {
		left, right := parsed[children[i]], parsed[children[j]]
		if left == nil || right == nil {
			return children[i] < children[j]
		}
		leftKey := snapshotPathSortKey(parsed, left)
		rightKey := snapshotPathSortKey(parsed, right)
		if leftKey == rightKey {
			return left.id < right.id
		}
		return leftKey < rightKey
	})
	for ordinal, childID := range children {
		child := parsed[childID]
		if child == nil {
			continue
		}
		child.siblingOrdinal = ordinal
		assignSnapshotPath(parsed, childID, path+"/"+snapshotPathSegment(child))
	}
}

func snapshotPathSegment(node *snapshotNode) string {
	segment := normalizeRefPart(node.role)
	if node.name != "" {
		segment += "[" + normalizeRefPart(node.name) + "]"
	}
	return segment
}

func snapshotPathSortKey(parsed map[string]*snapshotNode, node *snapshotNode) string {
	return snapshotPathSegment(node) + "{" + snapshotSubtreeSignature(parsed, node.id, make(map[string]bool)) + "}"
}

func snapshotSubtreeSignature(parsed map[string]*snapshotNode, id string, visiting map[string]bool) string {
	if visiting[id] {
		return "cycle"
	}
	node := parsed[id]
	if node == nil {
		return "missing"
	}
	visiting[id] = true
	children := make([]string, 0, len(node.children))
	for _, childID := range node.children {
		children = append(children, snapshotSubtreeSignature(parsed, childID, visiting))
	}
	sort.Strings(children)
	delete(visiting, id)
	return snapshotPathSegment(node) + "(" + strings.Join(children, ",") + ")"
}
func staleRefMessage(selector string, tombstone *RefTombstone) string {
	context := tombstone.Role
	if tombstone.Name != "" {
		context += fmt.Sprintf(" %q", tombstone.Name)
	}
	return fmt.Sprintf("stale element ref %q: %s is no longer attached (%s)", selector, context, tombstone.Reason)
}
