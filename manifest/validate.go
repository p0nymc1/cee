package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/p0nymc1/cee/stdlib"
)

// Severity distinguishes issues that make a manifest invalid from advisory
// ones that only warn.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Issue is a single validation finding.
type Issue struct {
	Severity Severity
	Message  string
}

// Report is the outcome of validating a manifest.
type Report struct {
	Issues []Issue
}

// OK reports whether the manifest is free of errors (warnings are allowed).
func (r Report) OK() bool {
	for _, issue := range r.Issues {
		if issue.Severity == Error {
			return false
		}
	}
	return true
}

func (r *Report) errf(format string, args ...any) {
	r.Issues = append(r.Issues, Issue{Error, fmt.Sprintf(format, args...)})
}

func (r *Report) warnf(format string, args ...any) {
	r.Issues = append(r.Issues, Issue{Warning, fmt.Sprintf(format, args...)})
}

// Validate statically checks a manifest's structural and reference
// integrity without executing anything, so a contributor can verify a
// plugin before it ever runs. A manifest that uses only standard-library
// actions can be validated completely; steps that reference domain-specific
// Go hooks can only be structurally checked here (hook existence is verified
// at Load time), which is called out as a warning.
//
// std supplies the known standard actions; pass stdlib.Default() for the
// built-ins. It may be nil, in which case every action_ref is treated as a
// custom hook.
func Validate(data []byte, std stdlib.Library) Report {
	var report Report

	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		report.errf("invalid JSON: %v", err)
		return report
	}
	if file.Name == "" {
		report.errf("missing domain name")
	}

	policyFallbacks := map[string]string{} // policyID -> fallbackStepRef
	for _, p := range file.Policies {
		if p.PolicyID == "" {
			report.errf("a policy is missing policy_id")
			continue
		}
		if p.FallbackStepRef == "" {
			report.errf("policy %q has no fallback_step_ref", p.PolicyID)
		}
		policyFallbacks[p.PolicyID] = p.FallbackStepRef
	}

	workflowIDs := map[string]bool{}
	for _, wf := range file.Workflows {
		workflowIDs[wf.WorkflowID] = true
	}

	for _, wf := range file.Workflows {
		validateWorkflow(&report, wf, workflowIDs, policyFallbacks, std)
	}

	validateSubWorkflowAcyclic(&report, file.Workflows)

	for _, intent := range file.Intents {
		if intent.NodeID == "" {
			report.errf("an intent is missing node_id")
		} else if !strings.Contains(intent.NodeID, ".") {
			report.warnf("intent node_id %q has no domain prefix (convention: <domain>.<name>)", intent.NodeID)
		}
		if intent.conflictingEntry() {
			report.errf(
				"intent %q sets both entry_workflow_ref (%q) and the deprecated entry_step_ref (%q) to different values; keep one",
				intent.NodeID, intent.EntryWorkflowRef, intent.DeprecatedEntryStepRef)
		} else if intent.DeprecatedEntryStepRef != "" {
			report.warnf(
				"intent %q uses entry_step_ref, which is deprecated and will be removed; rename it to entry_workflow_ref (the value is a workflow_id, which is what the old name got wrong)",
				intent.NodeID)
		}

		entry := intent.entryWorkflow()
		if entry == "" {
			report.errf("intent %q has no entry_workflow_ref", intent.NodeID)
		} else if !workflowIDs[entry] {
			report.errf("intent %q entry_workflow_ref %q does not match any workflow_id", intent.NodeID, entry)
		}
	}

	return report
}

