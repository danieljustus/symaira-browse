// Package doctor diagnoses the local browser and filesystem prerequisites for symbrowse.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	StatusPass    = "pass"
	StatusWarn    = "warn"
	StatusFail    = "fail"
	StatusSkipped = "skipped"

	defaultCDPEndpoint = "http://127.0.0.1:9222/json/version"
)

// Paths are the directories that symbrowse needs to be able to use.
type Paths struct {
	ConfigDir string
	CacheDir  string
	StateDir  string
}

// Options controls a doctor run. Empty timeout values use safe defaults.
type Options struct {
	ExecutablePath string
	CDPEndpoint    string
	SocketDir      string
	Paths          Paths
	VersionTimeout time.Duration
	ProbeTimeout   time.Duration
}

// Check is one stable, machine-readable diagnostic result.
type Check struct {
	Name    string            `json:"name"`
	Status  string            `json:"status"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// Report is the stable payload emitted by doctor --json.
type Report struct {
	Status string   `json:"status"`
	Checks []Check  `json:"checks"`
	Fixes  []string `json:"fixes,omitempty"`
}

// Browser describes the executable selected by discovery.
type Browser struct {
	Path   string
	Source string
}

// DiscoveryError is returned when no supported browser executable is found.
type DiscoveryError struct {
	SearchPaths []string
	Override    string
}

func (e *DiscoveryError) Error() string {
	return fmt.Sprintf("no Chrome, Chromium, or Edge executable found; searched %s", strings.Join(e.SearchPaths, ", "))
}

// Run performs the diagnostic using the host platform and environment.
func Run(options Options) Report {
	return run(options, runtime.GOOS, os.Getenv, exec.LookPath)
}

// SearchPaths returns the platform-specific paths and PATH names considered by doctor.
func SearchPaths(goos string) []string {
	return searchPaths(goos, os.Getenv)
}

// Write writes a report. JSON mode writes only the JSON payload to w.
func Write(w io.Writer, report Report, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(report)
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(w, "[%s] %s: %s\n", strings.ToUpper(check.Status), check.Name, check.Message); err != nil {
			return err
		}
	}
	if len(report.Fixes) > 0 {
		if _, err := fmt.Fprintln(w, "\nCopyable next steps:"); err != nil {
			return err
		}
		for _, fix := range report.Fixes {
			if _, err := fmt.Fprintf(w, "- %s\n", fix); err != nil {
				return err
			}
		}
	}
	return nil
}

// FixInstructions returns concrete, non-mutating guidance for --fix.
func FixInstructions(goos string, options Options) []string {
	override := options.ExecutablePath
	if override == "" {
		override = "/absolute/path/to/Chrome"
	}
	install := "Install Google Chrome, Chromium, or Microsoft Edge using your platform's package manager or official installer."
	switch goos {
	case "darwin":
		install = "Install Chrome with: brew install --cask google-chrome (or install Chromium/Edge from its official macOS installer)."
	case "linux":
		install = "Install Chromium with: sudo apt install chromium (or use your distribution's Chromium/Chrome/Edge package)."
	case "windows":
		install = "Install Chrome, Chromium, or Edge with winget, for example: winget install Google.Chrome."
	}
	return []string{
		"No changes were made; --fix only prints guidance.",
		install,
		fmt.Sprintf("Set an explicit browser when it is installed elsewhere: export SYMBROWSE_EXECUTABLE_PATH=%s", shellQuote(override)),
		"Rerun the checks with: symbrowse doctor (or symbrowse doctor --json).",
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\\"'\\\"'") + "'"
}

func run(options Options, goos string, environ func(string) string, lookPath func(string) (string, error)) Report {
	if options.VersionTimeout <= 0 {
		options.VersionTimeout = 2 * time.Second
	}
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = 750 * time.Millisecond
	}
	if options.CDPEndpoint == "" {
		options.CDPEndpoint = environ("SYMBROWSE_CDP_ENDPOINT")
		if options.CDPEndpoint == "" {
			options.CDPEndpoint = defaultCDPEndpoint
		}
	}
	if options.SocketDir == "" {
		options.SocketDir = socketDir(goos, environ)
	}

	search := searchPaths(goos, environ)
	browser, discoveryErr := discover(goos, options.ExecutablePath, environ, lookPath)
	report := Report{Checks: make([]Check, 0, 7)}
	if discoveryErr != nil {
		report.Checks = append(report.Checks, Check{
			Name:    "chrome",
			Status:  StatusFail,
			Message: fmt.Sprintf("Chrome, Chromium, or Edge was not found; searched %s; set SYMBROWSE_EXECUTABLE_PATH to override discovery", strings.Join(search, ", ")),
			Details: map[string]string{"override": "SYMBROWSE_EXECUTABLE_PATH", "search_paths": strings.Join(search, "\n")},
		})
		report.Checks = append(report.Checks, Check{Name: "version", Status: StatusSkipped, Message: "skipped because no browser executable was found"})
	} else {
		report.Checks = append(report.Checks, Check{
			Name:    "chrome",
			Status:  StatusPass,
			Message: fmt.Sprintf("using %s (%s)", browser.Path, browser.Source),
			Details: map[string]string{"path": browser.Path, "source": browser.Source},
		})
		versionCheck := checkVersion(browser.Path, options.VersionTimeout)
		versionCheck.Name = "version"
		report.Checks = append(report.Checks, versionCheck)
	}

	report.Checks = append(report.Checks, checkCDP(options.CDPEndpoint, options.ProbeTimeout))
	report.Checks = append(report.Checks,
		checkWritable("config_dir", options.Paths.ConfigDir),
		checkWritable("cache_dir", options.Paths.CacheDir),
		checkWritable("state_dir", options.Paths.StateDir),
		checkWritable("socket_dir", options.SocketDir),
	)
	report.Status = aggregateStatus(report.Checks)
	return report
}

func discover(goos, override string, environ func(string) string, lookPath func(string) (string, error)) (Browser, *DiscoveryError) {
	search := searchPaths(goos, environ)
	if override != "" {
		candidate := override
		if !filepath.IsAbs(candidate) {
			if path, err := lookPath(candidate); err == nil {
				candidate = path
			} else {
				return Browser{}, &DiscoveryError{SearchPaths: search, Override: override}
			}
		}
		if usableExecutable(goos, candidate) {
			return Browser{Path: candidate, Source: "SYMBROWSE_EXECUTABLE_PATH"}, nil
		}
		return Browser{}, &DiscoveryError{SearchPaths: search, Override: override}
	}

	known := knownPaths(goos, environ)
	for _, candidate := range known {
		if usableExecutable(goos, candidate) {
			return Browser{Path: candidate, Source: "platform path"}, nil
		}
	}
	for _, name := range pathNames(goos) {
		if candidate, err := lookPath(name); err == nil && usableExecutable(goos, candidate) {
			return Browser{Path: candidate, Source: "PATH"}, nil
		}
	}
	return Browser{}, &DiscoveryError{SearchPaths: search}
}

func searchPaths(goos string, environ func(string) string) []string {
	paths := append([]string{}, knownPaths(goos, environ)...)
	paths = append(paths, "PATH: "+strings.Join(pathNames(goos), ", "))
	return paths
}

func knownPaths(goos string, environ func(string) string) []string {
	home := environ("HOME")
	if home == "" {
		home = environ("USERPROFILE")
	}
	switch goos {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			filepath.Join(home, "Applications", "Chromium.app", "Contents", "MacOS", "Chromium"),
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			filepath.Join(home, "Applications", "Microsoft Edge.app", "Contents", "MacOS", "Microsoft Edge"),
		}
	case "windows":
		programFiles := environ("ProgramFiles")
		programFilesX86 := environ("ProgramFiles(x86)")
		localAppData := environ("LOCALAPPDATA")
		if programFiles == "" {
			programFiles = `C:\Program Files`
		}
		if programFilesX86 == "" {
			programFilesX86 = `C:\Program Files (x86)`
		}
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return []string{
			filepath.Join(programFiles, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(programFilesX86, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(programFiles, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(programFilesX86, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(localAppData, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(programFiles, "Chromium", "Application", "chromium.exe"),
			filepath.Join(localAppData, "Chromium", "Application", "chromium.exe"),
		}
	default:
		return []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/microsoft-edge",
			"/usr/bin/microsoft-edge-stable",
			"/opt/google/chrome/google-chrome",
			"/opt/microsoft/msedge/msedge",
			"/snap/bin/chromium",
		}
	}
}

func pathNames(goos string) []string {
	if goos == "windows" {
		return []string{"chrome.exe", "chromium.exe", "msedge.exe", "chrome", "chromium", "microsoft-edge"}
	}
	return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable"}
}

func socketDir(goos string, environ func(string) string) string {
	if goos == "darwin" {
		home := environ("HOME")
		return filepath.Join(home, "Library", "Caches", "symbrowse", "run")
	}
	if goos == "windows" {
		base := environ("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(environ("USERPROFILE"), "AppData", "Local")
		}
		return filepath.Join(base, "symbrowse", "run")
	}
	base := environ("XDG_RUNTIME_DIR")
	if base == "" {
		base = filepath.Join(environ("HOME"), ".cache", "symbrowse", "run")
	}
	return filepath.Join(base, "symbrowse")
}

func usableExecutable(goos, path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return goos == "windows" || info.Mode()&0o111 != 0
}

func checkVersion(path string, timeout time.Duration) Check {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := commandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Check{Status: StatusWarn, Message: fmt.Sprintf("version check timed out after %s", timeout), Details: map[string]string{"path": path}}
		}
		return Check{Status: StatusWarn, Message: fmt.Sprintf("version check failed: %v", err), Details: map[string]string{"path": path}}
	}
	version := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	if version == "" {
		return Check{Status: StatusWarn, Message: "browser returned an empty version", Details: map[string]string{"path": path}}
	}
	return Check{Status: StatusPass, Message: version, Details: map[string]string{"path": path, "version": version}}
}

func checkCDP(endpoint string, timeout time.Duration) Check {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Check{Name: "cdp", Status: StatusWarn, Message: fmt.Sprintf("CDP endpoint is invalid: %v", err), Details: map[string]string{"endpoint": endpoint}}
	}
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return Check{Name: "cdp", Status: StatusWarn, Message: fmt.Sprintf("CDP is not running or unreachable: %v", err), Details: map[string]string{"endpoint": endpoint}}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Check{Name: "cdp", Status: StatusWarn, Message: fmt.Sprintf("CDP endpoint returned HTTP %s", response.Status), Details: map[string]string{"endpoint": endpoint}}
	}
	return Check{Name: "cdp", Status: StatusPass, Message: "CDP endpoint is reachable", Details: map[string]string{"endpoint": endpoint}}
}

func checkWritable(name, path string) Check {
	if path == "" {
		return Check{Name: name, Status: StatusFail, Message: "path is empty"}
	}
	probeDir := path
	missing := false
	for {
		info, err := os.Stat(probeDir)
		if err == nil {
			if !info.IsDir() {
				return Check{Name: name, Status: StatusFail, Message: "path exists but is not a directory", Details: map[string]string{"path": path}}
			}
			break
		}
		if !os.IsNotExist(err) {
			return Check{Name: name, Status: StatusFail, Message: fmt.Sprintf("cannot inspect path: %v", err), Details: map[string]string{"path": path}}
		}
		missing = true
		parent := filepath.Dir(probeDir)
		if parent == probeDir {
			return Check{Name: name, Status: StatusFail, Message: "no existing parent directory", Details: map[string]string{"path": path}}
		}
		probeDir = parent
	}

	temporary, err := os.CreateTemp(probeDir, ".symbrowse-doctor-*")
	if err != nil {
		return Check{Name: name, Status: StatusFail, Message: fmt.Sprintf("directory is not writable: %v", err), Details: map[string]string{"path": path, "probe_dir": probeDir}}
	}
	temporaryName := temporary.Name()
	closeErr := temporary.Close()
	removeErr := os.Remove(temporaryName)
	if closeErr != nil || removeErr != nil {
		return Check{Name: name, Status: StatusFail, Message: "temporary write probe could not be cleaned up", Details: map[string]string{"path": path, "probe_dir": probeDir}}
	}
	if missing {
		return Check{Name: name, Status: StatusWarn, Message: "path does not exist yet; nearest existing parent is writable", Details: map[string]string{"path": path, "probe_dir": probeDir}}
	}
	return Check{Name: name, Status: StatusPass, Message: "directory is writable", Details: map[string]string{"path": path}}
}

func aggregateStatus(checks []Check) string {
	status := StatusPass
	for _, check := range checks {
		switch check.Status {
		case StatusFail:
			return StatusFail
		case StatusWarn:
			status = StatusWarn
		}
	}
	return status
}

// HasFailure reports whether any check failed.
func (r Report) HasFailure(name string) bool {
	for _, check := range r.Checks {
		if check.Name == name && check.Status == StatusFail {
			return true
		}
	}
	return false
}

// HasFailures reports whether the report contains any failed check.
func (r Report) HasFailures() bool {
	for _, check := range r.Checks {
		if check.Status == StatusFail {
			return true
		}
	}
	return false
}

// StableCheckNames is useful to callers that validate the doctor schema.
func StableCheckNames() []string {
	names := []string{"chrome", "version", "cdp", "config_dir", "cache_dir", "state_dir", "socket_dir"}
	return append([]string(nil), names...)
}

// SortedDetails returns detail keys in deterministic order for human callers.
func SortedDetails(details map[string]string) []string {
	keys := make([]string, 0, len(details))
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var commandContext = exec.CommandContext
