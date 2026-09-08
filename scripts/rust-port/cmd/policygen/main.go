// Command policygen freezes the deterministic Go policy contracts for Rust.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-browse/internal/policy"
)

const (
	oracleCommit  = "652453d1595fc302bd69c328e7da8a21dbee28b9"
	oracleRelease = "v0.8.0"
)

var sourceFiles = []string{
	"internal/policy/allowlist.go",
	"internal/policy/ssrf.go",
	"internal/policy/policy.go",
}

type fixture struct {
	SchemaVersion int             `json:"schema_version"`
	Oracle        oracle          `json:"oracle"`
	Allowlists    []allowlistCase `json:"allowlists"`
	SSRF          []ssrfCase      `json:"ssrf"`
	Risk          riskFixture     `json:"risk"`
}

type oracle struct {
	Commit      string            `json:"commit"`
	Release     string            `json:"release"`
	SourceFiles map[string]string `json:"source_files"`
}

type allowlistCase struct {
	Name     string     `json:"name"`
	Patterns []string   `json:"patterns"`
	Valid    bool       `json:"valid"`
	Hosts    []hostCase `json:"hosts,omitempty"`
	URLs     []urlCase  `json:"urls,omitempty"`
}

type hostCase struct {
	Host  string `json:"host"`
	Allow bool   `json:"allow"`
}

type urlCase struct {
	URL   string `json:"url"`
	Allow bool   `json:"allow"`
}

type ssrfCase struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Addresses []string `json:"addresses"`
	Allow     bool     `json:"allow"`
	Error     string   `json:"error,omitempty"`
}

type riskFixture struct {
	Defaults []defaultCase `json:"defaults"`
	Rules    []ruleCase    `json:"rules"`
	Explain  string        `json:"explain"`
}

type defaultCase struct {
	Class string `json:"class"`
	Mode  string `json:"mode"`
	Want  string `json:"want"`
}

type ruleCase struct {
	Class  string `json:"class"`
	Host   string `json:"host"`
	Mode   string `json:"mode"`
	Want   string `json:"want"`
	Origin string `json:"origin"`
}

