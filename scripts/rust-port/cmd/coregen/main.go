// Command coregen freezes pure Go output and budget contracts for the Rust port.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/danieljustus/symaira-browse/internal/budget"
	"github.com/danieljustus/symaira-browse/internal/output"
)

const (
	oracleCommit  = "652453d1595fc302bd69c328e7da8a21dbee28b9"
	oracleRelease = "v0.8.0"
)

type fixture struct {
	SchemaVersion int             `json:"schema_version"`
	Oracle        oracle          `json:"oracle"`
	ErrorCodes    []errorCode     `json:"error_codes"`
	Outputs       []outputCase    `json:"outputs"`
	Estimates     []estimateCase  `json:"estimates"`
	Truncations   []truncateCase  `json:"truncations"`
	LineRanges    []lineRangeCase `json:"line_ranges"`
	Cache         cacheCase       `json:"cache"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type errorCode struct {
	Code string `json:"code"`
	Kind string `json:"kind"`
	Exit int    `json:"exit"`
}

type outputCase struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Output string `json:"output"`
}

type estimateCase struct {
	Name     string `json:"name"`
	Input    string `json:"input"`
	Expected int    `json:"expected"`
}

type truncateCase struct {
	Name           string `json:"name"`
	Input          string `json:"input"`
	MaxTokens      int    `json:"max_tokens"`
	Head           string `json:"head"`
	Foot           string `json:"foot"`
	TokensReturned int    `json:"tokens_returned"`
	TokensTotal    int    `json:"tokens_total"`
	Truncated      bool   `json:"truncated"`
}

type lineRangeCase struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Output string `json:"output"`
}

type cacheCase struct {
	ID          string `json:"id"`
	Content     string `json:"content"`
	Metadata    string `json:"metadata"`
	ContentMode uint32 `json:"content_mode"`
	MetaMode    uint32 `json:"meta_mode"`
}

type cacheMetadata struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func main() {
	path := flag.String("output", "testdata/port/core/output-budget-contract.json", "fixture path")
	check := flag.Bool("check", false, "fail when the fixture differs")
	flag.Parse()
	built := buildFixture()
	content, err := json.MarshalIndent(built, "", "  ")
	if err != nil {
		fatal("encode fixture: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(*path) // #nosec G304 -- explicit developer-selected fixture
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			// Windows exposes no Unix permission bits: os.Stat().Mode().Perm()
			// there reflects the read-only attribute, not the 0600/0644 the
			// cache actually requests, so the committed Unix-generated fixture
			// can never match byte-for-byte. Relax only the two mode fields on
			// Windows; every other byte still has to match.
			if runtime.GOOS == "windows" && equalExceptCacheModes(existing, content) {
				fmt.Printf("PASS core fixture (%d error codes, %d output cases; cache mode bits relaxed on windows)\n",
					len(built.ErrorCodes), len(built.Outputs))
				return
			}
			fatal("fixture drift: run make port-core-fixtures-generate")
		}
		fmt.Printf("PASS core fixture (%d error codes, %d output cases)\n", len(built.ErrorCodes), len(built.Outputs))
		return
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*path, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("WROTE %s\n", *path)
}

// equalExceptCacheModes reports whether two encoded fixtures differ only in
// the platform-specific cache file mode fields. Used on Windows, where
// Mode().Perm() cannot represent the Unix 0600/0644 the cache requests.
func equalExceptCacheModes(a, b []byte) bool {
	var fa, fb fixture
	if err := json.Unmarshal(a, &fa); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &fb); err != nil {
		return false
	}
	fa.Cache.ContentMode = 0
	fa.Cache.MetaMode = 0
	fb.Cache.ContentMode = 0
	fb.Cache.MetaMode = 0
	na, err := json.MarshalIndent(fa, "", "  ")
	if err != nil {
		return false
	}
	nb, err := json.MarshalIndent(fb, "", "  ")
	if err != nil {
		return false
	}
	return bytes.Equal(na, nb)
}

func buildFixture() fixture {
	result := fixture{
		SchemaVersion: 1,
		Oracle:        oracle{Commit: oracleCommit, Release: oracleRelease},
	}
	for _, code := range errorCodes() {
		result.ErrorCodes = append(result.ErrorCodes, errorCode{
			Code: string(code),
			Kind: fmt.Sprint(output.KindFromCode(code)),
			Exit: int(output.ExitCodeFromCode(code)),
		})
	}
	result.Outputs = buildOutputs()
	result.Estimates = []estimateCase{
		{Name: "empty", Input: "", Expected: budget.Estimate("")},
		{Name: "short", Input: "hi", Expected: budget.Estimate("hi")},
		{Name: "ascii", Input: "abcdefghijklmnop", Expected: budget.Estimate("abcdefghijklmnop")},
		{Name: "unicode-runes", Input: "äöüß🙂漢字", Expected: budget.Estimate("äöüß🙂漢字")},
	}
	for _, tc := range []struct {
		name      string
		input     string
		maxTokens int
	}{
		{name: "under-budget", input: "small output", maxTokens: 100},
		{name: "ascii-split", input: "0123456789abcdefghijklmnopqrstuv", maxTokens: 4},
		{name: "unicode-split", input: "äöüß🙂漢字abcdefghi", maxTokens: 2},
	} {
		head, foot, returned, total, truncated := budget.Truncate([]byte(tc.input), tc.maxTokens)
		result.Truncations = append(result.Truncations, truncateCase{
			Name: tc.name, Input: tc.input, MaxTokens: tc.maxTokens,
			Head: string(head), Foot: string(foot), TokensReturned: returned,
			TokensTotal: total, Truncated: truncated,
		})
	}
	for _, tc := range []lineRangeCase{
		{Name: "middle", Input: "one\ntwo\nthree\nfour", Start: 2, End: 3},
		{Name: "all-defaults", Input: "one\ntwo\nthree\nfour", Start: 0, End: 0},
		{Name: "clamped", Input: "one\ntwo\nthree\nfour", Start: 3, End: 99},
		{Name: "past-end", Input: "one\ntwo", Start: 9, End: 10},
	} {
		tc.Output = budget.LineRange([]byte(tc.Input), tc.Start, tc.End)
		result.LineRanges = append(result.LineRanges, tc)
	}
	result.Cache = buildCacheCase()
	return result
}

func buildCacheCase() cacheCase {
	root, err := os.MkdirTemp("", "symbrowse-cache-contract-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	cache := budget.NewCache(root, time.Hour)
	generatedID, err := cache.Store([]byte("full content"))
	if err != nil {
		panic(err)
	}
	contentPath := filepath.Join(root, generatedID+".json")
	metaPath := filepath.Join(root, generatedID+".meta.json")
	content, err := os.ReadFile(contentPath)
	if err != nil {
		panic(err)
	}
	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		panic(err)
	}
	var metadata cacheMetadata
	if err := json.Unmarshal(metaRaw, &metadata); err != nil {
		panic(err)
	}
	metadata.ID = "out_060504030201"
	metadata.CreatedAt = time.Date(2099, 1, 2, 3, 4, 5, 6000000, time.UTC)
	metadata.ExpiresAt = metadata.CreatedAt.Add(time.Hour)
	normalized, err := json.Marshal(metadata)
	if err != nil {
		panic(err)
	}
	contentInfo, err := os.Stat(contentPath)
	if err != nil {
		panic(err)
	}
	metaInfo, err := os.Stat(metaPath)
	if err != nil {
		panic(err)
	}
	return cacheCase{
		ID:          metadata.ID,
		Content:     string(content),
		Metadata:    string(normalized),
		ContentMode: uint32(contentInfo.Mode().Perm()),
		MetaMode:    uint32(metaInfo.Mode().Perm()),
	}
}

func errorCodes() []output.Code {
	return []output.Code{
		output.CodeAuth, output.CodeConfig, output.CodeConflict,
		output.CodeDaemonUnavailable, output.CodeFlowFailed, output.CodeHandoffTimeout,
		output.CodeInternal, output.CodeInvalidArgs, output.CodeInvalidInspection,
		output.CodeInvalidSession, output.CodeMalformedRequest, output.CodeNoInput,
		output.CodeNotFound, output.CodeOperationFailed, output.CodeOperationTimeout,
		output.CodePeerDenied, output.CodePermission, output.CodeSessionInactive,
		output.CodeSessionNotFound, output.CodeSessionUserControl, output.CodeStaleRef,
		output.CodeUnavailable, output.CodeUnknownCommand, output.CodeUnknownRef,
		output.CodeValidation,
	}
}

func buildOutputs() []outputCase {
	warnings := []output.Warning{{Kind: "prompt_injection", Severity: "high", Message: "untrusted instruction", Ref: "@e7", Excerpt: "ignore previous"}}
	cases := []struct {
		name     string
		envelope output.Envelope
		format   output.Format
	}{
		{name: "json-success", envelope: output.OK(map[string]any{"result": "ok", "count": 2}, warnings), format: output.FormatJSON},
		{name: "json-error", envelope: output.FailureWithHint("stale_ref", "element @e7 no longer exists", "run snapshot --diff"), format: output.FormatJSON},
		{name: "json-hard-stop", envelope: output.Envelope{Success: false, Error: &output.Error{Code: "session_user_control", Message: "session is controlled by a human", Retryable: boolPointer(false), RequiresUserConfirmation: boolPointer(true), ResumeHint: "request explicit confirmation"}}, format: output.FormatJSON},
		{name: "text-nil-success", envelope: output.OK(nil, nil), format: output.FormatText},
		{name: "text-string", envelope: output.OK("hello", nil), format: output.FormatText},
		{name: "text-map", envelope: output.OK(map[string]any{"b": 2, "a": 1}, nil), format: output.FormatText},
		{name: "text-error", envelope: output.Failure("internal", "something failed"), format: output.FormatText},
		{name: "text-truncation", envelope: output.OK(map[string]any{"truncated": true, "tokens_returned": float64(90), "tokens_total": float64(18400), "cache_id": "out_000000000000", "hint": "symbrowse cache get out_000000000000 --range 40-120", "head": "HEAD", "foot": "FOOT"}, nil), format: output.FormatText},
		{name: "yaml-success", envelope: output.OK(map[string]any{"url": "https://example.com", "n": 42}, nil), format: output.FormatYAML},
		{name: "yaml-warning", envelope: output.OK("ok", warnings), format: output.FormatYAML},
		{name: "yaml-error", envelope: output.FailureWithHint("stale_ref", "element missing", "take a snapshot"), format: output.FormatYAML},
	}
	results := make([]outputCase, 0, len(cases))
	for _, tc := range cases {
		var buffer bytes.Buffer
		if err := output.Write(&buffer, tc.envelope, tc.format); err != nil {
			panic(err)
		}
		results = append(results, outputCase{Name: tc.name, Format: string(tc.format), Output: buffer.String()})
	}
	return results
}

func boolPointer(value bool) *bool { return &value }

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
