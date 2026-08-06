package mcp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

// ProfileInfo describes one tool profile for --list-profiles and for the
// profile documentation (issue #31).
type ProfileInfo struct {
	// Name is the profile identifier used by --tools.
	Name Profile `json:"name"`
	// Description explains when to use the profile.
	Description string `json:"description"`
	// Tools are the tool names registered by this profile, in table order.
	Tools []string `json:"tools"`
}

// profileDescriptions is the data table for the profile registry. The tool
// assignment itself lives in the tool table (tools.go); the profile layer
// only groups it. Profiles whose tools arrive with later milestones
// (state/network/debug/flows) are registered with an empty set and
// documented as such.
var profileDescriptions = []ProfileInfo{
	{Name: ProfileCore, Description: "Page interaction and reading: open, snapshot, click, fill, type, press, wait, read, get, find. The default profile."},
	{Name: ProfileNav, Description: "History navigation: back, forward, reload."},
	{Name: ProfileState, Description: "Sessions, cookies, storage, state save/load, auth login. Tools land with the state milestone (v0.4.0)."},
	{Name: ProfileNetwork, Description: "Routing, mocking, request inspection, HAR, headers, offline. Tools land with the network milestone (v1.0.0)."},
	{Name: ProfileDebug, Description: "Console, errors, eval, a11y, diff, doctor. Tools land with the reach milestones (v1.0.0)."},
	{Name: ProfileFlows, Description: "Flow list/run/record. Tools land with the flows milestone (v0.6.0)."},
}

// ProfileInfoFor builds the runtime view of one profile: its description and
// the tool names it currently registers.
func ProfileInfoFor(profile Profile) (ProfileInfo, error) {
	for _, info := range profileDescriptions {
		if info.Name == profile {
			info.Tools = toolNamesForProfile(profile)
			return info, nil
		}
	}
	return ProfileInfo{}, fmt.Errorf("unknown tool profile %q", profile)
}

// AllProfiles returns the profile registry in canonical order.
func AllProfiles() []ProfileInfo {
	result := make([]ProfileInfo, 0, len(profileDescriptions))
	for _, info := range profileDescriptions {
		info.Tools = toolNamesForProfile(info.Name)
		result = append(result, info)
	}
	return result
}

// SelectTools resolves a --tools value into the tool names to register.
// The value is a comma-separated list of profile names; "all" is the union
// of every profile. The empty value means the default profile (core).
func SelectTools(selection string) ([]string, error) {
	if strings.TrimSpace(selection) == "" {
		selection = string(ProfileCore)
	}
	var names []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(selection, ",") {
		profile := Profile(strings.TrimSpace(raw))
		if profile == "" {
			continue
		}
		if profile == "all" {
			for _, info := range AllProfiles() {
				for _, name := range info.Tools {
					if !seen[name] {
						seen[name] = true
						names = append(names, name)
					}
				}
			}
			continue
		}
		info, err := ProfileInfoFor(profile)
		if err != nil {
			return nil, err
		}
		for _, name := range info.Tools {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// toolNamesForProfile lists the tools assigned to one profile in table
// order. Aliases are excluded: the canonical tool ids are the stable
// profile membership (issue #2).
func toolNamesForProfile(profile Profile) []string {
	var names []string
	for _, tool := range tools {
		if tool.Profile == profile {
			names = append(names, tool.Name)
		}
	}
	return names
}

// CanonicalName resolves an alias to its canonical tool id (issue #2).
// Unknown names are returned unchanged.
func CanonicalName(name string) string {
	for _, tool := range tools {
		if tool.Name == name {
			return name
		}
		for _, alias := range tool.Aliases {
			if alias == name {
				return tool.Name
			}
		}
	}
	return name
}

// RiskClassOf reports the policy risk class of the daemon command behind a
// tool name (canonical or alias). Tools without a static command classify
// as interact (their resolved commands are classified individually by the
// daemon).
func RiskClassOf(name string) string {
	for _, tool := range tools {
		if tool.Name != name && !containsAlias(tool, name) {
			continue
		}
		if tool.Cmd == "" {
			return string(policy.ClassInteract)
		}
		class, err := policy.Classify(tool.Cmd)
		if err != nil {
			return string(policy.ClassInteract)
		}
		return string(class)
	}
	return string(policy.ClassInteract)
}

func containsAlias(tool ProxyTool, name string) bool {
	for _, alias := range tool.Aliases {
		if alias == name {
			return true
		}
	}
	return false
}

// validateToolRegistry enforces the canonical-id contract (issue #2):
// unique canonical names, no alias colliding with a canonical name or with
// another alias, and no tool aliasing itself.
func validateToolRegistry() error {
	canonical := make(map[string]string, len(tools)) // name -> tool
	for _, tool := range tools {
		if tool.Name == "" {
			return errors.New("tool registry: a tool has an empty canonical name")
		}
		if previous, exists := canonical[tool.Name]; exists {
			return fmt.Errorf("tool registry: duplicate canonical tool id %q (also used by %q)", tool.Name, previous)
		}
		canonical[tool.Name] = tool.Name
		for _, alias := range tool.Aliases {
			if alias == "" {
				return fmt.Errorf("tool registry: %q declares an empty alias", tool.Name)
			}
			if alias == tool.Name {
				return fmt.Errorf("tool registry: %q aliases itself", tool.Name)
			}
			if owner, exists := canonical[alias]; exists {
				return fmt.Errorf("tool registry: alias %q collides with %q", alias, owner)
			}
			canonical[alias] = tool.Name
		}
	}
	return nil
}

// RegisterSelection filters the tool table to the selected profile names and
// registers the result on the server. It is the single registration path so
// the profile filter and the tests cannot drift.
func (s *Server) RegisterSelection(selection string) error {
	if err := validateToolRegistry(); err != nil {
		return err
	}
	names, err := SelectTools(selection)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		selected[name] = true
	}
	for _, tool := range tools {
		if selected[tool.Name] {
			s.core.RegisterTool(s.proxyTool(tool))
			for _, alias := range tool.Aliases {
				// Compatibility aliases register as deprecated entries
				// (issue #2): same handler and schema, marked with the
				// canonical replacement.
				s.core.RegisterTool(s.proxyTool(aliasTool(tool, alias)))
			}
		}
	}
	return nil
}

// aliasTool builds the deprecated alias entry for a canonical tool.
func aliasTool(tool ProxyTool, alias string) ProxyTool {
	aliasCopy := tool
	aliasCopy.Name = alias
	aliasCopy.Description = fmt.Sprintf("Deprecated alias of %q; use %q instead. %s", tool.Name, tool.Name, tool.Description)
	return aliasCopy
}
