// Command portbench captures reproducible release-binary process baselines.
package main

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

const (
	defaultRuns    = 30
	processTimeout = 15 * time.Second
)

type workload struct {
	Name  string
	Args  []string
	Stdin string
}

type sample struct {
	DurationNS int64 `json:"duration_ns"`
	PeakRSS    int64 `json:"peak_rss_bytes,omitempty"`
}

type summary struct {
	Name             string   `json:"name"`
	Args             []string `json:"args"`
	Samples          int      `json:"samples"`
	FirstDurationNS  int64    `json:"first_duration_ns"`
	MinDurationNS    int64    `json:"min_duration_ns"`
	MedianDurationNS int64    `json:"median_duration_ns"`
	P95DurationNS    int64    `json:"p95_duration_ns"`
	MaxDurationNS    int64    `json:"max_duration_ns"`
	MedianPeakRSS    int64    `json:"median_peak_rss_bytes,omitempty"`
	P95PeakRSS       int64    `json:"p95_peak_rss_bytes,omitempty"`
}

type comparison struct {
	Name                string  `json:"name"`
	BinarySizeChangePct float64 `json:"binary_size_change_percent"`
	P95LatencyChangePct float64 `json:"p95_latency_change_percent"`
	MedianRSSChangePct  float64 `json:"median_peak_rss_change_percent,omitempty"`
}

type report struct {
	SchemaVersion      int          `json:"schema_version"`
	CapturedAt         string       `json:"captured_at"`
	Binary             binaryRef    `json:"binary"`
	Candidate          *binaryRef   `json:"candidate,omitempty"`
	Host               hostRef      `json:"host"`
	Runs               int          `json:"runs_per_workload"`
	Workloads          []summary    `json:"workloads"`
	CandidateWorkloads []summary    `json:"candidate_workloads,omitempty"`
	Comparisons        []comparison `json:"comparisons,omitempty"`
	Interpretation     string       `json:"interpretation,omitempty"`
	Limitations        []string     `json:"limitations"`
}

type binaryRef struct {
	Path     string `json:"path"`
	Size     int64  `json:"size_bytes"`
	SHA256   string `json:"sha256"`
	Revision string `json:"vcs_revision,omitempty"`
}

type hostRef struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

func main() {
	binary := flag.String("binary", "", "path to the reference binary")
	candidate := flag.String("candidate", "", "optional candidate binary for paired measurement")
	output := flag.String("output", "", "JSON report path")
	runs := flag.Int("runs", defaultRuns, "measured runs per workload")
	workloadName := flag.String("workload", "", "measure only this named workload")
	flag.Parse()
	if *binary == "" || *output == "" || *runs < 2 {
		fatal("--binary, --output and --runs >= 2 are required")
	}
	absolute, err := filepath.Abs(*binary)
	if err != nil {
		fatal("resolve binary: %v", err)
	}
	binaryInfo, err := inspectBinary(absolute)
	if err != nil {
		fatal("inspect binary: %v", err)
	}
	var candidatePath string
	var candidateInfo *binaryRef
	if *candidate != "" {
		candidatePath, err = filepath.Abs(*candidate)
		if err != nil {
			fatal("resolve candidate: %v", err)
		}
		value, inspectErr := inspectBinary(candidatePath)
		if inspectErr != nil {
			fatal("inspect candidate: %v", inspectErr)
		}
		candidateInfo = &value
	}
	workloads := []workload{
		{Name: "version-json", Args: []string{"version", "--json"}},
		{Name: "root-help", Args: []string{"--help"}},
		{Name: "config-show-json", Args: []string{"config", "show", "--json"}},
		{Name: "mcp-initialize-list", Args: []string{"mcp"}, Stdin: mcpInput()},
	}
	result := report{
		SchemaVersion: 1,
		CapturedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Binary:        binaryInfo,
		Candidate:     candidateInfo,
		Host:          hostRef{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		Runs:          *runs,
		Limitations: []string{
			"Measurements include process startup and use fresh HOME/XDG/temp roots per run.",
			"The first sample is reported separately; it is not a proven cold-cache measurement.",
			"Peak RSS is omitted on unsupported operating systems.",
			"This command measures process-mode CLI and MCP workloads; daemon steady-state is captured separately by its IPC slice.",
		},
	}
	for _, item := range workloads {
		if *workloadName != "" && item.Name != *workloadName {
			continue
		}
		measured, runErr := measure(absolute, item, *runs)
		if runErr != nil {
			fatal("measure %s: %v", item.Name, runErr)
		}
		result.Workloads = append(result.Workloads, measured)
		if candidateInfo != nil {
			candidateMeasured, candidateErr := measure(candidatePath, item, *runs)
			if candidateErr != nil {
				fatal("measure candidate %s: %v", item.Name, candidateErr)
			}
			result.CandidateWorkloads = append(result.CandidateWorkloads, candidateMeasured)
			result.Comparisons = append(result.Comparisons, compareSummaries(item.Name, binaryInfo, *candidateInfo, measured, candidateMeasured))
		}
	}
	if len(result.Workloads) == 0 {
		fatal("unknown workload %q", *workloadName)
	}
	if candidateInfo != nil && *workloadName == "version-json" {
		result.Interpretation = "Early value signal only: the Rust candidate implements only the version slice, so this does not satisfy or predict the full-product cutover value gate."
	}
	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatal("encode report: %v", err)
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create output directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write report: %v", err)
	}
	fmt.Printf("WROTE %s (%d workloads x %d runs, paired=%t)\n", *output, len(result.Workloads), *runs, candidateInfo != nil)
}

