// Package policy implements risk classification and the local policy engine
// (issue B-43). Every registered command carries exactly one risk class; a
// table maps class x domain -> allow|confirm|deny with separate defaults for
// MCP and TTY modes.
package policy

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// RiskClass is the fixed set of risk classes from ARCHITEKTUR.md §5.5.
type RiskClass string

const (
	ClassRead        RiskClass = "read"
	ClassNavigate    RiskClass = "navigate"
	ClassInteract    RiskClass = "interact"
	ClassSubmit      RiskClass = "submit"
	ClassEval        RiskClass = "eval"
	ClassCredential  RiskClass = "credential"
	ClassDownload    RiskClass = "download"
	ClassUpload      RiskClass = "upload"
	ClassNetworkMock RiskClass = "network-mock"
)

// AllClasses lists every class in stable order.
func AllClasses() []RiskClass {
	return []RiskClass{ClassRead, ClassNavigate, ClassInteract, ClassSubmit, ClassEval, ClassCredential, ClassDownload, ClassUpload, ClassNetworkMock}
}

// ValidClass reports whether c is a known class.
func ValidClass(c RiskClass) bool {
	for _, known := range AllClasses() {
		if c == known {
			return true
		}
	}
	return false
}

// Decision is the policy outcome.
type Decision string

const (
	Allow   Decision = "allow"
	Confirm Decision = "confirm"
	Deny    Decision = "deny"
)

// Mode distinguishes the default tables.
type Mode string

const (
	ModeMCP Mode = "mcp"
	ModeTTY Mode = "tty"
)

// Classification maps every registered command to its risk class. The test
// suite enforces that every command in the daemon's command table has an
// entry here.
var Classification = map[string]RiskClass{
	// read
	"snapshot":       ClassRead,
	"read":           ClassRead,
	"console.list":   ClassRead,
	"console.clear":  ClassRead,
	"errors.list":    ClassRead,
	"errors.clear":   ClassRead,
	"get.text":       ClassRead,
	"get.html":       ClassRead,
	"get.value":      ClassRead,
	"get.attr":       ClassRead,
	"get.title":      ClassRead,
	"get.url":        ClassRead,
	"get.count":      ClassRead,
	"get.box":        ClassRead,
	"get.styles":     ClassRead,
	"is.visible":     ClassRead,
	"is.enabled":     ClassRead,
	"is.checked":     ClassRead,
	"find":           ClassRead,
	"session.list":   ClassRead,
	"session.info":   ClassRead,
	"daemon.status":  ClassRead,
	"state.list":     ClassRead,
	"state.show":     ClassRead,
	"journal.tail":   ClassRead,
	"journal.show":   ClassRead,
	"policy.explain": ClassRead,
	"oob.status":     ClassRead,
	"cookies.list":   ClassRead,
	"storage.list":   ClassRead,
	"trace.replay":   ClassRead,
	"watch":          ClassRead,
	// navigate
	"open":    ClassNavigate,
	"goto":    ClassNavigate,
	"back":    ClassNavigate,
	"forward": ClassNavigate,
	"reload":  ClassNavigate,
	// interact
	"click":          ClassInteract,
	"dblclick":       ClassInteract,
	"fill":           ClassInteract,
	"type":           ClassInteract,
	"press":          ClassInteract,
	"hover":          ClassInteract,
	"focus":          ClassInteract,
	"select":         ClassInteract,
	"check":          ClassInteract,
	"uncheck":        ClassInteract,
	"scroll":         ClassInteract,
	"scrollintoview": ClassInteract,
	"wait":           ClassInteract,
	// submit
	"submit": ClassSubmit,
	// eval
	"eval": ClassEval,
	// credential
	"auth.login": ClassCredential,
	// download / upload
	"download": ClassDownload,
	"upload":   ClassUpload,
	// network-mock
	"network.route":    ClassNetworkMock,
	"network.unroute":  ClassNetworkMock,
	"network.requests": ClassRead,
	"network.request":  ClassRead,
	"network.har":      ClassRead,
	"set.headers":      ClassNetworkMock,
	"set.offline":      ClassNetworkMock,
	// state mutation commands are interact-classed (they touch the session)
	"state.save":     ClassInteract,
	"state.load":     ClassInteract,
	"state.clear":    ClassInteract,
	"state.clean":    ClassInteract,
	"cookies.set":    ClassInteract,
	"cookies.clear":  ClassInteract,
	"storage.set":    ClassInteract,
	"storage.clear":  ClassInteract,
	"set.viewport":   ClassInteract,
	"set.device":     ClassInteract,
	"set.geo":        ClassInteract,
	"set.media":      ClassInteract,
	"set.user-agent": ClassInteract,
}

// Classify returns the risk class for one command.
func Classify(command string) (RiskClass, error) {
	if class, ok := Classification[command]; ok {
		return class, nil
	}
	return "", fmt.Errorf("command %q has no risk classification", command)
}

