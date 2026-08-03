package mcp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// TestEveryToolBelongsToAtLeastOneProfileAndAllIsTheUnion is the AC of issue
// #31: the profile assignment is a data table, every tool belongs to at
// least one profile, and the all selection contains every tool exactly once.
func TestEveryToolBelongsToAtLeastOneProfileAndAllIsTheUnion(t *testing.T) {
	all, err := SelectTools("all")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(tools) {
		t.Fatalf("all selects %d tools, want %d (the full table)", len(all), len(tools))
	}
	seen := map[string]bool{}
	for _, name := range all {
		if seen[name] {
			t.Errorf("tool %s selected twice by all", name)
		}
		seen[name] = true
	}
	for _, tool := range tools {
		if !seen[tool.Name] {
			t.Errorf("tool %s is not covered by the all selection", tool.Name)
		}
	}
}

// TestDefaultProfileRegistersFewerThanFifteenTools is the AC "Default-Profil
// registriert < 15 Tools".
func TestDefaultProfileRegistersFewerThanFifteenTools(t *testing.T) {
	names, err := SelectTools("")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) >= 15 {
		t.Errorf("default profile registers %d tools, want < 15", len(names))
	}
}

// TestSelectToolsCommaSeparatedCombinations verifies that comma-separated
// profile names combine without duplicates and that unknown profiles fail.
func TestSelectToolsCommaSeparatedCombinations(t *testing.T) {
	core, err := SelectTools("core")
	if err != nil {
		t.Fatal(err)
	}
	nav, err := SelectTools("nav")
	if err != nil {
		t.Fatal(err)
	}
	combined, err := SelectTools("core,nav")
	if err != nil {
		t.Fatal(err)
	}
	if len(combined) != len(core)+len(nav) {
		t.Errorf("core,nav = %d tools, want %d", len(combined), len(core)+len(nav))
	}
	for _, name := range append(append([]string{}, core...), nav...) {
		if !contains(combined, name) {
			t.Errorf("combined selection misses %s", name)
		}
	}
	if _, err := SelectTools("core,unknown"); err == nil {
		t.Fatal("unknown profile must be rejected")
	}
	if _, err := SelectTools("core,,nav"); err != nil {
		t.Fatalf("empty parts must be ignored: %v", err)
	}
}

// TestAllProfilesListedWithDescriptionsAndCounts guards the --list-profiles
// contract: every canonical profile is described and reports its tool count.
func TestAllProfilesListedWithDescriptionsAndCounts(t *testing.T) {
	profiles := AllProfiles()
	if len(profiles) != 6 {
		t.Fatalf("profiles = %d, want 6 (core, nav, state, network, debug, flows)", len(profiles))
	}
	wantOrder := []Profile{ProfileCore, ProfileNav, ProfileState, ProfileNetwork, ProfileDebug, ProfileFlows}
	for index, profile := range profiles {
		if profile.Name != wantOrder[index] {
			t.Errorf("profile %d = %s, want %s", index, profile.Name, wantOrder[index])
		}
		if strings.TrimSpace(profile.Description) == "" {
			t.Errorf("profile %s has no description", profile.Name)
		}
		// Every profile's tool list must match the selection for its name.
		selected, err := SelectTools(string(profile.Name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(selected, profile.Tools) {
			t.Errorf("profile %s tools = %v, want %v", profile.Name, profile.Tools, selected)
		}
	}
}

// TestProfileFilterRegistersOnlySelectedTools verifies the registration path
// used by the mcp command: the server exposes exactly the selected tools.
func TestProfileFilterRegistersOnlySelectedTools(t *testing.T) {
	base := socketBase(t)
	server, err := New(Options{
		Version:    "v0.3.0-test",
		Session:    "test",
		Executable: "symbrowse",
		Profiles:   "core",
		SocketPath: func(session string) (string, error) {
			return daemon.SocketPathIn(base, session)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	send := serve(t, server)
	response := send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	result, _ := response["result"].(map[string]any)
	rawTools, _ := result["tools"].([]any)
	list := make([]string, 0, len(rawTools))
	for _, raw := range rawTools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		list = append(list, name)
	}
	// The core selection must not include nav-only tools.
	if contains(list, "back") || contains(list, "forward") || contains(list, "reload") {
		t.Errorf("core profile leaked nav tools: %v", list)
	}
	if !contains(list, "open") || !contains(list, "snapshot") {
		t.Errorf("core profile misses core tools: %v", list)
	}
}

func contains(list []string, name string) bool {
	for _, entry := range list {
		if entry == name {
			return true
		}
	}
	return false
}
