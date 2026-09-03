package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWithOverridesPrecedenceAndSources(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SYMBROWSE_LOG_LEVEL", "debug")
	t.Setenv("SYMBROWSE_LOG_FORMAT", "text")
	t.Setenv("SYMBROWSE_CONFIG_DIR", filepath.Join(home, "env-config"))
	t.Setenv("SYMBROWSE_CACHE_DIR", "")
	t.Setenv("SYMBROWSE_STATE_DIR", filepath.Join(home, "env-state"))
	t.Chdir(project)

	global := filepath.Join(home, ".config", "symbrowse", "config.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("log_level = \"info\"\nlog_format = \"text\"\nconfig_dir = \"global-config\"\ncache_dir = \"global-cache\"\nstate_dir = \"global-state\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".symbrowse.toml"), []byte("log_level = \"warn\"\nlog_format = \"json\"\nconfig_dir = \"project-config\"\ncache_dir = \"project-cache\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	flagLogLevel := "error"
	result, err := LoadWithOverrides(FlagOverrides{LogLevel: &flagLogLevel})
	if err != nil {
		t.Fatal(err)
	}

	if result.Config.LogLevel != "error" || result.Sources["log_level"] != "flag" {
		t.Fatalf("log level = %#v, want flag/error", result)
	}
	if result.Config.LogFormat != "text" || result.Sources["log_format"] != "env" {
		t.Fatalf("log format = %#v, want env/text", result)
	}
	if result.Config.ConfigDir != filepath.Join(home, "env-config") || result.Sources["config_dir"] != "env" {
		t.Fatalf("config dir = %#v, want env", result)
	}
	if result.Config.CacheDir != "project-cache" || result.Sources["cache_dir"] != "project" {
		t.Fatalf("cache dir = %#v, want project", result)
	}
	if result.Config.StateDir != filepath.Join(home, "env-state") || result.Sources["state_dir"] != "env" {
		t.Fatalf("state dir = %#v, want env", result)
	}
}

func TestWriteShowJSONAndHumanOutput(t *testing.T) {
	result := Result{Config: Config{LogLevel: "info", LogFormat: "json", ConfigDir: "/config", CacheDir: "/cache", StateDir: "/state"}, Sources: map[string]string{
		"log_level": "default", "log_format": "project", "config_dir": "env", "cache_dir": "flag", "state_dir": "global",
	}}
	var jsonOutput []byte
	var humanOutput []byte
	var jsonBuffer, humanBuffer testBuffer
	if err := WriteShow(&jsonBuffer, result, true); err != nil {
		t.Fatal(err)
	}
	if err := WriteShow(&humanBuffer, result, false); err != nil {
		t.Fatal(err)
	}
	jsonOutput, humanOutput = jsonBuffer.bytes, humanBuffer.bytes
	if len(jsonOutput) == 0 || string(jsonOutput) == "{}\n" {
		t.Fatalf("JSON output = %q", jsonOutput)
	}
	if got := string(humanOutput); got == "" || !contains(got, "log_level=info (source: default)") {
		t.Fatalf("human output = %q", got)
	}
}

func TestFetchSettingsFollowTOMLAndEnvironment(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("SYMBROWSE_FETCH_ROBOTS", "")
	t.Setenv("SYMBROWSE_FETCH_USER_AGENT", "env-agent/2.0")
	t.Setenv("SYMBROWSE_FETCH_NO_CACHE", "")
	t.Setenv("SYMBROWSE_CACHE_TTL_HOURS", "7")
	if err := os.MkdirAll(filepath.Join(home, "config", "symbrowse"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "symbrowse", "config.toml"), []byte("fetch_robots = false\nfetch_user_agent = \"toml-agent/1.0\"\nfetch_no_cache = true\ncache_ttl_hours = 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	result, err := LoadWithOverrides(FlagOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.FetchRobots || result.Sources["fetch_robots"] != "global" {
		t.Fatalf("fetch robots = %#v, want global false", result)
	}
	if result.Config.FetchUserAgent != "env-agent/2.0" || result.Sources["fetch_user_agent"] != "env" {
		t.Fatalf("fetch user agent = %#v, want env override", result)
	}
	if !result.Config.FetchNoCache || result.Sources["fetch_no_cache"] != "global" {
		t.Fatalf("fetch no-cache = %#v, want global true", result)
	}
	if result.Config.CacheTTLHours != 7 || result.Sources["cache_ttl_hours"] != "env" {
		t.Fatalf("cache ttl = %#v, want env 7", result)
	}
}

func TestShowOutputIncludesFetchSettings(t *testing.T) {
	result := Result{
		Config:  Config{CacheTTLHours: 24, FetchRobots: true, FetchUserAgent: "symbrowse/1.0", FetchNoCache: false},
		Sources: map[string]string{"cache_ttl_hours": "default", "fetch_robots": "default", "fetch_user_agent": "default", "fetch_no_cache": "default"},
	}
	fields := ShowOutputFor(result).Fields
	for _, key := range []string{"cache_ttl_hours", "fetch_robots", "fetch_user_agent", "fetch_no_cache"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("config show is missing %q: %#v", key, fields)
		}
	}
	if fields["fetch_robots"].Value != "true" || fields["fetch_user_agent"].Value != "symbrowse/1.0" || fields["fetch_no_cache"].Value != "false" {
		t.Fatalf("fetch config fields = %#v", fields)
	}
}

type testBuffer struct{ bytes []byte }

func (b *testBuffer) Write(p []byte) (int, error) {
	b.bytes = append(b.bytes, p...)
	return len(p), nil
}

func contains(value, want string) bool {
	for i := 0; i+len(want) <= len(value); i++ {
		if value[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
