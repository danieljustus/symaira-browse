package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/testserver"
)

const benchmarkDiffTokenThreshold = 200

// benchmarkInteractionEngine models the deterministic state transition used by
// the B-18 test. It intentionally implements the protocol-neutral engine
// boundary, rather than depending on Chrome or CDP being installed.
type benchmarkInteractionEngine struct {
	fixture testserver.Fixture
	clicked bool
	nodes   []AXNode
	actions []InteractionAction
}

func newBenchmarkInteractionEngine(fixture testserver.Fixture) *benchmarkInteractionEngine {
	return &benchmarkInteractionEngine{fixture: fixture, nodes: benchmarkNodes(fixture, false)}
}

func (e *benchmarkInteractionEngine) Launch(context.Context) error { return nil }
func (e *benchmarkInteractionEngine) NewContext(context.Context) (Context, error) {
	return Context{ID: "benchmark-context"}, nil
}
func (e *benchmarkInteractionEngine) NewPage(context.Context, Context, string) (Page, error) {
	return Page{ID: "benchmark-page"}, nil
}
func (e *benchmarkInteractionEngine) Navigate(context.Context, Page, string) (NavigationResult, error) {
	return NavigationResult{}, nil
}
func (e *benchmarkInteractionEngine) Evaluate(context.Context, Page, string) (EvaluationResult, error) {
	return EvaluationResult{}, nil
}
func (e *benchmarkInteractionEngine) AXTree(context.Context, Page) ([]AXNode, error) {
	return append([]AXNode(nil), e.nodes...), nil
}
func (e *benchmarkInteractionEngine) Screenshot(context.Context, Page) ([]byte, error) {
	return nil, nil
}
func (e *benchmarkInteractionEngine) Close() error { return nil }
func (e *benchmarkInteractionEngine) ResolveElement(context.Context, Page, string) (InteractionTarget, error) {
	return InteractionTarget{NodeID: "action", BackendNodeID: 1}, nil
}
func (e *benchmarkInteractionEngine) ScrollIntoView(context.Context, Page, InteractionTarget) error {
	return nil
}
func (e *benchmarkInteractionEngine) PerformInteraction(_ context.Context, _ Page, target InteractionTarget, request InteractionRequest) error {
	if request.Action != ActionClick || target.NodeID != "action" {
		return fmt.Errorf("benchmark only supports clicking the fixture action")
	}
	e.actions = append(e.actions, request.Action)
	e.clicked = true
	e.nodes = benchmarkNodes(e.fixture, true)
	return nil
}

func TestStableRefsAndSnapshotDiffBenchmark(t *testing.T) {
	server := testserver.New(t)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	results := make([]benchmarkResult, 0, len(testserver.Routes())-1)
	for _, route := range testserver.Routes() {
		if route.Fixture == testserver.SPA {
			continue
		}
		route := route
		t.Run(string(route.Fixture), func(t *testing.T) {
			assertFixtureIsReachable(t, client, server.URLFor(route.Fixture), route.Fixture)
			result := runBenchmarkCase(t, route.Fixture)
			results = append(results, result)
		})
	}
	if len(results) != len(testserver.Routes())-1 {
		t.Fatalf("benchmark covered %d fixtures, want %d", len(results), len(testserver.Routes())-1)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Fixture < results[j].Fixture })
	diffCosts := make([]int, 0, len(results))
	for _, result := range results {
		diffCosts = append(diffCosts, result.DiffTokens)
		if result.RetentionPercent < 80 {
			t.Errorf("%s retained %.1f%% of refs, threshold is >= 80%%", result.Fixture, result.RetentionPercent)
		}
	}
	sort.Ints(diffCosts)
	medianDiffTokens := diffCosts[len(diffCosts)/2]
	if medianDiffTokens >= benchmarkDiffTokenThreshold {
		t.Fatalf("median diff snapshot cost is %d tokens, threshold is < %d", medianDiffTokens, benchmarkDiffTokenThreshold)
	}

	var table strings.Builder
	table.WriteString("\nfixture                 refs  kept  retention  no-diff tokens  diff tokens\n")
	table.WriteString("----------------------  ----  ----  ---------  --------------  -----------\n")
	for _, result := range results {
		fmt.Fprintf(&table, "%-22s  %4d  %4d  %8.1f%%  %14d  %11d\n", result.Fixture, result.InitialRefs, result.RetainedRefs, result.RetentionPercent, result.NoDiffTokens, result.DiffTokens)
	}
	fmt.Fprintf(&table, "median diff tokens: %d (threshold < %d)\n", medianDiffTokens, benchmarkDiffTokenThreshold)
	t.Log(table.String())
}

type benchmarkResult struct {
	Fixture          string
	InitialRefs      int
	RetainedRefs     int
	RetentionPercent float64
	NoDiffTokens     int
	DiffTokens       int
}

