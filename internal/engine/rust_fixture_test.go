package engine

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/testserver"
)

// rustEngineFixture is deliberately assembled inside the engine package so the
// oracle exercises the unexported stable-ref registry and snapshot diff code.
// The generator writes only when RUST_PORT_ENGINE_FIXTURE_OUT is set.
type rustEngineFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	Capabilities  CapabilitiesFixture `json:"capabilities"`
	RefKeys       map[string]string   `json:"ref_keys"`
	Registry      RegistryFixture     `json:"registry"`
	Snapshot      SnapshotFixture     `json:"snapshot"`
	Routes        []RouteFixture      `json:"routes"`
	Diff          DiffFixture         `json:"diff"`
}

type CapabilitiesFixture struct {
	Empty   Capabilities `json:"empty"`
	Partial Capabilities `json:"partial"`
}

type RegistryFixture struct {
	First     SnapshotResult `json:"first"`
	Stable    SnapshotResult `json:"stable"`
	Removed   RefTombstone   `json:"removed"`
	Navigated SnapshotResult `json:"navigated"`
}

type SnapshotFixture struct {
	Nodes       []json.RawMessage `json:"nodes"`
	Full        SnapshotResult    `json:"full"`
	Interactive SnapshotResult    `json:"interactive_compact"`
	Depth       SnapshotResult    `json:"depth_1"`
	URLs        SnapshotResult    `json:"urls"`
}

type RouteFixture struct {
	Name       string            `json:"name"`
	Nodes      []json.RawMessage `json:"nodes"`
	AfterNodes []json.RawMessage `json:"after_nodes"`
	Before     SnapshotResult    `json:"before"`
	After      SnapshotResult    `json:"after"`
	Diff       DiffFixture       `json:"diff"`
}

type DiffFixture struct {
	Added   []SnapshotRef    `json:"added"`
	Removed []SnapshotRef    `json:"removed"`
	Changed []SnapshotChange `json:"changed"`
}

