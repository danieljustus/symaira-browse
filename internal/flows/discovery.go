package flows

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Origin identifies where a flow was discovered.
type Origin string

const (
	// OriginSymskills: discovered through the symskills library (SSOT).
	OriginSymskills Origin = "symskills"
	// OriginGlobal: discovered in ~/.config/symbrowse/flows.
	OriginGlobal Origin = "global"
	// OriginProject: discovered in ./.symbrowse/flows (highest precedence).
	OriginProject Origin = "project"
)

// FoundFlow is one discovered flow with its origin.
type FoundFlow struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Origin  Origin `json:"origin"`
	Version int    `json:"version"`
	Steps   int    `json:"steps"`
}

// DiscoveryOptions configures flow discovery.
type DiscoveryOptions struct {
	// SymskillsBinary overrides the symskills executable lookup (tests).
	SymskillsBinary string
}

// Discover finds all flows with their origin. Precedence: project-local
// flows win over global flows; symskills is consulted when the binary is
// available (runtime detection, no compile-time import).
func Discover(options DiscoveryOptions) ([]FoundFlow, error) {
	flows := make(map[string]FoundFlow) // key: name, project wins on conflict
	order := []Origin{OriginProject, OriginGlobal, OriginSymskills}

	project, err := discoverDirectory(".symbrowse/flows", OriginProject)
	if err == nil {
		mergeFound(flows, project)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		global, err := discoverDirectory(filepath.Join(home, ".config", "symbrowse", "flows"), OriginGlobal)
		if err == nil {
			mergeFound(flows, global)
		}
	}
	if binary := resolveSymskillsBinary(options.SymskillsBinary); binary != "" {
		skills, err := discoverViaSymskills(binary)
		if err == nil {
			mergeFound(flows, skills)
		}
	}

	result := make([]FoundFlow, 0, len(flows))
	for _, flow := range flows {
		result = append(result, flow)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return originRank(result[i].Origin) < originRank(result[j].Origin)
		}
		return result[i].Name < result[j].Name
	})
	_ = order
	return result, nil
}

// discoverDirectory scans one directory for *.yaml/yml flow documents that
// parse as flows.
func discoverDirectory(directory string, origin Origin) ([]FoundFlow, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var found []FoundFlow
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		path := filepath.Join(directory, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		flow, err := Parse(data, path)
		if err != nil {
			continue // not a flow document (or invalid); skip silently
		}
		found = append(found, FoundFlow{
			Name:    flow.Name,
			Path:    path,
			Origin:  origin,
			Version: flow.Version,
			Steps:   len(flow.Steps),
		})
	}
	return found, nil
}

// symskillsSkill is the subset of `symskills list --json` we consume.
type symskillsSkill struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// discoverViaSymskills asks the installed symskills binary for its library
// and scans skill directories for flow documents. Runtime detection only —
// no compile-time import of symaira-skills.
func discoverViaSymskills(binary string) ([]FoundFlow, error) {
	command := exec.Command(binary, "list", "--json")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("symskills list: %w", err)
	}
	var payload struct {
		Skills []symskillsSkill `json:"skills"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return nil, fmt.Errorf("decode symskills list: %w", err)
	}
	var found []FoundFlow
	for _, skill := range payload.Skills {
		for _, sub := range []string{"flows", "."} {
			directory := filepath.Join(skill.Path, sub)
			entries, err := os.ReadDir(directory)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
					continue
				}
				if sub == "." && !strings.Contains(name, "flow") {
					continue
				}
				path := filepath.Join(directory, name)
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				flow, err := Parse(data, path)
				if err != nil {
					continue
				}
				found = append(found, FoundFlow{
					Name:    flow.Name,
					Path:    path,
					Origin:  OriginSymskills,
					Version: flow.Version,
					Steps:   len(flow.Steps),
				})
			}
		}
	}
	return found, nil
}

// resolveSymskillsBinary locates the symskills executable.
func resolveSymskillsBinary(override string) string {
	if override != "" {
		return override
	}
	if path, err := exec.LookPath("symskills"); err == nil {
		return path
	}
	return ""
}

// mergeFound merges discovered flows; the higher-precedence origin wins for
// duplicate names (project > global > symskills).
func mergeFound(target map[string]FoundFlow, found []FoundFlow) {
	for _, flow := range found {
		existing, ok := target[flow.Name]
		if !ok || originRank(flow.Origin) < originRank(existing.Origin) {
			target[flow.Name] = flow
		}
	}
}

func originRank(origin Origin) int {
	switch origin {
	case OriginProject:
		return 0
	case OriginGlobal:
		return 1
	default:
		return 2
	}
}