func runBenchmarkCase(t *testing.T, fixture testserver.Fixture) benchmarkResult {
	t.Helper()
	ctx := context.Background()
	withoutDiffEngine := newBenchmarkInteractionEngine(fixture)
	withoutDiffService := NewNavigationService(withoutDiffEngine, Page{ID: "benchmark-page"}, NavigationOptions{})
	before, err := withoutDiffService.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	actionRef := benchmarkActionRef(before)
	interaction, err := withoutDiffService.Interact(ctx, InteractionRequest{Action: ActionClick, Selector: "@" + actionRef})
	if err != nil {
		t.Fatal(err)
	}
	if interaction.Ref != actionRef {
		t.Fatalf("interaction ref = %q, want %q", interaction.Ref, actionRef)
	}
	after, err := withoutDiffService.Snapshot(ctx, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	withDiffEngine := newBenchmarkInteractionEngine(fixture)
	withDiffService := NewNavigationService(withDiffEngine, Page{ID: "benchmark-page"}, NavigationOptions{})
	if _, err := withDiffService.Snapshot(ctx, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := withDiffService.Interact(ctx, InteractionRequest{Action: ActionClick, Selector: "@" + actionRef}); err != nil {
		t.Fatal(err)
	}
	diff, err := withDiffService.SnapshotDiff(ctx, SnapshotOptions{Diff: true})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Hint != "" {
		t.Fatalf("diff unexpectedly returned a full snapshot: %q", diff.Hint)
	}
	if len(diff.Changed) == 0 {
		t.Fatalf("diff did not report the deterministic click state change")
	}

	retained := 0
	beforeByKey := make(map[string]string, len(before.Refs))
	for ref, snapshotRef := range before.Refs {
		beforeByKey[snapshotRef.RefKey] = ref
	}
	for ref, snapshotRef := range after.Refs {
		if beforeByKey[snapshotRef.RefKey] == ref {
			retained++
		}
	}
	return benchmarkResult{
		Fixture:          string(fixture),
		InitialRefs:      len(before.Refs),
		RetainedRefs:     retained,
		RetentionPercent: float64(retained) * 100 / float64(len(before.Refs)),
		NoDiffTokens:     estimateBenchmarkTokens(SnapshotResult{SnapshotID: after.SnapshotID, Tree: after.Tree, Refs: after.Refs}),
		DiffTokens:       estimateBenchmarkTokens(diff),
	}
}

func benchmarkActionRef(snapshot SnapshotResult) string {
	for ref, snapshotRef := range snapshot.Refs {
		if snapshotRef.Role == "button" {
			return ref
		}
	}
	return ""
}

func benchmarkNodes(fixture testserver.Fixture, clicked bool) []AXNode {
	status := "Ready"
	if clicked {
		status = "Clicked"
	}
	payloads := []map[string]any{
		{"nodeId": "root", "role": map[string]any{"value": "RootWebArea"}, "childIds": []string{"heading", "main"}},
		{"nodeId": "heading", "parentId": "root", "role": map[string]any{"value": "heading"}, "name": map[string]any{"value": string(fixture) + " fixture"}},
		{"nodeId": "main", "parentId": "root", "role": map[string]any{"value": "main"}, "childIds": []string{"action", "status", "link"}},
		{"nodeId": "action", "parentId": "main", "role": map[string]any{"value": "button"}, "name": map[string]any{"value": "Fixture action"}},
		{"nodeId": "status", "parentId": "main", "role": map[string]any{"value": "status"}, "name": map[string]any{"value": status}},
		{"nodeId": "link", "parentId": "main", "role": map[string]any{"value": "link"}, "name": map[string]any{"value": "Fixture link"}, "properties": []map[string]any{{"name": "url", "value": map[string]any{"value": testserver.PathFor(fixture)}}}},
	}
	nodes := make([]AXNode, 0, len(payloads))
	for _, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		nodes = append(nodes, AXNode{Raw: encoded})
	}
	return nodes
}

func assertFixtureIsReachable(t *testing.T, client *http.Client, url string, fixture testserver.Fixture) {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s fixture: %v", fixture, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s fixture: %v", fixture, err)
	}
	wantStatus := http.StatusOK
	switch fixture {
	case testserver.RedirectLoop:
		wantStatus = http.StatusFound
	case testserver.NotFound:
		wantStatus = http.StatusNotFound
	case testserver.InternalServerError:
		wantStatus = http.StatusInternalServerError
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s fixture status = %d, want %d", fixture, response.StatusCode, wantStatus)
	}
	if len(body) == 0 {
		t.Fatalf("%s fixture returned an empty body", fixture)
	}
}

// estimateBenchmarkTokens is a deterministic, renderer-level estimate rather
// than a claim about a particular model tokenizer. Four UTF-8 characters per
// token is intentionally conservative for the compact JSON payloads measured
// here and is documented so results remain comparable in CGO-free CI.
func estimateBenchmarkTokens(value any) int {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return (len([]rune(string(payload))) + 3) / 4
}
