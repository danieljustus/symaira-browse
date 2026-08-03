package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SnapshotDiffResult is the stable output shape for snapshot --diff --json.
// When the requested predecessor is unavailable, Tree and Refs contain the
// complete current snapshot and Hint explains why no comparison was possible.
type SnapshotDiffResult struct {
	SnapshotID string                 `json:"snapshot_id"`
	Tree       string                 `json:"tree,omitempty"`
	Refs       map[string]SnapshotRef `json:"refs,omitempty"`
	Hint       string                 `json:"hint,omitempty"`
	Added      []SnapshotRef          `json:"added"`
	Removed    []SnapshotRef          `json:"removed"`
	Changed    []SnapshotChange       `json:"changed"`
}

// SnapshotChange contains the before and after values for one changed ref.
type SnapshotChange struct {
	Ref    string      `json:"ref"`
	Before SnapshotRef `json:"before"`
	After  SnapshotRef `json:"after"`
}

type snapshotRecord struct {
	result SnapshotResult
	epoch  uint64
}

func snapshotState(state map[string]string) string {
	if len(state) == 0 {
		return ""
	}
	keys := make([]string, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+state[key])
	}
	return strings.Join(parts, ",")
}

func (s *NavigationService) captureSnapshot(ctx context.Context, options SnapshotOptions) (SnapshotResult, error) {
	if options.Depth < 0 {
		return SnapshotResult{}, fmt.Errorf("snapshot depth cannot be negative")
	}
	if strings.TrimSpace(options.Selector) != "" {
		resolver, ok := s.engine.(AXSelectorResolver)
		if !ok {
			return SnapshotResult{}, fmt.Errorf("snapshot selector is not supported by this engine")
		}
		rootID, err := resolver.AXNodeForSelector(ctx, s.page, options.Selector)
		if err != nil {
			return SnapshotResult{}, fmt.Errorf("resolve snapshot selector %q: %w", options.Selector, err)
		}
		options.RootNodeID = rootID
	}
	nodes, err := s.engine.AXTree(ctx, s.page)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("read accessibility tree: %w", err)
	}
	result, err := RenderSnapshot(nodes, options)
	if err != nil {
		return SnapshotResult{}, err
	}
	result = s.applyStableSnapshot(result)

	snapshotID := s.nextSnapshotID()
	result.SnapshotID = snapshotID
	s.refMu.RLock()
	epoch := s.snapshotEpoch
	s.refMu.RUnlock()
	s.snapshotHistoryMu.Lock()
	s.snapshotHistory[snapshotID] = snapshotRecord{result: result, epoch: epoch}
	s.snapshotOrder = append(s.snapshotOrder, snapshotID)
	s.snapshotHistoryMu.Unlock()
	return result, nil
}

func (s *NavigationService) nextSnapshotID() string {
	s.snapshotHistoryMu.Lock()
	defer s.snapshotHistoryMu.Unlock()
	s.nextSnapshot++
	return fmt.Sprintf("snap-%d", s.nextSnapshot)
}

// SnapshotDiff captures a snapshot and compares it with the requested
// predecessor. An empty Since selects the immediately preceding snapshot.
func (s *NavigationService) SnapshotDiff(ctx context.Context, options SnapshotOptions) (SnapshotDiffResult, error) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	current, err := s.captureSnapshot(ctx, options)
	if err != nil {
		return SnapshotDiffResult{}, err
	}

	predecessorID := strings.TrimSpace(options.Since)
	s.snapshotHistoryMu.Lock()
	if predecessorID == "" && len(s.snapshotOrder) >= 2 {
		predecessorID = s.snapshotOrder[len(s.snapshotOrder)-2]
	}
	previous, found := s.snapshotHistory[predecessorID]
	s.snapshotHistoryMu.Unlock()
	if !found || predecessorID == "" {
		return SnapshotDiffResult{
			SnapshotID: current.SnapshotID,
			Tree:       current.Tree,
			Refs:       current.Refs,
			Hint:       "no previous snapshot available; returning the full snapshot",
			Added:      []SnapshotRef{},
			Removed:    []SnapshotRef{},
			Changed:    []SnapshotChange{},
		}, nil
	}
	added, removed, changed := diffSnapshots(previous.result, current, previous.epoch == currentEpoch(s))
	return SnapshotDiffResult{
		SnapshotID: current.SnapshotID,
		Added:      added,
		Removed:    removed,
		Changed:    changed,
	}, nil
}

func currentEpoch(s *NavigationService) uint64 {
	s.refMu.RLock()
	defer s.refMu.RUnlock()
	return s.snapshotEpoch
}

func diffSnapshots(previous, current SnapshotResult, sameEpoch bool) ([]SnapshotRef, []SnapshotRef, []SnapshotChange) {
	before := previous.Refs
	after := current.Refs
	added := make([]SnapshotRef, 0)
	removed := make([]SnapshotRef, 0)
	changed := make([]SnapshotChange, 0)
	matchedBefore := make(map[string]bool)
	matchedAfter := make(map[string]bool)

	refs := make([]string, 0, len(after))
	for ref := range after {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		beforeRef, ok := before[ref]
		if !ok {
			continue
		}
		matchedBefore[ref] = true
		matchedAfter[ref] = true
		if snapshotRefChanged(beforeRef, after[ref]) {
			changed = append(changed, SnapshotChange{Ref: ref, Before: beforeRef, After: after[ref]})
		}
	}

	// A same-page accessible-name change changes the B-16 ref key. Match it by
	// structural path so it is reported as ~ rather than a misleading +/- pair.
	if sameEpoch {
		beforeKeys := make(map[string]string)
		for ref, item := range before {
			if !matchedBefore[ref] {
				beforeKeys[snapshotStructuralKey(item)] = ref
			}
		}
		for _, ref := range refs {
			if matchedAfter[ref] {
				continue
			}
			key := snapshotStructuralKey(after[ref])
			oldRef, ok := beforeKeys[key]
			if !ok || matchedBefore[oldRef] {
				continue
			}
			matchedBefore[oldRef] = true
			matchedAfter[ref] = true
			changed = append(changed, SnapshotChange{Ref: ref, Before: before[oldRef], After: after[ref]})
		}
	}

	for ref, item := range after {
		if !matchedAfter[ref] {
			added = append(added, item)
		}
	}
	for ref, item := range before {
		if !matchedBefore[ref] {
			removed = append(removed, item)
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i].RefKey < added[j].RefKey })
	sort.Slice(removed, func(i, j int) bool { return removed[i].RefKey < removed[j].RefKey })
	sort.Slice(changed, func(i, j int) bool { return changed[i].Ref < changed[j].Ref })
	return added, removed, changed
}

func snapshotRefChanged(before, after SnapshotRef) bool {
	return before.Name != after.Name || before.State != after.State || before.Value != after.Value || before.Visible != after.Visible
}

func snapshotStructuralKey(ref SnapshotRef) string {
	path := ref.DOMPath
	for {
		start := strings.LastIndex(path, "[")
		if start < 0 {
			break
		}
		end := strings.Index(path[start:], "]")
		if end < 0 {
			break
		}
		path = path[:start] + path[start+end+1:]
	}
	return ref.Role + "|" + path + fmt.Sprintf("|%d", ref.SiblingOrdinal)
}