// ClassForCommand returns the risk class for one command, falling back to
// RiskClass("unknown") when the command has no classification yet.
func ClassForCommand(command string) RiskClass {
	if class, ok := Classification[command]; ok {
		return class
	}
	return RiskClass("unknown")
}

// Defaults returns the built-in default decision for a class and mode.
func Defaults(class RiskClass, mode Mode) Decision {
	if mode == ModeMCP {
		switch class {
		case ClassSubmit, ClassEval, ClassCredential, ClassDownload, ClassUpload:
			return Confirm
		case ClassNetworkMock:
			return Deny
		}
		return Allow
	}
	switch class {
	case ClassEval, ClassCredential, ClassDownload, ClassUpload, ClassNetworkMock:
		return Confirm
	}
	return Allow
}

// Rule is one policy.toml rule.
type Rule struct {
	Class    RiskClass `toml:"class"`
	Domain   string    `toml:"domain"`
	Decision Decision  `toml:"decision"`
}

// Policy is the loaded local policy: explicit rules plus mode defaults.
type Policy struct {
	Rules []Rule `toml:"rules"`
	// Source is the file the policy was loaded from (for explain).
	Source string
}

// LoadPolicy reads a policy.toml file. A missing file yields an empty policy
// with the built-in defaults, never an error.
func LoadPolicy(path string) (*Policy, error) {
	policy := &Policy{Source: path}
	if _, err := toml.DecodeFile(path, policy); err != nil {
		if strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "not exist") {
			return policy, nil
		}
		return nil, fmt.Errorf("load policy %s: %w", path, err)
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return policy, nil
}

// Validate checks rule invariants.
func (p *Policy) Validate() error {
	for _, rule := range p.Rules {
		if !ValidClass(rule.Class) {
			return fmt.Errorf("policy rule references unknown risk class %q", rule.Class)
		}
		if rule.Domain == "" {
			return errors.New("policy rule with empty domain")
		}
		switch rule.Decision {
		case Allow, Confirm, Deny:
		default:
			return fmt.Errorf("policy rule has invalid decision %q", rule.Decision)
		}
	}
	return nil
}

// Decide resolves the effective decision for a class, URL host and mode.
// Explicit rules (longest domain match) win over mode defaults.
func (p *Policy) Decide(class RiskClass, host string, mode Mode) (Decision, string) {
	host = strings.ToLower(strings.TrimSpace(host))
	var best *Rule
	for i := range p.Rules {
		rule := &p.Rules[i]
		if rule.Class != class {
			continue
		}
		if domainMatches(rule.Domain, host) {
			if best == nil || len(rule.Domain) > len(best.Domain) {
				best = rule
			}
		}
	}
	if best != nil {
		return best.Decision, fmt.Sprintf("rule:%s", best.Domain)
	}
	return Defaults(class, mode), "default"
}

// domainMatches reports whether host matches the rule domain. A rule domain
// without a leading dot matches the host exactly or as a suffix boundary.
func domainMatches(ruleDomain, host string) bool {
	ruleDomain = strings.ToLower(strings.TrimSpace(ruleDomain))
	if ruleDomain == "" || host == "" {
		return false
	}
	if ruleDomain == host {
		return true
	}
	if strings.HasPrefix(ruleDomain, ".") {
		return strings.HasSuffix(host, ruleDomain)
	}
	return strings.HasSuffix(host, "."+ruleDomain)
}

// Explain returns a human-readable explanation of the effective decision.
func (p *Policy) Explain(command, url string, mode Mode) (string, error) {
	class, err := Classify(command)
	if err != nil {
		return "", err
	}
	host := hostOf(url)
	decision, origin := p.Decide(class, host, mode)
	var lines []string
	lines = append(lines, fmt.Sprintf("command:  %s", command))
	lines = append(lines, fmt.Sprintf("class:    %s", class))
	lines = append(lines, fmt.Sprintf("url:      %s", url))
	lines = append(lines, fmt.Sprintf("host:     %s", host))
	lines = append(lines, fmt.Sprintf("mode:     %s", mode))
	lines = append(lines, fmt.Sprintf("decision: %s", decision))
	lines = append(lines, fmt.Sprintf("origin:   %s", origin))
	return strings.Join(lines, "\n"), nil
}

// hostOf extracts the host from a URL, tolerating malformed input.
func hostOf(url string) string {
	rest := strings.TrimSpace(url)
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
	}
	if idx := strings.IndexAny(rest, "/?#"); idx >= 0 {
		rest = rest[:idx]
	}
	if idx := strings.Index(rest, "@"); idx >= 0 {
		rest = rest[idx+1:]
	}
	if idx := strings.Index(rest, ":"); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

// SortedCommands returns all classified commands in stable order (for
// documentation and tests).
func SortedCommands() []string {
	commands := make([]string, 0, len(Classification))
	for command := range Classification {
		commands = append(commands, command)
	}
	sort.Strings(commands)
	return commands
}
