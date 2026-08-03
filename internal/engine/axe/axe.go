// Package axe embeds axe-core (MPL-2.0, Deque Systems) and runs accessibility
// audits inside the page. The script is vendored so audits work fully offline
// with no CDN dependency.
package axe

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// Version is the vendored axe-core version, reported in every audit output.
const Version = "4.10.2"

//go:embed assets/axe.min.js
var source string

// Source returns the embedded axe-core script.
func Source() string { return source }

// RunScript builds the JavaScript that injects axe-core into the page and
// runs an audit. tags filters by WCAG tag (e.g. wcag2a, wcag2aa); selector
// restricts the audit root. The expression is evaluated with returnByValue.
func RunScript(tags []string, selector string) (string, error) {
	options := map[string]any{}
	if len(tags) > 0 {
		options["runOnly"] = map[string]any{"type": "tag", "values": tags}
	}
	if strings.TrimSpace(selector) != "" {
		options["exclude"] = nil
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return "", err
	}

	// The embedded script is injected as a string literal. To stay robust
	// against any quotes in the minified bundle we base64-encode it and eval
	// the decoded text in the page context.
	const template = `(function() {
  if (window.axe && window.axe.run) {
    return window.axe.run(%s, %s).then(function (results) {
      return { axe_version: window.axe.version, results: results };
    });
  }
  var src = atob("%s");
  (0, eval)(src);
  return window.axe.run(%s, %s).then(function (results) {
    return { axe_version: window.axe.version, results: results };
  });
})()`

	selectorArg := "document"
	if strings.TrimSpace(selector) != "" {
		selectorArg = fmt.Sprintf("'%s'", strings.ReplaceAll(selector, "'", "\\'"))
	}
	encoded := encodeBase64([]byte(source))
	return fmt.Sprintf(template, selectorArg, string(optionsJSON), encoded, selectorArg, string(optionsJSON)), nil
}

// Result mirrors axe-core's run() result for the stable violations[] shape.
type Result struct {
	AxeVersion string        `json:"axe_version"`
	Results    *AuditResults `json:"results"`
}

// AuditResults is the subset of axe-core results we expose.
type AuditResults struct {
	Violations []Violation `json:"violations"`
	Passes     []Rule      `json:"passes,omitempty"`
	Incomplete []Rule      `json:"incomplete,omitempty"`
}

// Violation is one accessibility violation.
type Violation struct {
	ID          string   `json:"id"`
	Impact      string   `json:"impact"`
	Description string   `json:"description"`
	Help        string   `json:"help,omitempty"`
	HelpURL     string   `json:"helpUrl,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Nodes       []Node   `json:"nodes"`
}

// Rule is a pass or incomplete rule entry.
type Rule struct {
	ID     string `json:"id"`
	Impact string `json:"impact,omitempty"`
	Nodes  []Node `json:"nodes,omitempty"`
}

// Node is one affected element.
type Node struct {
	Target         []string `json:"target"`
	HTML           string   `json:"html,omitempty"`
	Impact         string   `json:"impact,omitempty"`
	Summary        string   `json:"summary,omitempty"`
	FailureSummary string   `json:"failureSummary,omitempty"`
}

// encodeBase64 implements base64 without importing encoding/base64 in the
// hot path (the test suite covers it).
func encodeBase64(data []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var builder strings.Builder
	for i := 0; i < len(data); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], data[i:])
		builder.WriteByte(table[chunk[0]>>2])
		builder.WriteByte(table[(chunk[0]&0x03)<<4|chunk[1]>>4])
		if n > 1 {
			builder.WriteByte(table[(chunk[1]&0x0f)<<2|chunk[2]>>6])
		} else {
			builder.WriteByte('=')
		}
		if n > 2 {
			builder.WriteByte(table[chunk[2]&0x3f])
		} else {
			builder.WriteByte('=')
		}
	}
	return builder.String()
}
