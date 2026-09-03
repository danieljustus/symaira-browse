package mcp

import (
	"encoding/json"
	"testing"
)

func TestFetchSecurityControlsAreExposedAndForwarded(t *testing.T) {
	for _, name := range []string{"fetch_url", "fetch_batch"} {
		var tool *ProxyTool
		for i := range tools {
			if tools[i].Name == name {
				tool = &tools[i]
				break
			}
		}
		if tool == nil {
			t.Fatalf("tool %q is not registered", name)
		}
		properties, ok := tool.Schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q has no properties schema", name)
		}
		for _, property := range []string{"no_injection_scan", "injection_patterns", "content_boundaries"} {
			if _, ok := properties[property]; !ok {
				t.Errorf("tool %q schema is missing %q", name, property)
			}
		}

		input := map[string]any{
			"url":                "https://example.com",
			"urls":               []any{"https://example.com"},
			"no_injection_scan":  true,
			"injection_patterns": "/tmp/patterns.txt",
			"content_boundaries": false,
		}
		args, err := tool.Args(input)
		if err != nil {
			t.Fatalf("tool %q args: %v", name, err)
		}
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("tool %q marshal args: %v", name, err)
		}
		var forwarded map[string]any
		if err := json.Unmarshal(raw, &forwarded); err != nil {
			t.Fatalf("tool %q forwarded args: %v", name, err)
		}
		if forwarded["no_injection_scan"] != true || forwarded["injection_patterns"] != "/tmp/patterns.txt" || forwarded["content_boundaries"] != false {
			t.Fatalf("tool %q forwarded args = %#v", name, forwarded)
		}
	}
}
