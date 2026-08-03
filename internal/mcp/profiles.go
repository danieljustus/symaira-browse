package mcp

import (
	"fmt"
	"strings"
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
// order.
func toolNamesForProfile(profile Profile) []string {
	var names []string
	for _, tool := range tools {
		if tool.Profile == profile {
			names = append(names, tool.Name)
		}
	}
	return names
}

// RegisterSelection filters the tool table to the selected profile names and
// registers the result on the server. It is the single registration path so
// the profile filter and the tests cannot drift.
func (s *Server) RegisterSelection(selection string) error {
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
		}
	}
	return nil
}
