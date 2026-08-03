package flows

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/policy"
)

// Executor sends one daemon frame and returns the protocol response. It is
// injectable so the runner is fully testable without a browser.
type Executor func(ctx context.Context, frame daemon.Frame) (daemon.Response, error)

// RunOptions configures one flow execution.
type RunOptions struct {
	Flow    *Flow
	Inputs  map[string]string
	Session string
	DryRun  bool
}

// StepRun is the outcome of one executed step.
type StepRun struct {
	Index      int              `json:"index"`
	Action     string           `json:"action"`
	RiskClass  policy.RiskClass `json:"risk_class"`
	Success    bool             `json:"success"`
	Error      string           `json:"error,omitempty"`
	Data       any              `json:"data,omitempty"`
	DurationMS int64            `json:"duration_ms"`
}

// RunPlanItem is one entry of the dry-run execution plan.
type RunPlanItem struct {
	Index     int              `json:"index"`
	Action    string           `json:"action"`
	RiskClass policy.RiskClass `json:"risk_class"`
}

// RunReport is the overall flow run result.
type RunReport struct {
	FlowID     string            `json:"flow_id"`
	Name       string            `json:"name"`
	Version    int               `json:"version"`
	DryRun     bool              `json:"dry_run,omitempty"`
	Steps      []StepRun         `json:"steps,omitempty"`
	Plan       []RunPlanItem     `json:"plan,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	Success    bool              `json:"success"`
	Error      string            `json:"error,omitempty"`
	DurationMS int64             `json:"duration_ms"`
}

// RunError is a structured flow-run failure: the step index and a diagnosis
// that is understandable without access to the page.
type RunError struct {
	StepIndex int        `json:"step_index"`
	Action    string     `json:"action"`
	Message   string     `json:"message"`
	Hint      string     `json:"hint,omitempty"`
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
}

func (e *RunError) Error() string {
	return fmt.Sprintf("flow step %d (%s) failed: %s", e.StepIndex, e.Action, e.Message)
}

// Run executes the flow step by step. Missing required inputs abort before
// any step runs; assertions are hard abort conditions; outputs are extracted
// from the final state. Every step is grouped under one flow_id for journal
// grouping.
func Run(ctx context.Context, executor Executor, options RunOptions) (*RunReport, error) {
	started := time.Now()
	flow := options.Flow
	resolved, err := resolveInputs(flow, options.Inputs)
	if err != nil {
		return nil, err
	}
	report := &RunReport{
		FlowID:  newFlowID(),
		Name:    flow.Name,
		Version: flow.Version,
		DryRun:  options.DryRun,
	}
	if options.DryRun {
		report.Plan = buildPlan(flow)
		report.Success = true
		report.DurationMS = time.Since(started).Milliseconds()
		return report, nil
	}
	if err := enforceDomains(flow, resolved); err != nil {
		var runErr *RunError
		if errors.As(err, &runErr) {
			diagnosis := diagnoseFailure(ctx, executor, options.Session, runErr.StepIndex, &flow.Steps[runErr.StepIndex], err)
			runErr.Diagnosis = &diagnosis
		}
		return nil, err
	}
	for index := range flow.Steps {
		step := &flow.Steps[index]
		stepStarted := time.Now()
		stepRun := StepRun{Index: index, Action: step.Action(), RiskClass: riskForStep(step)}
		data, stepErr := executeStep(ctx, executor, options.Session, step, resolved)
		stepRun.DurationMS = time.Since(stepStarted).Milliseconds()
		stepRun.Data = data
		if stepErr != nil {
			stepRun.Success = false
			stepRun.Error = stepErr.Error()
			report.Steps = append(report.Steps, stepRun)
			report.Success = false
			report.Error = stepErr.Error()
			report.DurationMS = time.Since(started).Milliseconds()
			diagnosis := diagnoseFailure(ctx, executor, options.Session, index, step, stepErr)
			return report, &RunError{
				StepIndex: index,
				Action:    step.Action(),
				Message:   stepErr.Error(),
				Hint:      diagnosis.RepairSuggestion,
				Diagnosis: &diagnosis,
			}
		}
		stepRun.Success = true
		report.Steps = append(report.Steps, stepRun)
	}
	outputs, err := extractOutputs(ctx, executor, options.Session, flow)
	if err != nil {
		report.Success = false
		report.Error = err.Error()
		report.DurationMS = time.Since(started).Milliseconds()
		return report, err
	}
	report.Outputs = outputs
	report.Success = true
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

// newFlowID returns a random hex identifier grouping one flow run in the
// journal (each step is executed as a regular daemon action under this id).
func newFlowID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("flow-%d", time.Now().UnixNano())
	}
	return "flow-" + hex.EncodeToString(raw[:])
}

// buildPlan returns the dry-run plan with risk classes per step.
func buildPlan(flow *Flow) []RunPlanItem {
	plan := make([]RunPlanItem, 0, len(flow.Steps))
	for index := range flow.Steps {
		plan = append(plan, RunPlanItem{Index: index, Action: flow.Steps[index].Action(), RiskClass: riskForStep(&flow.Steps[index])})
	}
	return plan
}

// riskForStep classifies a step by its underlying command risk class.
func riskForStep(step *Step) policy.RiskClass {
	switch step.Action() {
	case "open", "wait":
		return policy.ClassForCommand("open")
	case "find", "click", "fill":
		return policy.ClassForCommand("click")
	case "assert", "snapshot":
		return policy.ClassRead
	default:
		return policy.ClassRead
	}
}

// resolveInputs validates that every {{name}} reference is supplied and
// substitutes it into step values. Missing inputs abort before execution.
func resolveInputs(flow *Flow, inputs map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(inputs))
	for key, value := range inputs {
		resolved[key] = value
	}
	var missing []string
	seen := make(map[string]bool)
	collect := func(value string) {
		for _, name := range inputReferences(value) {
			if seen[name] {
				continue
			}
			seen[name] = true
			if _, ok := resolved[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	for _, name := range flow.Inputs {
		if _, ok := resolved[name]; !ok {
			missing = append(missing, name)
		}
	}
	for index := range flow.Steps {
		step := &flow.Steps[index]
		switch {
		case step.Open != nil:
			collect(step.Open.URL)
		case step.Find != nil:
			collect(step.Find.Value)
		case step.Fill != nil:
			collect(step.Fill.Value)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required inputs: %s (pass them with --input %s=...)",
			strings.Join(uniqueStrings(missing), ", "), missing[0])
	}
	return resolved, nil
}

// inputReferences extracts {{name}} references from a value.
func inputReferences(value string) []string {
	var references []string
	rest := value
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			break
		}
		name := strings.TrimSpace(rest[start+2 : start+end])
		if name != "" {
			references = append(references, name)
		}
		rest = rest[start+end+2:]
	}
	return references
}

// substitute replaces every {{name}} reference with the resolved input.
func substitute(value string, inputs map[string]string) string {
	for name, replacement := range inputs {
		value = strings.ReplaceAll(value, "{{"+name+"}}", replacement)
	}
	return value
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

// enforceDomains is the hard domain constraint: every open URL's host must
// match the flow's domains list (exact or *.subdomain wildcard) before any
// step executes.
func enforceDomains(flow *Flow, inputs map[string]string) error {
	for index := range flow.Steps {
		step := &flow.Steps[index]
		if step.Open == nil {
			continue
		}
		raw := substitute(step.Open.URL, inputs)
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return &RunError{StepIndex: index, Action: "open", Message: fmt.Sprintf("cannot parse open URL %q", raw)}
		}
		if !domainAllowed(parsed.Host, flow.Domains) {
			return &RunError{
				StepIndex: index,
				Action:    "open",
				Message:   fmt.Sprintf("domain %q is not allowed by flow domains %v", parsed.Host, flow.Domains),
				Hint:      "add the domain to the flow's domains list or fix the URL",
			}
		}
	}
	return nil
}

// domainAllowed matches a host against exact entries and *.subdomain
// wildcards. Host:port forms compare against the bare host.
func domainAllowed(host string, domains []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if colon := strings.LastIndex(host, ":"); colon > 0 {
		host = host[:colon]
	}
	for _, pattern := range domains {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if colon := strings.LastIndex(pattern, ":"); colon > 0 {
			pattern = pattern[:colon]
		}
		if pattern == host {
			return true
		}
		if strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, strings.TrimPrefix(pattern, "*")) {
			return true
		}
	}
	return false
}

// executeStep runs one step through the executor.
func executeStep(ctx context.Context, executor Executor, session string, step *Step, inputs map[string]string) (any, error) {
	switch {
	case step.Open != nil:
		return executeOpen(ctx, executor, session, substitute(step.Open.URL, inputs))
	case step.Find != nil:
		return executeFind(ctx, executor, session, step.Find, inputs)
	case step.Click != nil:
		return executeClick(ctx, executor, session, step.Click)
	case step.Fill != nil:
		return executeFill(ctx, executor, session, step.Fill, inputs)
	case step.Wait != nil:
		return executeWait(ctx, executor, session, step.Wait)
	case step.Assert != nil:
		return executeAssert(ctx, executor, session, step.Assert)
	case step.Snapshot != nil:
		return executeSnapshot(ctx, executor, session, step.Snapshot)
	default:
		return nil, fmt.Errorf("unsupported step")
	}
}

func executeOpen(ctx context.Context, executor Executor, session, target string) (any, error) {
	return request(ctx, executor, session, "open", map[string]any{"url": target})
}

// finderRequest maps a semantic selector onto an engine find request.
func finderRequest(find *FindStep, value string) engine.FindRequest {
	request := engine.FindRequest{}
	switch {
	case find.Label != "":
		request.Kind = engine.FindLabel
		request.Query = find.Label
	case find.Role != "":
		request.Kind = engine.FindRole
		request.Query = find.Role
	case find.Text != "":
		request.Kind = engine.FindText
		request.Query = find.Text
	case find.Placeholder != "":
		request.Kind = engine.FindPlaceholder
		request.Query = find.Placeholder
	case find.Alt != "":
		request.Kind = engine.FindAlt
		request.Query = find.Alt
	case find.Title != "":
		request.Kind = engine.FindTitle
		request.Query = find.Title
	case find.TestID != "":
		request.Kind = engine.FindTestID
		request.Query = find.TestID
	}
	switch find.Action {
	case "click":
		request.Action = engine.FindClick
	case "fill":
		request.Action = engine.FindFill
	case "check":
		request.Action = engine.FindCheck
	case "hover":
		request.Action = engine.FindHover
	case "text":
		request.Action = engine.FindTextAction
	default:
		request.Action = engine.FindRef
	}
	request.Value = value
	request.Exact = find.Exact
	return request
}

func executeFind(ctx context.Context, executor Executor, session string, find *FindStep, inputs map[string]string) (any, error) {
	value := substitute(find.Value, inputs)
	return request(ctx, executor, session, "find", finderRequest(find, value))
}

// executeClick resolves the target semantically, scrolls it into view and
// clicks it. The two-step path (find → scrollintoview → click) keeps clicks
// reliable for elements below the fold.
func executeClick(ctx context.Context, executor Executor, session string, click *SelectorStep) (any, error) {
	find := engine.FindRequest{Action: engine.FindRef}
	switch {
	case click.Label != "":
		find.Kind = engine.FindLabel
		find.Query = click.Label
	case click.Role != "":
		find.Kind = engine.FindRole
		find.Query = click.Role
	case click.Text != "":
		find.Kind = engine.FindText
		find.Query = click.Text
	}
	find.Name = click.Name
	find.Exact = click.Exact
	result, err := request(ctx, executor, session, "find", find)
	if err != nil {
		return nil, err
	}
	ref, err := resultRef(result)
	if err != nil {
		return nil, err
	}
	if _, err := request(ctx, executor, session, "scrollintoview", engine.InteractionRequest{Action: engine.ActionScrollIntoView, Selector: "@" + ref}); err != nil {
		return nil, err
	}
	return request(ctx, executor, session, "click", engine.InteractionRequest{Action: engine.ActionClick, Selector: "@" + ref})
}

// executeFill resolves the target semantically, scrolls it into view and
// fills it.
func executeFill(ctx context.Context, executor Executor, session string, fill *FillStep, inputs map[string]string) (any, error) {
	value := substitute(fill.Value, inputs)
	find := engine.FindRequest{Action: engine.FindRef}
	switch {
	case fill.Label != "":
		find.Kind = engine.FindLabel
		find.Query = fill.Label
	case fill.Role != "":
		find.Kind = engine.FindRole
		find.Query = fill.Role
	case fill.Text != "":
		find.Kind = engine.FindText
		find.Query = fill.Text
	}
	find.Name = fill.Name
	find.Exact = fill.Exact
	result, err := request(ctx, executor, session, "find", find)
	if err != nil {
		return nil, err
	}
	ref, err := resultRef(result)
	if err != nil {
		return nil, err
	}
	if _, err := request(ctx, executor, session, "scrollintoview", engine.InteractionRequest{Action: engine.ActionScrollIntoView, Selector: "@" + ref}); err != nil {
		return nil, err
	}
	return request(ctx, executor, session, "fill", engine.InteractionRequest{Action: engine.ActionFill, Selector: "@" + ref, Value: value})
}

// resultRef extracts the ref from a find result payload.
func resultRef(result any) (string, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("decode find result: %w", err)
	}
	var payload struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode find result: %w", err)
	}
	if payload.Ref == "" {
		return "", errors.New("find returned no element ref")
	}
	return payload.Ref, nil
}

func executeWait(ctx context.Context, executor Executor, session string, wait *WaitStep) (any, error) {
	condition := engine.WaitCondition{}
	switch {
	case wait.URL != "":
		condition.Kind = engine.WaitURL
		condition.Value = wait.URL
	case wait.Visible != "":
		condition.Kind = engine.WaitSelector
		condition.Value = wait.Visible
		condition.SelectorState = engine.SelectorVisible
	default:
		condition.Kind = engine.WaitMilliseconds
		condition.Duration = time.Duration(wait.Ms) * time.Millisecond
	}
	return request(ctx, executor, session, "wait", condition)
}

func executeAssert(ctx context.Context, executor Executor, session string, assert *AssertStep) (any, error) {
	switch {
	case assert.Visible != "":
		find := engine.FindRequest{Kind: engine.FindText, Query: assert.Visible, Action: engine.FindFirst}
		found, err := request(ctx, executor, session, "find", find)
		if err != nil {
			return nil, fmt.Errorf("assert visible %q failed: %w", assert.Visible, err)
		}
		return found, nil
	case assert.URL != "":
		response, err := request(ctx, executor, session, "get.url", map[string]any{})
		if err != nil {
			return nil, err
		}
		current := responseString(response)
		if !globMatch(assert.URL, current) {
			return nil, fmt.Errorf("assert url %q failed: current url is %q", assert.URL, current)
		}
		return map[string]any{"expected": assert.URL, "actual": current}, nil
	case assert.Text != "":
		find := engine.FindRequest{Kind: engine.FindText, Query: assert.Text, Action: engine.FindFirst}
		found, err := request(ctx, executor, session, "find", find)
		if err != nil {
			return nil, fmt.Errorf("assert text %q failed: %w", assert.Text, err)
		}
		return found, nil
	case assert.Not != "":
		find := engine.FindRequest{Kind: engine.FindText, Query: assert.Not, Action: engine.FindFirst}
		_, err := request(ctx, executor, session, "find", find)
		if err == nil {
			return nil, fmt.Errorf("assert not %q failed: element is present", assert.Not)
		}
		return map[string]any{"absent": assert.Not}, nil
	default:
		return nil, fmt.Errorf("unsupported assert")
	}
}

func executeSnapshot(ctx context.Context, executor Executor, session string, snapshot *SnapshotStep) (any, error) {
	options := engine.SnapshotOptions{Compact: snapshot.Compact, Diff: snapshot.Diff}
	return request(ctx, executor, session, "snapshot", options)
}

// extractOutputs reads the declared outputs from the final state.
func extractOutputs(ctx context.Context, executor Executor, session string, flow *Flow) (map[string]string, error) {
	outputs := make(map[string]string, len(flow.Outputs))
	for _, output := range flow.Outputs {
		var value string
		var err error
		switch output.From {
		case "url":
			value, err = readOutputString(ctx, executor, session, "get.url", map[string]any{})
		case "html":
			value, err = readOutputString(ctx, executor, session, "get.html", map[string]any{})
		case "text":
			value, err = readOutputString(ctx, executor, session, "get.text", map[string]any{"selector": output.Path})
		case "attribute":
			parts := strings.SplitN(output.Path, "@", 2)
			if len(parts) != 2 {
				err = fmt.Errorf("output %q: attribute path must be selector@attribute", output.Name)
			} else {
				value, err = readOutputString(ctx, executor, session, "get.attr", map[string]any{"selector": parts[0], "attribute": parts[1]})
			}
		default:
			err = fmt.Errorf("output %q: unsupported source %q", output.Name, output.From)
		}
		if err != nil {
			return nil, fmt.Errorf("extract output %q: %w", output.Name, err)
		}
		outputs[output.Name] = value
	}
	return outputs, nil
}

func readOutputString(ctx context.Context, executor Executor, session, command string, args any) (string, error) {
	response, err := request(ctx, executor, session, command, args)
	if err != nil {
		return "", err
	}
	return responseString(response), nil
}

// request sends one frame and converts protocol failures into plain errors.
func request(ctx context.Context, executor Executor, session, command string, args any) (any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode %s args: %w", command, err)
	}
	response, err := executor(ctx, daemon.Frame{Cmd: command, Args: raw, Session: session})
	if err != nil {
		return nil, err
	}
	if !response.Success {
		if response.Error != nil {
			return nil, fmt.Errorf("%s", response.Error.Message)
		}
		return nil, fmt.Errorf("%s failed", command)
	}
	return response.Data, nil
}

// responseString extracts a string value from a protocol response payload.
func responseString(response any) string {
	raw, err := json.Marshal(response)
	if err != nil {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return strings.TrimSpace(string(raw))
}

// globMatch matches a URL glob pattern against an actual URL. ** matches any
// prefix (including slashes), * matches within a path segment. The pattern is
// anchored on both ends.
func globMatch(pattern, value string) bool {
	var builder strings.Builder
	builder.WriteString("^")
	rest := pattern
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "**"):
			builder.WriteString(".*")
			rest = rest[2:]
		case strings.HasPrefix(rest, "*"):
			builder.WriteString("[^/]*")
			rest = rest[1:]
		default:
			next := strings.IndexAny(rest, "*")
			if next < 0 {
				builder.WriteString(regexp.QuoteMeta(rest))
				rest = ""
			} else {
				builder.WriteString(regexp.QuoteMeta(rest[:next]))
				rest = rest[next:]
			}
		}
	}
	builder.WriteString("$")
	matched, err := regexp.MatchString(builder.String(), value)
	return err == nil && matched
}
