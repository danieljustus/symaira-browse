// Package diff runs language-neutral binary contract comparisons in isolated sandboxes.
package diff

import "time"

const (
	comparisonModeBytes       = "bytes"
	comparisonModeConsoleText = "console_text"
	comparisonModeJSON        = "json"
	comparisonModeIgnore      = "ignore"
)

// Suite is a language-neutral collection of black-box command cases.
type Suite struct {
	SchemaVersion int    `json:"schema_version"`
	Oracle        Oracle `json:"oracle"`
	Cases         []Case `json:"cases"`
}

// Oracle identifies the frozen implementation that produced the contracts.
type Oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

// Case defines one isolated process comparison.
type Case struct {
	ID         string            `json:"id"`
	Stage      string            `json:"stage,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Stdin      string            `json:"stdin,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	Clock      string            `json:"clock,omitempty"`
	Seed       string            `json:"seed,omitempty"`
	TimeoutMS  int               `json:"timeout_ms,omitempty"`
	StdoutMode string            `json:"stdout_mode,omitempty"`
	StderrMode string            `json:"stderr_mode,omitempty"`
	// IgnoreJSONFields removes volatile object fields recursively in JSON mode.
	IgnoreJSONFields []string    `json:"ignore_json_fields,omitempty"`
	CompareFiles     bool        `json:"compare_files,omitempty"`
	// DiagnoseContent allows mismatch errors to include a bounded excerpt of
	// the differing lines. Default false: mismatch errors never carry stream
	// content, because compared output may contain secrets. Set only for
	// cases whose output is known to be secret-free (for example config
	// path listings).
	DiagnoseContent bool        `json:"diagnose_content,omitempty"`
	Setup            []SetupFile `json:"setup,omitempty"`
}

// SetupFile is created below the isolated workspace before a process starts.
type SetupFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
}

func (c Case) timeout() time.Duration {
	if c.TimeoutMS <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.TimeoutMS) * time.Millisecond
}

func (c Case) stdoutComparisonMode() string {
	if c.StdoutMode == "" {
		return comparisonModeBytes
	}
	return c.StdoutMode
}

func (c Case) stderrComparisonMode() string {
	if c.StderrMode == "" {
		return comparisonModeBytes
	}
	return c.StderrMode
}
