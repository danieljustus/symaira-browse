// Package config resolves symbrowse's complete configuration surface.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-corekit/exitcodes"
)

const (
	appName   = "symbrowse"
	envPrefix = "SYMBROWSE"
)

// Config is the effective symbrowse configuration. Fields use JSON tags for
// the corresponding config.toml keys and SYMBROWSE_* environment names.
type Config struct {
	LogLevel                string   `json:"log_level"`
	LogFormat               string   `json:"log_format"`
	ConfigDir               string   `json:"config_dir"`
	CacheDir                string   `json:"cache_dir"`
	StateDir                string   `json:"state_dir"`
	ExecutablePath          string   `json:"executable_path"`
	CDPEndpoint             string   `json:"cdp_endpoint"`
	AllowedDomains          []string `json:"allowed_domains"`
	SSRFEnabled             bool     `json:"ssrf_enabled"`
	AllowPrivate            bool     `json:"allow_private"`
	Headless                bool     `json:"headless"`
	CacheTTLHours           int      `json:"cache_ttl_hours"`
	IdleTimeoutSeconds      int      `json:"idle_timeout"`
	OperationTimeoutSeconds int      `json:"operation_timeout"`
	ReadTimeoutSeconds      int      `json:"read_timeout"`
	StateExpireDays         int      `json:"state_expire_days"`
	AutosavePolicy          string   `json:"autosave"`
	AutosaveIntervalSeconds int      `json:"autosave_interval"`
	AutosaveKey             string   `json:"autosave_key"`
	UploadDirs              []string `json:"upload_dirs"`
	DaemonLogPath           string   `json:"daemon_log"`
	ApprovalTimeoutSeconds  int      `json:"approval_timeout"`
}

// Paths contains the XDG directories used by symbrowse.
type Paths struct {
	ConfigDir string `json:"config_dir"`
	CacheDir  string `json:"cache_dir"`
	StateDir  string `json:"state_dir"`
}

// FlagOverrides contains only explicitly supplied command-line values. A nil
// field means that the corresponding flag was not supplied.
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

var configFields = []string{
	"log_level", "log_format", "config_dir", "cache_dir", "state_dir",
	"executable_path", "cdp_endpoint", "allowed_domains", "ssrf_enabled",
	"allow_private", "headless", "cache_ttl_hours", "idle_timeout",
	"operation_timeout", "read_timeout", "state_expire_days", "autosave",
	"autosave_interval", "autosave_key", "upload_dirs", "daemon_log",
	"approval_timeout",
}

// Defaults returns the built-in configuration defaults.
func Defaults() *Config {
	paths, err := DefaultPaths()
	if err != nil {
		return &Config{
			LogLevel:                "warn",
			LogFormat:               "text",
			AutosavePolicy:          "auto",
			IdleTimeoutSeconds:      30 * 60,
			OperationTimeoutSeconds: 25,
			ReadTimeoutSeconds:      30,
			StateExpireDays:         30,
			AutosaveIntervalSeconds: 30,
			ApprovalTimeoutSeconds:  60,
		}
	}
	var uploadDirs []string
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		uploadDirs = []string{cwd}
	}
	return &Config{
		LogLevel:                "warn",
		LogFormat:               "text",
		ConfigDir:               paths.ConfigDir,
		CacheDir:                paths.CacheDir,
		StateDir:                paths.StateDir,
		CacheTTLHours:           24,
		IdleTimeoutSeconds:      30 * 60,
		OperationTimeoutSeconds: 25,
		ReadTimeoutSeconds:      30,
		StateExpireDays:         30,
		AutosavePolicy:          "auto",
		AutosaveIntervalSeconds: 30,
		UploadDirs:              uploadDirs,
		DaemonLogPath:           filepath.Join(paths.StateDir, "daemon.log"),
		ApprovalTimeoutSeconds:  60,
	}
}

// DefaultPaths returns the XDG config, cache, and state directories. When an
// XDG variable is unset, the existing platform-independent defaults are kept.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("cannot determine home directory: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(home, ".cache")
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	return Paths{
		ConfigDir: filepath.Join(configHome, appName),
		CacheDir:  filepath.Join(cacheHome, appName),
		StateDir:  filepath.Join(stateHome, appName),
	}, nil
}

