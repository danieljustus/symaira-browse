// Command configgen freezes Go configuration precedence for the Rust port.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/config"
)

const (
	oracleCommit  = "652453d1595fc302bd69c328e7da8a21dbee28b9"
	oracleRelease = "v0.8.0"
)

type fixture struct {
	SchemaVersion int           `json:"schema_version"`
	Oracle        oracle        `json:"oracle"`
	Cases         []caseFixture `json:"cases"`
	Invalid       []invalidCase `json:"invalid"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type caseFixture struct {
	Name   string                  `json:"name"`
	Fields map[string]config.Field `json:"fields"`
}

type invalidCase struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

func main() {
	path := flag.String("output", "testdata/port/core/config-contract.json", "fixture path")
	check := flag.Bool("check", false, "fail when the fixture differs")
	flag.Parse()
	absolutePath, err := filepath.Abs(*path)
	if err != nil {
		fatal("resolve fixture path: %v", err)
	}
	built := buildFixture()
	content, err := json.MarshalIndent(built, "", "  ")
	if err != nil {
		fatal("encode fixture: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(absolutePath) // #nosec G304 -- explicit developer-selected fixture
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			fatal("fixture drift: run make port-config-fixtures-generate")
		}
		fmt.Printf("PASS config fixture (%d cases)\n", len(built.Cases))
		return
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(absolutePath, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("WROTE %s\n", *path)
}

func buildFixture() fixture {
	root, err := os.MkdirTemp("", "symbrowse-config-contract-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	xdgConfig := filepath.Join(root, "xdg-config")
	xdgCache := filepath.Join(root, "xdg-cache")
	xdgState := filepath.Join(root, "xdg-state")
	for _, dir := range []string{home, workspace, xdgConfig, xdgCache, xdgState} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			panic(err)
		}
	}
	clearSymbrowseEnv()
	mustSetenv("HOME", home)
	mustSetenv("XDG_CONFIG_HOME", xdgConfig)
	mustSetenv("XDG_CACHE_HOME", xdgCache)
	mustSetenv("XDG_STATE_HOME", xdgState)
	if err := os.Chdir(workspace); err != nil {
		panic(err)
	}
	result := fixture{SchemaVersion: 1, Oracle: oracle{Commit: oracleCommit, Release: oracleRelease}}
	defaults, err := config.LoadWithOverrides(config.FlagOverrides{})
	if err != nil {
		panic(err)
	}
	result.Cases = append(result.Cases, caseFixture{Name: "defaults", Fields: normalizedFields(defaults, root)})

	globalDir := filepath.Join(xdgConfig, "symbrowse")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		panic(err)
	}
	mustWrite(filepath.Join(globalDir, "config.toml"), "log_level = \"info\"\nstate_dir = \"global-state\"\nread_timeout = 77\nallowed_domains = [\"global.example\"]\n")
	mustWrite(filepath.Join(workspace, ".symbrowse.toml"), "log_level = \"debug\"\nstate_dir = \"project-state\"\noperation_timeout = 44\nengine = \"static\"\n")
	mustSetenv("SYMBROWSE_LOG_LEVEL", "error")
	mustSetenv("SYMBROWSE_READ_TIMEOUT", "90")
	mustSetenv("SYMBROWSE_ALLOWED_DOMAINS", "env.example, *.env.example")
	mustSetenv("SYMBROWSE_HEADLESS", "true")
	trace, flagState, flagCache := "trace", "flag-state", "flag-cache"
	precedence, err := config.LoadWithOverrides(config.FlagOverrides{LogLevel: &trace, StateDir: &flagState, CacheDir: &flagCache})
	if err != nil {
		panic(err)
	}
	result.Cases = append(result.Cases, caseFixture{Name: "precedence", Fields: normalizedFields(precedence, root)})

	mustSetenv("SYMBROWSE_ENGINE", "wat")
	_, err = config.LoadWithOverrides(config.FlagOverrides{})
	result.Invalid = append(result.Invalid, invalidCase{Name: "engine", Error: errorString(err)})
	mustSetenv("SYMBROWSE_ENGINE", "chrome")
	mustSetenv("SYMBROWSE_AUTOSAVE", "sometimes")
	_, err = config.LoadWithOverrides(config.FlagOverrides{})
	result.Invalid = append(result.Invalid, invalidCase{Name: "autosave", Error: errorString(err)})
	mustSetenv("SYMBROWSE_AUTOSAVE", "auto")
	mustSetenv("SYMBROWSE_READ_TIMEOUT", "0")
	_, err = config.LoadWithOverrides(config.FlagOverrides{})
	result.Invalid = append(result.Invalid, invalidCase{Name: "timeout", Error: errorString(err)})
	return result
}

func normalizedFields(result config.Result, root string) map[string]config.Field {
	fields := config.ShowOutputFor(result).Fields
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		canonicalRoot = root
	}
	for name, field := range fields {
		field.Value = strings.ReplaceAll(field.Value, canonicalRoot, "<ROOT>")
		field.Value = strings.ReplaceAll(field.Value, root, "<ROOT>")
		field.Value = filepath.ToSlash(field.Value)
		fields[name] = field
	}
	return fields
}

func clearSymbrowseEnv() {
	var names []string
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "SYMBROWSE_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
}

func mustSetenv(name, value string) {
	if err := os.Setenv(name, value); err != nil {
		panic(err)
	}
}

func mustWrite(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		panic(err)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
