// Package config wires symbrowse's TOML configuration to symaira-corekit.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-corekit/configkit"
	"github.com/danieljustus/symaira-corekit/exitcodes"
)

const (
	appName   = "symbrowse"
	envPrefix = "SYMBROWSE"
)

// Config is the effective symbrowse configuration. All fields use JSON tags
// because configkit uses the same tags for TOML and environment lookups.
type Config struct {
	LogLevel       string `json:"log_level"`
	LogFormat      string `json:"log_format"`
	ConfigDir      string `json:"config_dir"`
	CacheDir       string `json:"cache_dir"`
	StateDir       string `json:"state_dir"`
	ExecutablePath string `json:"executable_path"`
	// AllowedDomains activates the domain allowlist network policy. Patterns
	// are bare hostnames, optionally prefixed with "*." (see internal/policy).
	AllowedDomains []string `json:"allowed_domains"`
}

// Paths contains the default XDG directories used by symbrowse.
type Paths struct {
	ConfigDir string `json:"config_dir"`
	CacheDir  string `json:"cache_dir"`
	StateDir  string `json:"state_dir"`
}

// FlagOverrides contains only explicitly supplied command-line values. A nil
// field means that the corresponding flag was not supplied and must not
// override a higher-level configuration source.
type FlagOverrides struct {
	LogLevel       *string
	LogFormat      *string
	ConfigDir      *string
	CacheDir       *string
	StateDir       *string
	ExecutablePath *string
}

// Result contains the effective configuration and the source selected for each
// field. Source values are stable labels: default, global, project, env, flag.
type Result struct {
	Config  Config
	Sources map[string]string
}

// Defaults returns the built-in configuration defaults.
func Defaults() *Config {
	paths, err := DefaultPaths()
	if err != nil {
		return &Config{LogLevel: "warn", LogFormat: "text"}
	}
	return &Config{
		LogLevel:       "warn",
		LogFormat:      "text",
		ConfigDir:      paths.ConfigDir,
		CacheDir:       paths.CacheDir,
		StateDir:       paths.StateDir,
		ExecutablePath: "",
	}
}

// DefaultPaths returns the XDG-style config, cache, and state directories.
// The defaults intentionally follow the repository contract:
// ~/.config/symbrowse, ~/.cache/symbrowse, and ~/.local/state/symbrowse.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return Paths{
		ConfigDir: filepath.Join(home, ".config", appName),
		CacheDir:  filepath.Join(home, ".cache", appName),
		StateDir:  filepath.Join(home, ".local", "state", appName),
	}, nil
}

// NewLoader creates a fresh corekit-backed loader. A fresh loader per command
// keeps CLI invocations and tests isolated while retaining corekit's cache API
// for callers that need Reload or ResetCache.
func NewLoader() *configkit.Loader[Config] {
	return configkit.NewLoader[Config](configkit.Options{
		AppName:    appName,
		EnvPrefix:  envPrefix,
		ConfigName: appName,
	}, Defaults)
}

// Load returns the effective configuration without command-line overrides.
func Load() (*Config, error) {
	result, err := LoadWithOverrides(FlagOverrides{})
	if err != nil {
		return nil, err
	}
	return &result.Config, nil
}

// LoadWithOverrides applies the complete precedence chain:
// defaults < global TOML < project TOML < SYMBROWSE_* < flags.
func LoadWithOverrides(overrides FlagOverrides) (Result, error) {
	cfg, err := NewLoader().Load()
	if err != nil {
		return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to load configuration")
	}

	sources := map[string]string{
		"log_level":       "default",
		"log_format":      "default",
		"config_dir":      "default",
		"cache_dir":       "default",
		"state_dir":       "default",
		"executable_path": "default",
		"allowed_domains": "default",
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to determine configuration home")
	}
	globalPath := filepath.Join(home, ".config", appName, "config.toml")
	cwd, err := os.Getwd()
	if err != nil {
		return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to determine project directory")
	}
	projectPath := filepath.Join(cwd, "."+appName+".toml")

	if err := markFileSources(sources, globalPath, "global"); err != nil {
		return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to inspect global configuration")
	}
	if err := markFileSources(sources, projectPath, "project"); err != nil {
		return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to inspect project configuration")
	}
	markEnvSources(sources)

	applyOverride(&cfg.LogLevel, overrides.LogLevel, sources, "log_level")
	applyOverride(&cfg.LogFormat, overrides.LogFormat, sources, "log_format")
	applyOverride(&cfg.ConfigDir, overrides.ConfigDir, sources, "config_dir")
	applyOverride(&cfg.CacheDir, overrides.CacheDir, sources, "cache_dir")
	applyOverride(&cfg.StateDir, overrides.StateDir, sources, "state_dir")
	applyOverride(&cfg.ExecutablePath, overrides.ExecutablePath, sources, "executable_path")

	return Result{Config: *cfg, Sources: sources}, nil
}

func markFileSources(sources map[string]string, path, source string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}

	var raw map[string]interface{}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return err
	}
	for _, field := range []string{"log_level", "log_format", "config_dir", "cache_dir", "state_dir", "executable_path", "allowed_domains"} {
		if _, ok := raw[field]; ok {
			sources[field] = source
		}
	}
	return nil
}

func markEnvSources(sources map[string]string) {
	for _, field := range []string{"log_level", "log_format", "config_dir", "cache_dir", "state_dir", "executable_path", "allowed_domains"} {
		if value, ok := os.LookupEnv(envPrefix + "_" + envKey(field)); ok && value != "" {
			sources[field] = "env"
		}
	}
}

func envKey(field string) string {
	result := make([]byte, 0, len(field))
	for i := 0; i < len(field); i++ {
		if field[i] == '_' {
			result = append(result, '_')
			continue
		}
		if field[i] >= 'a' && field[i] <= 'z' {
			result = append(result, field[i]-('a'-'A'))
		} else {
			result = append(result, field[i])
		}
	}
	return string(result)
}

func applyOverride(dst *string, value *string, sources map[string]string, field string) {
	if value == nil {
		return
	}
	*dst = *value
	sources[field] = "flag"
}