func validateWorkflow(report *Report, wf WorkflowSpec, workflowIDs map[string]bool, policyFallbacks map[string]string, std stdlib.Library) {
	if wf.WorkflowID == "" {
		report.errf("a workflow is missing workflow_id")
	}

	stepIDs := map[string]bool{}
	for _, s := range wf.Steps {
		if s.StepID == "" {
			report.errf("workflow %q has a step with no step_id", wf.WorkflowID)
			continue
		}
		if stepIDs[s.StepID] {
			report.errf("workflow %q has duplicate step_id %q", wf.WorkflowID, s.StepID)
		}
		stepIDs[s.StepID] = true
	}

	if wf.EntryStepID == "" {
		report.errf("workflow %q has no entry_step_id", wf.WorkflowID)
	} else if !stepIDs[wf.EntryStepID] {
		report.errf("workflow %q entry_step_id %q is not one of its steps", wf.WorkflowID, wf.EntryStepID)
	} else {
		validateStepGraph(report, wf, stepIDs, policyFallbacks)
	}

	for _, s := range wf.Steps {
		validateStep(report, wf, s, stepIDs, workflowIDs, policyFallbacks, std)
	}
}

func validateStep(report *Report, wf WorkflowSpec, s StepSpec, stepIDs, workflowIDs map[string]bool, policyFallbacks map[string]string, std stdlib.Library) {
	where := fmt.Sprintf("workflow %q step %q", wf.WorkflowID, s.StepID)

	if s.OnSuccess != "" && !stepIDs[s.OnSuccess] {
		report.errf("%s: on_success %q is not a step in this workflow", where, s.OnSuccess)
	}

	if s.CompensateWith != "" {
		// A dangling compensation is worse than none: the run believes the
		// step is reversible and only finds out otherwise while abandoning.
		if !stepIDs[s.CompensateWith] {
			report.errf("%s: compensate_with %q is not a step in this workflow", where, s.CompensateWith)
		}
		if s.CompensateWith == s.StepID {
			report.errf("%s: compensate_with points at itself", where)
		}
		if s.Type == "composite" {
			report.errf("%s: only a leaf step can declare compensate_with", where)
		}
	}

	if s.CircuitBreakerPolicyRef != "" {
		fallback, ok := policyFallbacks[s.CircuitBreakerPolicyRef]
		if !ok {
			report.errf("%s: circuit_breaker_policy_ref %q is not a declared policy", where, s.CircuitBreakerPolicyRef)
		} else if fallback != "" && !stepIDs[fallback] {
			report.errf("%s: policy %q falls back to step %q, which does not exist in this workflow", where, s.CircuitBreakerPolicyRef, fallback)
		}
	}

	switch s.Type {
	case "leaf":
		if s.ActionRef == "" {
			report.errf("%s: leaf step has no action_ref", where)
			return
		}
		if factory, ok := std[s.ActionRef]; ok {
			if _, err := factory(s.With); err != nil {
				report.errf("%s: action %q misconfigured: %v", where, s.ActionRef, err)
			}
		} else {
			report.warnf("%s: action_ref %q is not a standard action; its existence is verified against Go hooks at load time", where, s.ActionRef)
		}
	case "composite":
		if s.SubWorkflowRef == "" {
			report.errf("%s: composite step has no sub_workflow_ref", where)
		} else if !workflowIDs[s.SubWorkflowRef] {
			report.errf("%s: sub_workflow_ref %q does not match any workflow_id", where, s.SubWorkflowRef)
		}
	default:
		report.errf("%s: unknown type %q (want \"leaf\" or \"composite\")", where, s.Type)
	}
}