func TestGenerateRustPortEngineFixture(t *testing.T) {
	output := os.Getenv("RUST_PORT_ENGINE_FIXTURE_OUT")
	if output == "" {
		t.Skip("set RUST_PORT_ENGINE_FIXTURE_OUT to generate the Rust oracle fixture")
	}
	fixture := buildRustEngineFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(output, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildRustEngineFixture(t *testing.T) rustEngineFixture {
	t.Helper()
	empty := CapabilitiesFor("static")
	partial := CapabilitiesFor("chrome", "TabManager", "FileTransfer")

	registry := newStableRefRegistry()
	first := registry.apply(SnapshotResult{
		Tree: "- button [ref=e1]",
		Refs: map[string]SnapshotRef{"e1": {Role: "button", Name: "Save", RefKey: "key-save"}},
	})
	stable := registry.apply(SnapshotResult{
		Tree: "- button [ref=e7]",
		Refs: map[string]SnapshotRef{"e7": {Role: "button", Name: "Save", RefKey: "key-save"}},
	})
	registry.apply(SnapshotResult{
		Refs: map[string]SnapshotRef{"e2": {Role: "link", Name: "Home", RefKey: "key-home"}},
	})
	_, removed, ok := registry.resolve("e1")
	if !ok || removed == nil {
		t.Fatal("oracle did not produce the removed-ref tombstone")
	}
	registry.invalidate("navigated")
	navigated := registry.apply(SnapshotResult{
		Refs: map[string]SnapshotRef{"e1": {Role: "button", RefKey: "key-save"}},
	})

	nodes := rawFixtureNodes(t)
	full := mustRender(t, nodes, SnapshotOptions{})
	interactive := mustRender(t, nodes, SnapshotOptions{Interactive: true, Compact: true})
	depth := mustRender(t, nodes, SnapshotOptions{Depth: 1})
	urls := mustRender(t, nodes, SnapshotOptions{URLs: true})

	routes := make([]RouteFixture, 0, len(testserver.Routes())-1)
	for _, route := range testserver.Routes() {
		if route.Fixture == testserver.SPA {
			continue
		}
		routeNodes := benchmarkNodes(route.Fixture, false)
		routeRegistry := newStableRefRegistry()
		routeBefore := routeRegistry.apply(mustRender(t, routeNodes, SnapshotOptions{}))
		routeAfter := routeRegistry.apply(mustRender(t, benchmarkNodes(route.Fixture, true), SnapshotOptions{}))
		added, removedRefs, changed := diffSnapshots(routeBefore, routeAfter, true)
		routes = append(routes, RouteFixture{
			Name:       string(route.Fixture),
			Nodes:      rawNodes(routeNodes),
			AfterNodes: rawNodes(benchmarkNodes(route.Fixture, true)),
			Before:     routeBefore,
			After:      routeAfter,
			Diff:       DiffFixture{Added: added, Removed: removedRefs, Changed: changed},
		})
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Name < routes[j].Name })

	before := SnapshotResult{Refs: map[string]SnapshotRef{
		"e1": {RefKey: "key-1", Role: "button", Name: "Save", Value: "old", Visible: true},
		"e2": {RefKey: "key-2", Role: "link", Name: "Old", Visible: true},
	}}
	after := SnapshotResult{Refs: map[string]SnapshotRef{
		"e1": {RefKey: "key-1", Role: "button", Name: "Save", Value: "new", Visible: true},
		"e3": {RefKey: "key-3", Role: "textbox", Name: "Search", Visible: true},
	}}
	added, removedRefs, changed := diffSnapshots(before, after, true)

	fixture := rustEngineFixture{
		SchemaVersion: 1,
		Capabilities:  CapabilitiesFixture{Empty: empty, Partial: partial},
		RefKeys: map[string]string{
			"button|Save|/document/button|0": RefKey("button", "Save", "/document/button", 0),
			"button|Save|/document/button|1": RefKey("button", "Save", "/document/button", 1),
		},
		Registry: RegistryFixture{First: first, Stable: stable, Removed: *removed, Navigated: navigated},
		Snapshot: SnapshotFixture{Nodes: rawNodes(nodes), Full: full, Interactive: interactive, Depth: depth, URLs: urls},
		Routes:   routes,
		Diff:     DiffFixture{Added: added, Removed: removedRefs, Changed: changed},
	}
	return normalizeFixture(fixture)
}

func normalizeFixture(fixture rustEngineFixture) rustEngineFixture {
	fixture.Capabilities.Empty.Interfaces = []string{}
	fixture.Capabilities.Partial.Interfaces = append([]string{}, fixture.Capabilities.Partial.Interfaces...)
	fixture.Capabilities.Partial.Unsupported = append([]string{}, fixture.Capabilities.Partial.Unsupported...)
	fixture.Registry.First = normalizeSnapshot(fixture.Registry.First)
	fixture.Registry.Stable = normalizeSnapshot(fixture.Registry.Stable)
	fixture.Registry.Navigated = normalizeSnapshot(fixture.Registry.Navigated)
	fixture.Snapshot.Full = normalizeSnapshot(fixture.Snapshot.Full)
	fixture.Snapshot.Interactive = normalizeSnapshot(fixture.Snapshot.Interactive)
	fixture.Snapshot.Depth = normalizeSnapshot(fixture.Snapshot.Depth)
	fixture.Snapshot.URLs = normalizeSnapshot(fixture.Snapshot.URLs)
	for index := range fixture.Routes {
		fixture.Routes[index].Before = normalizeSnapshot(fixture.Routes[index].Before)
		fixture.Routes[index].After = normalizeSnapshot(fixture.Routes[index].After)
		// The refs and diff remain authoritative. The tree's ref substitutions are
		// omitted until issue #401 fixes nondeterministic chained replacements in
		// the pinned Go oracle.
		fixture.Routes[index].Before.Tree = ""
		fixture.Routes[index].After.Tree = ""
		fixture.Routes[index].Diff = normalizeDiff(fixture.Routes[index].Diff)
	}
	fixture.Diff = normalizeDiff(fixture.Diff)
	return fixture
}

func normalizeSnapshot(result SnapshotResult) SnapshotResult {
	if result.Refs == nil {
		result.Refs = map[string]SnapshotRef{}
	}
	for key, item := range result.Refs {
		if item.Attributes == nil {
			item.Attributes = map[string]string{}
		}
		result.Refs[key] = item
	}
	return result
}

func normalizeDiff(diff DiffFixture) DiffFixture {
	for index := range diff.Added {
		if diff.Added[index].Attributes == nil {
			diff.Added[index].Attributes = map[string]string{}
		}
	}
	for index := range diff.Removed {
		if diff.Removed[index].Attributes == nil {
			diff.Removed[index].Attributes = map[string]string{}
		}
	}
	for index := range diff.Changed {
		diff.Changed[index].Before = normalizeSnapshot(SnapshotResult{Refs: map[string]SnapshotRef{"before": diff.Changed[index].Before}}).Refs["before"]
		diff.Changed[index].After = normalizeSnapshot(SnapshotResult{Refs: map[string]SnapshotRef{"after": diff.Changed[index].After}}).Refs["after"]
	}
	return diff
}

func rawFixtureNodes(t *testing.T) []AXNode {
	t.Helper()
	return snapshotNodes(t)
}

func rawNodes(nodes []AXNode) []json.RawMessage {
	output := make([]json.RawMessage, 0, len(nodes))
	for _, node := range nodes {
		output = append(output, append(json.RawMessage(nil), node.Raw...))
	}
	return output
}

func mustRender(t *testing.T, nodes []AXNode, options SnapshotOptions) SnapshotResult {
	t.Helper()
	result, err := RenderSnapshot(nodes, options)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