// DefaultDaemonLogPath returns the default detached-daemon log path. The
// explicit SYMBROWSE_DAEMON_LOG setting is handled here so callers do not
// re-derive state paths independently.
func DefaultDaemonLogPath() string {
	if path := os.Getenv("SYMBROWSE_DAEMON_LOG"); path != "" {
		return path
	}
	if stateDir := os.Getenv("SYMBROWSE_STATE_DIR"); stateDir != "" {
		return filepath.Join(stateDir, "daemon.log")
	}
	if paths, err := DefaultPaths(); err == nil {
		return filepath.Join(paths.StateDir, "daemon.log")
	}
	return filepath.Join(os.TempDir(), appName, "daemon.log")
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
	cfg := Defaults()
	sources := make(map[string]string, len(configFields))
	for _, field := range configFields {
		sources[field] = "default"
	}

	paths, err := DefaultPaths()
	if err != nil {
		return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to determine configuration home")
	}
	globalPath := filepath.Join(paths.ConfigDir, "config.toml")
	cwd, err := os.Getwd()
	if err != nil {
		return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig,
			"failed to determine project directory")
	}
	projectPath := filepath.Join(cwd, "."+appName+".toml")
	if err := applyFile(cfg, sources, globalPath, "global"); err != nil {
		return configError(err, "failed to load global configuration")
	}
	if err := applyFile(cfg, sources, projectPath, "project"); err != nil {
		return configError(err, "failed to load project configuration")
	}
	if err := applyEnv(cfg, sources); err != nil {
		return configError(err, "failed to load environment configuration")
	}

	applyStringOverride(&cfg.LogLevel, overrides.LogLevel, sources, "log_level")
	applyStringOverride(&cfg.LogFormat, overrides.LogFormat, sources, "log_format")
	applyStringOverride(&cfg.ConfigDir, overrides.ConfigDir, sources, "config_dir")
	applyStringOverride(&cfg.CacheDir, overrides.CacheDir, sources, "cache_dir")
	applyStringOverride(&cfg.StateDir, overrides.StateDir, sources, "state_dir")
	applyStringOverride(&cfg.ExecutablePath, overrides.ExecutablePath, sources, "executable_path")
	cfg.DaemonLogPath = effectiveDaemonLogPath(cfg, sources)
	if err := validate(cfg); err != nil {
		return configError(err, "invalid configuration")
	}
	return Result{Config: *cfg, Sources: sources}, nil
}

func configError(err error, message string) (Result, error) {
	return Result{}, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, message)
}

type fileConfig struct {
	LogLevel                *string   `json:"log_level"`
	LogFormat               *string   `json:"log_format"`
	ConfigDir               *string   `json:"config_dir"`
	CacheDir                *string   `json:"cache_dir"`
	StateDir                *string   `json:"state_dir"`
	ExecutablePath          *string   `json:"executable_path"`
	CDPEndpoint             *string   `json:"cdp_endpoint"`
	AllowedDomains          *[]string `json:"allowed_domains"`
	SSRFEnabled             *bool     `json:"ssrf_enabled"`
	AllowPrivate            *bool     `json:"allow_private"`
	Headless                *bool     `json:"headless"`
	CacheTTLHours           *int      `json:"cache_ttl_hours"`
	IdleTimeoutSeconds      *int      `json:"idle_timeout"`
	OperationTimeoutSeconds *int      `json:"operation_timeout"`
	ReadTimeoutSeconds      *int      `json:"read_timeout"`
	StateExpireDays         *int      `json:"state_expire_days"`
	AutosavePolicy          *string   `json:"autosave"`
	AutosaveIntervalSeconds *int      `json:"autosave_interval"`
	AutosaveKey             *string   `json:"autosave_key"`
	UploadDirs              *[]string `json:"upload_dirs"`
	DaemonLogPath           *string   `json:"daemon_log"`
	ApprovalTimeoutSeconds  *int      `json:"approval_timeout"`
}

