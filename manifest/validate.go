package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cee/stdlib"
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

	for _, intent := range file.Intents {
		if intent.NodeID == "" {
			report.errf("an intent is missing node_id")
		} else if !strings.Contains(intent.NodeID, ".") {
			report.warnf("intent node_id %q has no domain prefix (convention: <domain>.<name>)", intent.NodeID)
		}
		if intent.EntryStepRef == "" {
			report.errf("intent %q has no entry_step_ref", intent.NodeID)
		} else if !workflowIDs[intent.EntryStepRef] {
			report.errf("intent %q entry_step_ref %q does not match any workflow_id", intent.NodeID, intent.EntryStepRef)
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
