//go:build rustport

package robots

// Rules exposes the production parser only to the Rust-port fixture generator.
type Rules struct {
	groups []group
}

// ParseRules parses robots.txt without performing network I/O.
func ParseRules(content string) Rules {
	return Rules{groups: parse(content)}
}

// Allows reports whether userAgent may access path.
func (r Rules) Allows(userAgent, path string) bool {
	return isAllowed(r.groups, userAgent, path)
}