func applyFile(cfg *Config, sources map[string]string, path, source string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var raw map[string]interface{}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var patch fileConfig
	if err := json.Unmarshal(encoded, &patch); err != nil {
		return err
	}
	applyFileConfig(cfg, patch)
	for _, field := range configFields {
		if _, ok := raw[field]; ok {
			sources[field] = source
		}
	}
	return nil
}

func applyFileConfig(cfg *Config, patch fileConfig) {
	if patch.LogLevel != nil {
		cfg.LogLevel = *patch.LogLevel
	}
	if patch.LogFormat != nil {
		cfg.LogFormat = *patch.LogFormat
	}
	if patch.ConfigDir != nil {
		cfg.ConfigDir = *patch.ConfigDir
	}
	if patch.CacheDir != nil {
		cfg.CacheDir = *patch.CacheDir
	}
	if patch.StateDir != nil {
		cfg.StateDir = *patch.StateDir
	}
	if patch.ExecutablePath != nil {
		cfg.ExecutablePath = *patch.ExecutablePath
	}
	if patch.CDPEndpoint != nil {
		cfg.CDPEndpoint = *patch.CDPEndpoint
	}
	if patch.AllowedDomains != nil {
		cfg.AllowedDomains = *patch.AllowedDomains
	}
	if patch.SSRFEnabled != nil {
		cfg.SSRFEnabled = *patch.SSRFEnabled
	}
	if patch.AllowPrivate != nil {
		cfg.AllowPrivate = *patch.AllowPrivate
	}
	if patch.Headless != nil {
		cfg.Headless = *patch.Headless
	}
	if patch.CacheTTLHours != nil {
		cfg.CacheTTLHours = *patch.CacheTTLHours
	}
	if patch.IdleTimeoutSeconds != nil {
		cfg.IdleTimeoutSeconds = *patch.IdleTimeoutSeconds
	}
	if patch.OperationTimeoutSeconds != nil {
		cfg.OperationTimeoutSeconds = *patch.OperationTimeoutSeconds
	}
	if patch.ReadTimeoutSeconds != nil {
		cfg.ReadTimeoutSeconds = *patch.ReadTimeoutSeconds
	}
	if patch.StateExpireDays != nil {
		cfg.StateExpireDays = *patch.StateExpireDays
	}
	if patch.AutosavePolicy != nil {
		cfg.AutosavePolicy = *patch.AutosavePolicy
	}
	if patch.AutosaveIntervalSeconds != nil {
		cfg.AutosaveIntervalSeconds = *patch.AutosaveIntervalSeconds
	}
	if patch.AutosaveKey != nil {
		cfg.AutosaveKey = *patch.AutosaveKey
	}
	if patch.UploadDirs != nil {
		cfg.UploadDirs = *patch.UploadDirs
	}
	if patch.DaemonLogPath != nil {
		cfg.DaemonLogPath = *patch.DaemonLogPath
	}
	if patch.ApprovalTimeoutSeconds != nil {
		cfg.ApprovalTimeoutSeconds = *patch.ApprovalTimeoutSeconds
	}
}