func measure(binary string, item workload, runs int) (summary, error) {
	first, err := runOnce(binary, item)
	if err != nil {
		return summary{}, err
	}
	samples := make([]sample, 0, runs)
	for range runs {
		value, runErr := runOnce(binary, item)
		if runErr != nil {
			return summary{}, runErr
		}
		samples = append(samples, value)
	}
	durations := make([]int64, len(samples))
	rss := make([]int64, 0, len(samples))
	for i, value := range samples {
		durations[i] = value.DurationNS
		if value.PeakRSS > 0 {
			rss = append(rss, value.PeakRSS)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	result := summary{
		Name:             item.Name,
		Args:             append([]string(nil), item.Args...),
		Samples:          len(samples),
		FirstDurationNS:  first.DurationNS,
		MinDurationNS:    durations[0],
		MedianDurationNS: percentile(durations, 50),
		P95DurationNS:    percentile(durations, 95),
		MaxDurationNS:    durations[len(durations)-1],
	}
	if len(rss) > 0 {
		sort.Slice(rss, func(i, j int) bool { return rss[i] < rss[j] })
		result.MedianPeakRSS = percentile(rss, 50)
		result.P95PeakRSS = percentile(rss, 95)
	}
	return result, nil
}

func runOnce(binary string, item workload) (sample, error) {
	root, err := os.MkdirTemp("", "symbrowse-bench-")
	if err != nil {
		return sample{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{home, workspace, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return sample{}, err
		}
	}
	command := exec.Command(binary, item.Args...) // #nosec G204 -- explicit operator-selected benchmark binary
	configureProcessTree(command)
	command.Dir = workspace
	command.Env = benchmarkEnv(home, tmp)
	command.Stdin = bytes.NewBufferString(item.Stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	started := time.Now()
	if err := command.Start(); err != nil {
		return sample{}, err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	timer := time.NewTimer(processTimeout)
	var waitErr error
	select {
	case waitErr = <-waitDone:
		timer.Stop()
	case <-timer.C:
		if err := killProcessTree(command); err != nil {
			return sample{}, fmt.Errorf("kill timed-out process tree: %w", err)
		}
		select {
		case <-waitDone:
			return sample{}, fmt.Errorf("process exceeded %s", processTimeout)
		case <-time.After(2 * time.Second):
			_ = command.Process.Kill()
			return sample{}, fmt.Errorf("process did not exit after timeout cleanup")
		}
	}
	elapsed := time.Since(started)
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return sample{}, fmt.Errorf("exit %d (stdout_sha256=%s stderr_sha256=%s)", exitErr.ExitCode(), digest(stdout.Bytes()), digest(stderr.Bytes()))
		}
		return sample{}, waitErr
	}
	return sample{DurationNS: elapsed.Nanoseconds(), PeakRSS: peakRSS(command.ProcessState)}, nil
}

func benchmarkEnv(home, tmp string) []string {
	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_RUNTIME_DIR=" + filepath.Join(home, ".local", "run"),
		"TMPDIR=" + tmp,
		"TMP=" + tmp,
		"TEMP=" + tmp,
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
		"TERM=dumb",
		"NO_COLOR=1",
		"SYMBROWSE_CHECK_UPDATES=0",
		"SYMBROWSE_SYMGUARD=off",
	}
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func inspectBinary(path string) (binaryRef, error) {
	content, err := os.ReadFile(path) // #nosec G304 -- explicit operator-selected benchmark binary
	if err != nil {
		return binaryRef{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return binaryRef{}, err
	}
	result := binaryRef{Path: filepath.Base(path), Size: info.Size(), SHA256: digest(content)}
	if build, buildErr := buildinfo.ReadFile(path); buildErr == nil {
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" {
				result.Revision = setting.Value
				break
			}
		}
	}
	return result, nil
}

func compareSummaries(name string, reference, candidate binaryRef, left, right summary) comparison {
	result := comparison{
		Name:                name,
		BinarySizeChangePct: percentChange(reference.Size, candidate.Size),
		P95LatencyChangePct: percentChange(left.P95DurationNS, right.P95DurationNS),
	}
	if left.MedianPeakRSS > 0 && right.MedianPeakRSS > 0 {
		result.MedianRSSChangePct = percentChange(left.MedianPeakRSS, right.MedianPeakRSS)
	}
	return result
}

func percentChange(reference, candidate int64) float64 {
	if reference == 0 {
		return 0
	}
	return (float64(candidate)/float64(reference) - 1) * 100
}

func percentile(sorted []int64, percent int) int64 {
	index := (len(sorted)*percent + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func mcpInput() string {
	return "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{},\"clientInfo\":{\"name\":\"portbench\",\"version\":\"0\"}}}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\"}\n"
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
