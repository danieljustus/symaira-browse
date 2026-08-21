package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/fetch/agentdom"
)

// SchemaMiss indicates the query was syntactically valid but found no matching data.
// Callers may treat this as a non-fatal condition (warning + empty result).
type SchemaMiss struct {
	Path string
	Msg  string
}

func (e *SchemaMiss) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("schema query %q: no match", e.Path)
}

// QuerySchema queries JSON-LD data islands for a given schema path.
//
// Two syntax modes are supported:
//
//	Typed selector:  @Type:path   — e.g. @Recipe:name, @Product:aggregateRating.ratingValue
//	Plain field path: name, headline, aggregateRating.ratingValue, @type
//
// Typed selectors find a JSON-LD island whose @type matches Type and traverse path.
// Plain field paths search all ld+json islands (including @graph nodes) for the field.
// For @type, the collected type values are returned.
func QuerySchema(islands []agentdom.DataIsland, schemaPath string) (string, error) {
	if schemaPath == "" {
		return "", fmt.Errorf("schema path must not be empty")
	}

	// Typed selector: @Type:path (colon separates type from field path)
	if strings.HasPrefix(schemaPath, "@") {
		rest := schemaPath[1:]
		if idx := strings.Index(rest, ":"); idx > 0 {
			schemaType := rest[:idx]
			fieldPath := rest[idx+1:]
			if schemaType != "" && fieldPath != "" {
				return queryTypedSelector(islands, schemaType, fieldPath)
			}
		}
		// @type without colon → plain path for @type field
		if schemaPath == "@type" {
			return queryTypeField(islands)
		}
		// Other @ without colon → still an error (ambiguous typed selector)
		return "", fmt.Errorf("schema path must have format @Type:path (e.g., @Recipe:name) or be a plain field path (e.g., name)")
	}

	// Reject paths with colon but no @ prefix (likely mistyped typed selector)
	if strings.Contains(schemaPath, ":") {
		return "", fmt.Errorf("schema path must start with @, e.g., @Recipe:name")
	}

	// Plain field path: name, headline, aggregateRating.ratingValue
	return queryPlainPath(islands, schemaPath)
}

// queryTypedSelector finds the JSON-LD island matching schemaType and traverses fieldPath.
func queryTypedSelector(islands []agentdom.DataIsland, schemaType, fieldPath string) (string, error) {
	for _, island := range islands {
		if island.Source != "ld+json" {
			continue
		}
		data, err := unmarshalIsland(island.JSON)
		if err != nil {
			continue
		}
		if !matchesType(data, schemaType) {
			continue
		}
		// Find the specific node with this type (root or within @graph)
		nodeJSON := findTypedNode(island.JSON, schemaType)
		if nodeJSON == nil {
			continue
		}
		return traversePath(nodeJSON, fieldPath)
	}
	return "", &SchemaMiss{Path: schemaType + ":" + fieldPath, Msg: fmt.Sprintf("no JSON-LD with @type %q found", schemaType)}
}

// queryTypeField collects @type values from all ld+json islands.
func queryTypeField(islands []agentdom.DataIsland) (string, error) {
	var types []string
	for _, island := range islands {
		if island.Source != "ld+json" {
			continue
		}
		data, err := unmarshalIsland(island.JSON)
		if err != nil {
			continue
		}
		// Check root @type
		if t, ok := data["@type"].(string); ok && t != "" {
			types = append(types, t)
			continue
		}
		if t, ok := data["type"].(string); ok && t != "" {
			types = append(types, t)
			continue
		}
		// Check @graph
		if graph, ok := data["@graph"].([]interface{}); ok {
			for _, item := range graph {
				if m, ok := item.(map[string]interface{}); ok {
					if t, ok := m["@type"].(string); ok && t != "" {
						types = append(types, t)
					}
				}
			}
		}
	}

	if len(types) == 0 {
		return "", &SchemaMiss{Path: "@type", Msg: "@type not found in any JSON-LD island"}
	}

	if len(types) == 1 {
		b, _ := json.Marshal(types[0])
		return string(b), nil
	}

	b, err := json.Marshal(types)
	if err != nil {
		return "", fmt.Errorf("failed to marshal @type values: %w", err)
	}
	return string(b), nil
}

// queryPlainPath searches all ld+json islands (including @graph nodes) for fieldPath.
func queryPlainPath(islands []agentdom.DataIsland, fieldPath string) (string, error) {
	var results []string
	for _, island := range islands {
		if island.Source != "ld+json" {
			continue
		}
		data, err := unmarshalIsland(island.JSON)
		if err != nil {
			continue
		}
		// Try root first
		result, err := traverseFieldMap(data, fieldPath)
		if err == nil && result != "" {
			results = append(results, result)
			continue
		}
		// Try @graph nodes
		if graph, ok := data["@graph"].([]interface{}); ok {
			for _, item := range graph {
				if m, ok := item.(map[string]interface{}); ok {
					result, err := traverseFieldMap(m, fieldPath)
					if err == nil && result != "" {
						results = append(results, result)
					}
				}
			}
		}
	}

	if len(results) == 0 {
		return "", &SchemaMiss{Path: fieldPath, Msg: fmt.Sprintf("field %q not found in any JSON-LD island", fieldPath)}
	}

	if len(results) == 1 {
		return results[0], nil
	}

	b, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("failed to marshal results: %w", err)
	}
	return string(b), nil
}

// findTypedNode returns the raw JSON of the node matching schemaType.
// It checks the root object first, then searches inside @graph arrays.
func findTypedNode(data json.RawMessage, schemaType string) json.RawMessage {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	// Check root @type or type field
	if t, ok := raw["@type"].(string); ok && strings.EqualFold(t, schemaType) {
		return data
	}
	if t, ok := raw["type"].(string); ok && strings.EqualFold(t, schemaType) {
		return data
	}

	// Search @graph array
	if graph, ok := raw["@graph"].([]interface{}); ok {
		for _, item := range graph {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["@type"].(string); ok && strings.EqualFold(t, schemaType) {
					b, err := json.Marshal(m)
					if err != nil {
						return nil
					}
					return b
				}
			}
		}
	}

	return nil
}

func unmarshalIsland(data json.RawMessage) (map[string]interface{}, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func matchesType(data map[string]interface{}, schemaType string) bool {
	if t, ok := data["@type"].(string); ok {
		if strings.EqualFold(t, schemaType) {
			return true
		}
	}

	if t, ok := data["type"].(string); ok {
		if strings.EqualFold(t, schemaType) {
			return true
		}
	}

	if graph, ok := data["@graph"].([]interface{}); ok {
		for _, item := range graph {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["@type"].(string); ok {
					if strings.EqualFold(t, schemaType) {
						return true
					}
				}
			}
		}
	}

	return false
}

// traversePath unmarshals data and traverses a dot-separated field path.
func traversePath(data json.RawMessage, fieldPath string) (string, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("failed to parse JSON-LD: %w", err)
	}
	return traverseFieldMap(raw, fieldPath)
}

// traverseFieldMap walks a dot-separated field path through a map.
func traverseFieldMap(data map[string]interface{}, fieldPath string) (string, error) {
	parts := strings.Split(fieldPath, ".")
	var current interface{} = data

	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			val, ok := v[part]
			if !ok {
				return "", fmt.Errorf("field %q not found", part)
			}
			current = val
		case []interface{}:
			return "", fmt.Errorf("cannot traverse into array with field %q", part)
		default:
			return "", fmt.Errorf("cannot traverse into %T", current)
		}
	}

	result, err := json.Marshal(current)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}

	return string(result), nil
}