func applyEnv(cfg *Config, sources map[string]string) error {
	for _, field := range configFields {
		envName := envName(field)
		if raw, ok := os.LookupEnv(envName); ok && raw != "" {
			sources[field] = "env"
		}
	}
	var err error
	applyStringEnv := func(dst *string, field string) {
		if err == nil {
			if raw := os.Getenv(envName(field)); raw != "" {
				*dst = raw
			}
		}
	}
	applyStringEnv(&cfg.LogLevel, "log_level")
	applyStringEnv(&cfg.LogFormat, "log_format")
	applyStringEnv(&cfg.ConfigDir, "config_dir")
	applyStringEnv(&cfg.CacheDir, "cache_dir")
	applyStringEnv(&cfg.StateDir, "state_dir")
	applyStringEnv(&cfg.ExecutablePath, "executable_path")
	applyStringEnv(&cfg.CDPEndpoint, "cdp_endpoint")
	applyStringEnv(&cfg.AutosavePolicy, "autosave")
	applyStringEnv(&cfg.AutosaveKey, "autosave_key")
	applyStringEnv(&cfg.DaemonLogPath, "daemon_log")
	if err = applyBoolEnv(&cfg.SSRFEnabled, "ssrf_enabled"); err != nil {
		return err
	}
	if err = applyBoolEnv(&cfg.AllowPrivate, "allow_private"); err != nil {
		return err
	}
	if err = applyBoolEnv(&cfg.Headless, "headless"); err != nil {
		return err
	}
	if err = applyIntEnv(&cfg.CacheTTLHours, "cache_ttl_hours", func(int) bool { return true }); err != nil {
		return err
	}
	if err = applyIntEnv(&cfg.IdleTimeoutSeconds, "idle_timeout", func(v int) bool { return v >= 0 }); err != nil {
		return err
	}
	if err = applyIntEnv(&cfg.OperationTimeoutSeconds, "operation_timeout", func(v int) bool { return v > 0 }); err != nil {
		return err
	}
	if err = applyIntEnv(&cfg.ReadTimeoutSeconds, "read_timeout", func(v int) bool { return v > 0 }); err != nil {
		return err
	}
	if err = applyIntEnv(&cfg.StateExpireDays, "state_expire_days", func(v int) bool { return v >= 0 }); err != nil {
		return err
	}
	if err = applyIntEnv(&cfg.AutosaveIntervalSeconds, "autosave_interval", func(v int) bool { return v >= 0 }); err != nil {
		return err
	}
	if err = applyIntEnv(&cfg.ApprovalTimeoutSeconds, "approval_timeout", func(v int) bool { return v > 0 }); err != nil {
		return err
	}
	if raw := os.Getenv(envName("allowed_domains")); raw != "" {
		cfg.AllowedDomains = splitList(raw)
	}
	if raw := os.Getenv(envName("upload_dirs")); raw != "" {
		cfg.UploadDirs = splitList(raw)
	}
	return nil
}

func applyBoolEnv(dst *bool, field string) error {
	raw := os.Getenv(envName(field))
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", envName(field), raw, err)
	}
	*dst = value
	return nil
}

func applyIntEnv(dst *int, field string, valid func(int) bool) error {
	raw := os.Getenv(envName(field))
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || !valid(value) {
		return fmt.Errorf("invalid %s %q", envName(field), raw)
	}
	*dst = value
	return nil
}

func splitList(raw string) []string {
	var values []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}

func envName(field string) string {
	// A few configuration keys intentionally retain their historical env names.
	keys := map[string]string{
		"idle_timeout": "IDLE_TIMEOUT", "operation_timeout": "OPERATION_TIMEOUT",
		"read_timeout": "READ_TIMEOUT", "autosave": "AUTOSAVE",
		"autosave_interval": "AUTOSAVE_INTERVAL", "autosave_key": "AUTOSAVE_KEY",
		"upload_dirs": "UPLOAD_DIRS", "daemon_log": "DAEMON_LOG",
		"approval_timeout": "APPROVAL_TIMEOUT", "state_expire_days": "STATE_EXPIRE_DAYS",
		"ssrf_enabled": "SSRF",
	}
	if key, ok := keys[field]; ok {
		return envPrefix + "_" + key
	}
	return envPrefix + "_" + strings.ToUpper(field)
}

func effectiveDaemonLogPath(cfg *Config, sources map[string]string) string {
	if sources["daemon_log"] != "default" {
		return cfg.DaemonLogPath
	}
	return filepath.Join(cfg.StateDir, "daemon.log")
}

func validate(cfg *Config) error {
	if cfg.IdleTimeoutSeconds < 0 || cfg.OperationTimeoutSeconds <= 0 || cfg.ReadTimeoutSeconds <= 0 || cfg.StateExpireDays < 0 || cfg.AutosaveIntervalSeconds < 0 || cfg.ApprovalTimeoutSeconds <= 0 {
		return fmt.Errorf("timeout and retention values must be non-negative, with operation, read, and approval timeouts positive")
	}
	if cfg.AutosavePolicy != "auto" && cfg.AutosavePolicy != "always" && cfg.AutosavePolicy != "never" {
		return fmt.Errorf("invalid autosave policy %q", cfg.AutosavePolicy)
	}
	return nil
}

func applyStringOverride(dst *string, value *string, sources map[string]string, field string) {
	if value == nil {
		return
	}
	*dst = *value
	sources[field] = "flag"
}
