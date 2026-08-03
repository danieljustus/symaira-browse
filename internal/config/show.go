package config

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Field is one effective configuration value and the source that supplied it.
type Field struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ShowOutput is the stable machine-readable payload for config show --json.
type ShowOutput struct {
	Fields map[string]Field `json:"fields"`
}

// WriteShow writes the effective configuration to w. Human output is concise
// and line-oriented; JSON output has a stable top-level field schema.
func WriteShow(w io.Writer, result Result, jsonOutput bool) error {
	fields := map[string]Field{
		"cache_dir":  {Value: result.Config.CacheDir, Source: result.Sources["cache_dir"]},
		"config_dir": {Value: result.Config.ConfigDir, Source: result.Sources["config_dir"]},
		"log_format": {Value: result.Config.LogFormat, Source: result.Sources["log_format"]},
		"log_level":  {Value: result.Config.LogLevel, Source: result.Sources["log_level"]},
		"state_dir":  {Value: result.Config.StateDir, Source: result.Sources["state_dir"]},
	}
	if jsonOutput {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(ShowOutput{Fields: fields})
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field := fields[key]
		if _, err := fmt.Fprintf(w, "%s=%s (source: %s)\n", key, field.Value, field.Source); err != nil {
			return err
		}
	}
	return nil
}
