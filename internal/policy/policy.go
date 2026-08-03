// Package policy assigns every symbrowse action a risk class. Enforcement by
// a policy engine starts in milestone M4 (ARCHITEKTUR.md §5.5); until then
// the classes are prepared as constants and reported by batch --dry-run.
package policy

// RiskClass classifies the blast radius of an action.
type RiskClass string

const (
	// RiskNone: read-only, no state change.
	RiskNone RiskClass = "none"
	// RiskLow: navigation and waiting.
	RiskLow RiskClass = "low"
	// RiskMedium: trusted interactions that mutate the page.
	RiskMedium RiskClass = "medium"
	// RiskHigh: reserved for flows and approval-gated actions (M4).
	RiskHigh RiskClass = "high"
)

// ClassForCommand maps a symbrowse command name to its risk class.
func ClassForCommand(command string) RiskClass {
	switch command {
	case "open", "goto", "back", "forward", "reload", "wait", "find":
		return RiskLow
	case "click", "dblclick", "fill", "type", "press", "hover", "focus",
		"select", "check", "uncheck", "scroll", "scrollintoview":
		return RiskMedium
	default:
		// read, snapshot, get, is, session, config, doctor, version, daemon,
		// batch and unknown commands are treated as read-only/planning.
		return RiskNone
	}
}
