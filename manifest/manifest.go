// Package manifest loads a domain plugin's declarative shape (JSON) and
// binds it to behavior, producing a registry.Domain ready to hand to
// registry.Registry.RegisterDomain.
//
// A manifest only ever describes *shape*: which steps exist, how they
// connect, and which named action each one runs. Behavior comes from two
// places, both bound by name:
//
//   - the standard action library (stdlib), parameterized purely in JSON via
//     each step's "with" block -- the no-code path; and
//   - a domain's own Go hooks, for logic the standard library can't express.
//
// A manifest can never embed behavior, so it is safe to author (or generate
// from a future drag-and-drop editor) without granting it anything beyond
// the actions it references.
//
// JSON rather than YAML is used deliberately: it keeps this package (and the
// whole cee module) dependency-free. A YAML front-end can be layered on top
// later -- YAML unmarshals cleanly into the same File struct.
package manifest

import (
	"encoding/json"
	"fmt"

	"cee/entities"
	"cee/execution"
	"cee/registry"
	"cee/stdlib"
)

// File is the on-disk shape of a domain manifest.
type File struct {
	Name      string         `json:"name"`
	Intents   []IntentSpec   `json:"intents"`
	Policies  []PolicySpec   `json:"policies"`
	Workflows []WorkflowSpec `json:"workflows"`
}

// IntentSpec declares one matchable intent, scoped to this manifest's
// domain.
type IntentSpec struct {
	NodeID       string   `json:"node_id"`
	Examples     []string `json:"examples"`
	EntryStepRef string   `json:"entry_step_ref"`
}

// PolicySpec declares one named circuit breaker fallback.
type PolicySpec struct {
	PolicyID        string `json:"policy_id"`
	FallbackStepRef string `json:"fallback_step_ref"`
}

// WorkflowSpec declares one Step DAG.
type WorkflowSpec struct {
	WorkflowID  string     `json:"workflow_id"`
	EntryStepID string     `json:"entry_step_id"`
	Steps       []StepSpec `json:"steps"`
}

// StepSpec describes one node in a workflow's DAG. Type must be "leaf" or
// "composite"; which of the remaining fields matter depends on which. With
// carries the parameters for a standard-library action_ref.
type StepSpec struct {
	StepID                  string         `json:"step_id"`
	Type                    string         `json:"type"`
	ActionRef               string         `json:"action_ref,omitempty"`
	With                    map[string]any `json:"with,omitempty"`
	SandboxProbeRef         string         `json:"sandbox_probe_ref,omitempty"`
	CircuitBreakerPolicyRef string         `json:"circuit_breaker_policy_ref,omitempty"`
	OnSuccess               string         `json:"on_success,omitempty"`
	SubWorkflowRef          string         `json:"sub_workflow_ref,omitempty"`
}

// Hooks is the set of named Go functions a manifest's action_ref fields may
// resolve to when the standard library does not cover the behavior. The
// domain author builds this map in code; Load fails loudly if a manifest
// references a name found in neither the standard library nor here.
type Hooks map[string]execution.Action

// Load parses a manifest and binds every leaf step's action_ref -- against
// the standard library first (using the step's "with" params), then against
// hooks -- returning a registry.Domain ready to register. Either std or
// hooks may be nil.
func Load(data []byte, hooks Hooks, std stdlib.Library) (*registry.Domain, error) {
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("manifest: invalid JSON: %w", err)
	}
	if file.Name == "" {
		return nil, fmt.Errorf("manifest: missing domain name")
	}

	domain := &registry.Domain{Name: file.Name}

	for _, intent := range file.Intents {
		domain.Intents = append(domain.Intents, entities.IntentNode{
			NodeID:       intent.NodeID,
			DomainID:     file.Name,
			Examples:     intent.Examples,
			EntryStepRef: intent.EntryStepRef,
		})
	}

	for _, policy := range file.Policies {
		domain.Policies = append(domain.Policies, execution.CircuitBreakerPolicy{
			PolicyID:        policy.PolicyID,
			FallbackStepRef: policy.FallbackStepRef,
		})
	}

	for _, wf := range file.Workflows {
		steps := make(map[string]execution.Step, len(wf.Steps))
		for _, stepSpec := range wf.Steps {
			step, err := buildStep(file.Name, wf.WorkflowID, stepSpec, hooks, std)
			if err != nil {
				return nil, err
			}
			steps[stepSpec.StepID] = step
		}
		domain.Workflows = append(domain.Workflows, &execution.Workflow{
			WorkflowID:  wf.WorkflowID,
			EntryStepID: wf.EntryStepID,
			Steps:       steps,
		})
	}

	return domain, nil
}

func buildStep(domainName, workflowID string, spec StepSpec, hooks Hooks, std stdlib.Library) (execution.Step, error) {
	switch spec.Type {
	case "leaf":
		action, err := resolveAction(domainName, workflowID, spec, hooks, std)
		if err != nil {
			return nil, err
		}
		return &execution.LeafStep{
			StepID:                  spec.StepID,
			Run:                     action,
			SandboxProbeRef:         spec.SandboxProbeRef,
			CircuitBreakerPolicyRef: spec.CircuitBreakerPolicyRef,
			OnSuccess:               spec.OnSuccess,
		}, nil
	case "composite":
		if spec.SubWorkflowRef == "" {
			return nil, fmt.Errorf(
				"manifest: domain %q workflow %q step %q is composite but has no sub_workflow_ref",
				domainName, workflowID, spec.StepID,
			)
		}
		return &execution.CompositeStep{
			StepID:                  spec.StepID,
			SubWorkflowRef:          spec.SubWorkflowRef,
			CircuitBreakerPolicyRef: spec.CircuitBreakerPolicyRef,
			OnSuccess:               spec.OnSuccess,
		}, nil
	default:
		return nil, fmt.Errorf(
			"manifest: domain %q workflow %q step %q has unknown type %q (want \"leaf\" or \"composite\")",
			domainName, workflowID, spec.StepID, spec.Type,
		)
	}
}

// resolveAction binds a leaf step's action_ref: standard library first
// (parameterized by With), then the domain's own hooks.
func resolveAction(domainName, workflowID string, spec StepSpec, hooks Hooks, std stdlib.Library) (execution.Action, error) {
	if factory, ok := std[spec.ActionRef]; ok {
		action, err := factory(spec.With)
		if err != nil {
			return nil, fmt.Errorf(
				"manifest: domain %q workflow %q step %q action %q: %w",
				domainName, workflowID, spec.StepID, spec.ActionRef, err,
			)
		}
		return action, nil
	}
	if action, ok := hooks[spec.ActionRef]; ok {
		return action, nil
	}
	return nil, fmt.Errorf(
		"manifest: domain %q workflow %q step %q references unregistered action_ref %q (not in the standard library or the provided hooks)",
		domainName, workflowID, spec.StepID, spec.ActionRef,
	)
}
