package config

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
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

// ShowOutputFor builds the stable machine-readable payload for config show.
func ShowOutputFor(result Result) ShowOutput {
	return ShowOutput{Fields: showFields(result)}
}

func showFields(result Result) map[string]Field {
	cfg := result.Config
	return map[string]Field{
		"allow_private":     {Value: strconv.FormatBool(cfg.AllowPrivate), Source: result.Sources["allow_private"]},
		"allowed_domains":   {Value: strings.Join(cfg.AllowedDomains, ","), Source: result.Sources["allowed_domains"]},
		"approval_timeout":  {Value: strconv.Itoa(cfg.ApprovalTimeoutSeconds), Source: result.Sources["approval_timeout"]},
		"autosave":          {Value: cfg.AutosavePolicy, Source: result.Sources["autosave"]},
		"autosave_interval": {Value: strconv.Itoa(cfg.AutosaveIntervalSeconds), Source: result.Sources["autosave_interval"]},
		"autosave_key":      {Value: cfg.AutosaveKey, Source: result.Sources["autosave_key"]},
		"cache_dir":         {Value: cfg.CacheDir, Source: result.Sources["cache_dir"]},
		"cache_ttl_hours":   {Value: strconv.Itoa(cfg.CacheTTLHours), Source: result.Sources["cache_ttl_hours"]},
		"cdp_endpoint":      {Value: cfg.CDPEndpoint, Source: result.Sources["cdp_endpoint"]},
		"config_dir":        {Value: cfg.ConfigDir, Source: result.Sources["config_dir"]},
		"daemon_log":        {Value: cfg.DaemonLogPath, Source: result.Sources["daemon_log"]},
		"executable_path":   {Value: cfg.ExecutablePath, Source: result.Sources["executable_path"]},
		"headless":          {Value: strconv.FormatBool(cfg.Headless), Source: result.Sources["headless"]},
		"idle_timeout":      {Value: strconv.Itoa(cfg.IdleTimeoutSeconds), Source: result.Sources["idle_timeout"]},
		"log_format":        {Value: cfg.LogFormat, Source: result.Sources["log_format"]},
		"log_level":         {Value: cfg.LogLevel, Source: result.Sources["log_level"]},
		"operation_timeout": {Value: strconv.Itoa(cfg.OperationTimeoutSeconds), Source: result.Sources["operation_timeout"]},
		"read_timeout":      {Value: strconv.Itoa(cfg.ReadTimeoutSeconds), Source: result.Sources["read_timeout"]},
		"ssrf_enabled":      {Value: strconv.FormatBool(cfg.SSRFEnabled), Source: result.Sources["ssrf_enabled"]},
		"state_dir":         {Value: cfg.StateDir, Source: result.Sources["state_dir"]},
		"state_expire_days": {Value: strconv.Itoa(cfg.StateExpireDays), Source: result.Sources["state_expire_days"]},
		"upload_dirs":       {Value: strings.Join(cfg.UploadDirs, ","), Source: result.Sources["upload_dirs"]},
	}
}

// WriteShow writes the effective configuration to w. Human output is concise
// and line-oriented; JSON output has a stable top-level field schema.
func WriteShow(w io.Writer, result Result, jsonOutput bool) error {
	fields := showFields(result)
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
