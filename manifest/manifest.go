package manifest

import (
	"encoding/json"
	"fmt"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/stdlib"
)

type File struct {
	Name      string         `json:"name"`
	Intents   []IntentSpec   `json:"intents"`
	Policies  []PolicySpec   `json:"policies"`
	Workflows []WorkflowSpec `json:"workflows"`
}

type IntentSpec struct {
	NodeID   string   `json:"node_id"`
	Examples []string `json:"examples"`

	EntryWorkflowRef string `json:"entry_workflow_ref,omitempty"`

	DeprecatedEntryStepRef string `json:"entry_step_ref,omitempty"`
}

func (s IntentSpec) entryWorkflow() string {
	if s.EntryWorkflowRef != "" {
		return s.EntryWorkflowRef
	}
	return s.DeprecatedEntryStepRef
}

func (s IntentSpec) conflictingEntry() bool {
	return s.EntryWorkflowRef != "" &&
		s.DeprecatedEntryStepRef != "" &&
		s.EntryWorkflowRef != s.DeprecatedEntryStepRef
}

type PolicySpec struct {
	PolicyID        string `json:"policy_id"`
	FallbackStepRef string `json:"fallback_step_ref"`
}

type WorkflowSpec struct {
	WorkflowID  string     `json:"workflow_id"`
	EntryStepID string     `json:"entry_step_id"`
	Steps       []StepSpec `json:"steps"`
}

type StepSpec struct {
	StepID                  string         `json:"step_id"`
	Type                    string         `json:"type"`
	ActionRef               string         `json:"action_ref,omitempty"`
	With                    map[string]any `json:"with,omitempty"`
	SandboxProbeRef         string         `json:"sandbox_probe_ref,omitempty"`
	CircuitBreakerPolicyRef string         `json:"circuit_breaker_policy_ref,omitempty"`
	OnSuccess               string         `json:"on_success,omitempty"`
	SubWorkflowRef          string         `json:"sub_workflow_ref,omitempty"`

	CompensateWith string `json:"compensate_with,omitempty"`
}

type Hooks map[string]execution.Action

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
		if intent.conflictingEntry() {
			return nil, fmt.Errorf(
				"manifest: domain %q intent %q sets both entry_workflow_ref (%q) and the deprecated entry_step_ref (%q) to different values; keep one",
				file.Name, intent.NodeID, intent.EntryWorkflowRef, intent.DeprecatedEntryStepRef,
			)
		}
		domain.Intents = append(domain.Intents, entities.IntentNode{
			NodeID:           intent.NodeID,
			DomainID:         file.Name,
			Examples:         intent.Examples,
			EntryWorkflowRef: intent.entryWorkflow(),
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
			CompensateStepRef:       spec.CompensateWith,
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