func main() {
	path := flag.String("output", "testdata/port/policy/policy-contract.json", "fixture path")
	check := flag.Bool("check", false, "fail when the fixture differs")
	flag.Parse()
	built := buildFixture()
	content, err := json.MarshalIndent(built, "", "  ")
	if err != nil {
		fatal("encode fixture: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(*path) // #nosec G304 -- explicit fixture path
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			fatal("fixture drift: run make port-policy-fixtures-generate")
		}
		fmt.Printf("PASS policy fixture (%d allowlist, %d SSRF cases)\n", len(built.Allowlists), len(built.SSRF))
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

func buildFixture() fixture {
	hashes := pinnedSourceHashes()
	result := fixture{
		SchemaVersion: 1,
		Oracle:        oracle{Commit: oracleCommit, Release: oracleRelease, SourceFiles: hashes},
		Allowlists:    buildAllowlists(),
		SSRF:          buildSSRF(),
		Risk:          buildRisk(),
	}
	return result
}

func buildAllowlists() []allowlistCase {
	valid := []allowlistCase{
		{
			Name: "exact-and-wildcard", Patterns: []string{"example.com", "*.example.org"}, Valid: true,
			Hosts: []hostCase{{"example.com", true}, {"www.example.com", false}, {"example.org", true}, {"a.b.example.org", true}, {"notexample.com", false}},
			URLs: []urlCase{
				{URL: "https://example.com/"},
				{URL: "wss://a.example.org/socket"},
				{URL: "file:///etc/passwd"},
				{URL: "https://example.org.evil/"},
				{URL: "http://127.0.0.1\\@example.com/"},
				{URL: "http://example.com:bad/"},
				{URL: " https://example.com/"},
			},
		},
		{
			Name: "case-and-trailing-dot", Patterns: []string{"EXAMPLE.COM.", "*.Example.ORG."}, Valid: true,
			Hosts: []hostCase{{"EXAMPLE.COM.", true}, {"WWW.EXAMPLE.ORG.", true}},
		},
		{Name: "inactive", Patterns: nil, Valid: true, URLs: []urlCase{{"https://anything.example/", true}, {"file:///etc/passwd", true}}},
	}
	for _, pattern := range []string{"*", "*.", "http://x.com", "example.com:8080", "example.com/path", "user@example.com", "foo..example.com", ".example.com", "exa*mple.com"} {
		valid = append(valid, allowlistCase{Name: "invalid-" + strings.ReplaceAll(pattern, "/", "_"), Patterns: []string{pattern}})
	}
	for caseIndex := range valid {
		caseItem := &valid[caseIndex]
		allowlist, err := policy.ParseAllowlist(caseItem.Patterns)
		caseItem.Valid = err == nil
		if err != nil {
			continue
		}
		for hostIndex := range caseItem.Hosts {
			caseItem.Hosts[hostIndex].Allow = allowlist.AllowsHost(caseItem.Hosts[hostIndex].Host)
		}
		for urlIndex := range caseItem.URLs {
			parsed, parseErr := url.Parse(caseItem.URLs[urlIndex].URL)
			caseItem.URLs[urlIndex].Allow = parseErr == nil && allowlist.AllowsURL(parsed)
		}
	}
	return valid
}

func buildSSRF() []ssrfCase {
	cases := []ssrfCase{
		{Name: "public", URL: "https://example.com/", Addresses: []string{"93.184.216.34"}, Allow: true},
		{Name: "loopback", URL: "http://127.0.0.1:8080/", Addresses: []string{"127.0.0.1"}},
		{Name: "rfc1918", URL: "http://10.0.0.5/", Addresses: []string{"10.0.0.5"}},
		{Name: "link-local", URL: "http://169.254.10.10/", Addresses: []string{"169.254.10.10"}},
		{Name: "carrier-grade-nat", URL: "http://100.64.0.1/", Addresses: []string{"100.64.0.1"}},
		{Name: "ipv6-ula", URL: "http://[fd00::1]/", Addresses: []string{"fd00::1"}},
		{Name: "ipv4-mapped", URL: "http://[::ffff:127.0.0.1]/", Addresses: []string{"::ffff:127.0.0.1"}},
		{Name: "local-name", URL: "http://printer.local/", Addresses: []string{"192.168.1.7"}},
		{Name: "localhost", URL: "http://localhost:3000/", Addresses: []string{"127.0.0.1"}},
		{Name: "rebinding", URL: "http://rebind.example/", Addresses: []string{"93.184.216.34", "127.0.0.1"}},
		{Name: "dns-failure", URL: "http://missing.example/", Addresses: nil, Error: "DNS resolution failed"},
		{Name: "non-http", URL: "file:///etc/passwd", Addresses: []string{"127.0.0.1"}},
		{Name: "backslash-authority-confusion", URL: "http://127.0.0.1\\@example.com/", Addresses: []string{"93.184.216.34"}},
	}
	for i := range cases {
		caseItem := &cases[i]
		lookupError := caseItem.Error != ""
		var lookup policy.LookupFunc
		if lookupError {
			lookup = func(ctx context.Context, host string) ([]string, error) {
				return nil, errors.New("fixture DNS failure")
			}
		} else {
			addresses := append([]string(nil), caseItem.Addresses...)
			lookup = func(ctx context.Context, host string) ([]string, error) { return addresses, nil }
		}
		err := policy.CheckSSRFWithLookup(caseItem.URL, lookup)
		caseItem.Allow = err == nil
		if err != nil && caseItem.Error == "" {
			caseItem.Error = strings.SplitN(err.Error(), ":", 2)[0]
		}
	}
	return cases
}

func buildRisk() riskFixture {
	result := riskFixture{}
	for _, class := range policy.AllClasses() {
		for _, mode := range []policy.Mode{policy.ModeMCP, policy.ModeTTY} {
			result.Defaults = append(result.Defaults, defaultCase{string(class), string(mode), string(policy.Defaults(class, mode))})
		}
	}
	loaded := &policy.Policy{Rules: []policy.Rule{{Class: policy.ClassSubmit, Domain: "bank.example.com", Decision: policy.Deny}, {Class: policy.ClassCredential, Domain: "login.example.com", Decision: policy.Allow}}}
	for _, tc := range []struct {
		class policy.RiskClass
		host  string
		mode  policy.Mode
	}{{policy.ClassSubmit, "bank.example.com", policy.ModeMCP}, {policy.ClassSubmit, "bank.example.com.", policy.ModeMCP}, {policy.ClassSubmit, "other.example.com", policy.ModeMCP}, {policy.ClassCredential, "app.login.example.com", policy.ModeMCP}, {policy.ClassCredential, "login.example.com.", policy.ModeMCP}} {
		decision, origin := loaded.Decide(tc.class, tc.host, tc.mode)
		result.Rules = append(result.Rules, ruleCase{string(tc.class), tc.host, string(tc.mode), string(decision), origin})
	}
	result.Explain, _ = loaded.Explain("auth.login", "https://login.example.com/app", policy.ModeMCP)
	return result
}

func pinnedSourceHashes() map[string]string {
	result := make(map[string]string, len(sourceFiles))
	for _, path := range sourceFiles {
		current, err := os.ReadFile(path) // #nosec G304 -- fixed repository production inputs
		if err != nil {
			fatal("read current %s: %v", path, err)
		}
		command := exec.Command("git", "show", oracleCommit+":"+path) // #nosec G204 -- fixed commit and validated list
		pinned, err := command.Output()
		if err != nil {
			fatal("read pinned %s: %v", path, err)
		}
		currentSum := sha256.Sum256(current)
		pinnedSum := sha256.Sum256(pinned)
		if currentSum != pinnedSum {
			fatal("fixture source drift for %s: current_sha256=%s pinned_sha256=%s", path, hex.EncodeToString(currentSum[:]), hex.EncodeToString(pinnedSum[:]))
		}
		result[path] = hex.EncodeToString(pinnedSum[:])
	}
	return result
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
