package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/danieljustus/symaira-browse/internal/mcp"
)

// TestNoDuplicateTopLevelShortDescriptions is the CLI description contract
// (issue #188): no two top-level commands share a Short description.
func TestNoDuplicateTopLevelShortDescriptions(t *testing.T) {
	root := newRootCommand()
	seen := map[string]string{}
	for _, cmd := range root.Commands() {
		short := strings.TrimSpace(cmd.Short)
		if short == "" {
			t.Errorf("command %q has an empty Short description", cmd.Name())
			continue
		}
		if previous, exists := seen[short]; exists {
			t.Errorf("commands %q and %q share the same Short description: %q", previous, cmd.Name(), short)
		}
		seen[short] = cmd.Name()
	}
}

// TestSelectorTakingCommandsHaveLongDocumentation enforces that every
// selector-taking command carries a Long description documenting the accepted
// selector forms (CSS, stable @eN ref, role/name pair) and the [value] argument.
func TestSelectorTakingCommandsHaveLongDocumentation(t *testing.T) {
	root := newRootCommand()
	for _, cmd := range root.Commands() {
		if !strings.Contains(cmd.Use, "<selector>") {
			continue
		}
		if strings.TrimSpace(cmd.Long) == "" {
			t.Errorf("selector-taking command %q lacks a Long description", cmd.Name())
			continue
		}
		if !strings.Contains(cmd.Long, "CSS selector") {
			t.Errorf("command %q Long does not document CSS selector form", cmd.Name())
		}
		if !strings.Contains(cmd.Long, "@e") {
			t.Errorf("command %q Long does not document @ref form", cmd.Name())
		}
		if !strings.Contains(cmd.Long, "role") && !strings.Contains(cmd.Long, "Role") {
			t.Errorf("command %q Long does not document role/name pair form", cmd.Name())
		}
		if !strings.Contains(cmd.Long, "value") && !strings.Contains(cmd.Long, "Value") {
			t.Errorf("command %q Long does not document [value] argument", cmd.Name())
		}
	}
}

// TestEveryTopLevelCommandHasValidGroupID verifies that every top-level command
// registered on the root command belongs to one of the named cobra groups.
func TestEveryTopLevelCommandHasValidGroupID(t *testing.T) {
	root := newRootCommand()
	groups := map[string]bool{}
	for _, g := range root.Groups() {
		groups[g.ID] = true
	}
	if len(groups) == 0 {
		t.Fatal("root command has no groups registered")
	}
	for _, cmd := range root.Commands() {
		if cmd.GroupID == "" {
			t.Errorf("top-level command %q has no GroupID assigned", cmd.Name())
			continue
		}
		if !groups[cmd.GroupID] {
			t.Errorf("top-level command %q has unknown GroupID %q", cmd.Name(), cmd.GroupID)
		}
	}
}

// TestGroupsMirrorMCPProfiles verifies that the cobra groups mirror the
// existing MCP profile taxonomy (issue #188, internal/mcp/tools.go).
func TestGroupsMirrorMCPProfiles(t *testing.T) {
	root := newRootCommand()
	groupIDs := map[string]bool{}
	for _, g := range root.Groups() {
		groupIDs[g.ID] = true
	}
	expectedProfiles := []mcp.Profile{
		mcp.ProfileCore,
		mcp.ProfileNav,
		mcp.ProfileState,
		mcp.ProfileNetwork,
		mcp.ProfileDebug,
		mcp.ProfileFlows,
	}
	for _, profile := range expectedProfiles {
		if !groupIDs[string(profile)] {
			t.Errorf("expected cobra group for MCP profile %q, but none registered", profile)
		}
	}
}

// TestUserFacingDescriptionsHideIssueReferences keeps repository bookkeeping
// out of CLI help while allowing issue references in Go comments.
func TestUserFacingDescriptionsHideIssueReferences(t *testing.T) {
	pattern := regexp.MustCompile(`(?i)issue #[0-9]+`)
	var walk func(*cobra.Command, []string)
	walk = func(command *cobra.Command, path []string) {
		name := strings.Join(append(path, command.Name()), " ")
		for field, value := range map[string]string{"Short": command.Short, "Long": command.Long} {
			if pattern.MatchString(value) {
				t.Errorf("%s contains internal issue reference in %s: %q", name, field, value)
			}
		}
		checkFlags := func(flags *pflag.FlagSet) {
			flags.VisitAll(func(flag *pflag.Flag) {
				if pattern.MatchString(flag.Usage) {
					t.Errorf("%s flag --%s contains internal issue reference: %q", name, flag.Name, flag.Usage)
				}
			})
		}
		checkFlags(command.PersistentFlags())
		checkFlags(command.LocalNonPersistentFlags())
		for _, child := range command.Commands() {
			walk(child, append(path, command.Name()))
		}
	}
	walk(newRootCommand(), nil)
}