// validateStepGraph checks the shapes that make Engine.Run runaway or leave
// dead weight in a DAG: a cycle the happy path can spin on forever, and steps
// nothing can ever reach.
//
// Two edge kinds exist between steps. on_success is the happy path, taken
// whenever a step succeeds; a cycle over those edges alone is unconditionally
// a hang, so it is an error. A policy's fallback_step_ref is only taken when
// a step fails, so a cycle that needs a fallback edge to close requires
// repeated failure to actually spin -- reachable, but not certain, so it is a
// warning rather than an error.
func validateStepGraph(report *Report, wf WorkflowSpec, stepIDs map[string]bool, policyFallbacks map[string]string) {
	successEdges := map[string][]string{}
	allEdges := map[string][]string{}
	var order []string

	for _, s := range wf.Steps {
		if s.StepID == "" {
			continue
		}
		order = append(order, s.StepID)
		if s.OnSuccess != "" && stepIDs[s.OnSuccess] {
			successEdges[s.StepID] = append(successEdges[s.StepID], s.OnSuccess)
			allEdges[s.StepID] = append(allEdges[s.StepID], s.OnSuccess)
		}
		if fallback, ok := policyFallbacks[s.CircuitBreakerPolicyRef]; ok && stepIDs[fallback] {
			allEdges[s.StepID] = append(allEdges[s.StepID], fallback)
		}
	}

	if cycle := findCycle(order, successEdges); cycle != nil {
		report.errf("workflow %q has an on_success cycle: %s -- Engine.Run would spin until it hits the step limit",
			wf.WorkflowID, strings.Join(cycle, " -> "))
	} else if cycle := findCycle(order, allEdges); cycle != nil {
		report.warnf("workflow %q can loop through a circuit breaker fallback: %s -- only spins if the steps keep failing, but it will not terminate on its own if they do",
			wf.WorkflowID, strings.Join(cycle, " -> "))
	}

	reached := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if reached[id] {
			return
		}
		reached[id] = true
		for _, next := range allEdges[id] {
			walk(next)
		}
	}
	walk(wf.EntryStepID)

	for _, id := range order {
		if !reached[id] {
			report.warnf("workflow %q step %q is unreachable from entry_step_id %q", wf.WorkflowID, id, wf.EntryStepID)
		}
	}
}

// validateSubWorkflowAcyclic checks composite nesting across the manifest's
// workflows. Unlike an on_success cycle -- which merely spins -- a
// sub_workflow_ref cycle recurses until the Go runtime aborts the process
// with a stack overflow that cannot be recovered, so it is always an error.
func validateSubWorkflowAcyclic(report *Report, workflows []WorkflowSpec) {
	edges := map[string][]string{}
	var order []string
	known := map[string]bool{}
	for _, wf := range workflows {
		known[wf.WorkflowID] = true
	}
	for _, wf := range workflows {
		order = append(order, wf.WorkflowID)
		for _, s := range wf.Steps {
			if s.Type == "composite" && s.SubWorkflowRef != "" && known[s.SubWorkflowRef] {
				edges[wf.WorkflowID] = append(edges[wf.WorkflowID], s.SubWorkflowRef)
			}
		}
	}

	if cycle := findCycle(order, edges); cycle != nil {
		report.errf("sub_workflow_ref cycle: %s -- Engine.Run would recurse until the process dies of stack overflow",
			strings.Join(cycle, " -> "))
	}
}

// findCycle returns one cycle as the path that closes it (first node repeated
// at the end), or nil when the graph is acyclic. Nodes are visited in the
// given order so the result is stable across runs.
func findCycle(nodes []string, edges map[string][]string) []string {
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := map[string]int{}
	var path []string

	var visit func(string) []string
	visit = func(node string) []string {
		state[node] = onStack
		path = append(path, node)
		for _, next := range edges[node] {
			switch state[next] {
			case onStack:
				// Trim the prefix that leads into the cycle but is not part
				// of it, so the report shows the loop and nothing else.
				for i, n := range path {
					if n == next {
						return append(append([]string{}, path[i:]...), next)
					}
				}
			case unvisited:
				if cycle := visit(next); cycle != nil {
					return cycle
				}
			}
		}
		path = path[:len(path)-1]
		state[node] = done
		return nil
	}

	for _, node := range nodes {
		if state[node] == unvisited {
			path = path[:0]
			if cycle := visit(node); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

// String renders a report as human-readable lines, errors first.
func (r Report) String() string {
	if len(r.Issues) == 0 {
		return "ok: no issues"
	}
	issues := make([]Issue, len(r.Issues))
	copy(issues, r.Issues)
	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].Severity == Error && issues[j].Severity == Warning
	})
	var b strings.Builder
	for _, issue := range issues {
		fmt.Fprintf(&b, "[%s] %s\n", issue.Severity, issue.Message)
	}
	return strings.TrimRight(b.String(), "\n")
}
