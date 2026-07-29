// Package execution implements the Deterministic Execution Engine (DEE): the
// DAG walker at CEE's core. Steps are either atomic (LeafStep) or a
// reference to a nested workflow (CompositeStep). Every leaf step optionally
// gates on a sandbox probe before it runs, and optionally declares a circuit
// breaker policy to consult if the probe or the action itself fails --
// there is no blind retry, and no LLM anywhere in this loop.
package execution

import (
	"fmt"

	"cee/entities"
)

// CircuitBreakerTripped is returned when a step fails and no fallback policy
// is registered for it.
type CircuitBreakerTripped struct {
	StepID string
	Reason string
}

func (e *CircuitBreakerTripped) Error() string {
	return fmt.Sprintf("circuit breaker tripped at step %q: %s", e.StepID, e.Reason)
}

// Step is the shape every node in a workflow's step map satisfies. It is
// deliberately only implemented by LeafStep and CompositeStep in this
// package -- a workflow's DAG is one of exactly these two step kinds.
type Step interface {
	ID() string
	circuitBreakerPolicyRef() string
}

// Action performs a leaf step's deterministic work: context in, partial
// context out.
type Action func(ctx map[string]any) (map[string]any, error)

// LeafStep is an atomic, deterministic unit of work.
type LeafStep struct {
	StepID                  string
	Run                     Action
	SandboxProbeRef         string
	CircuitBreakerPolicyRef string
	OnSuccess               string
}

func (s *LeafStep) ID() string                      { return s.StepID }
func (s *LeafStep) circuitBreakerPolicyRef() string { return s.CircuitBreakerPolicyRef }

// CompositeStep points at a named sub-workflow, letting DAGs nest and reuse
// common sub-flows instead of flattening every step to one grain.
type CompositeStep struct {
	StepID                  string
	SubWorkflowRef          string
	CircuitBreakerPolicyRef string
	OnSuccess               string
}

func (s *CompositeStep) ID() string                      { return s.StepID }
func (s *CompositeStep) circuitBreakerPolicyRef() string { return s.CircuitBreakerPolicyRef }

// CircuitBreakerPolicy is a named fallback. Steps declare a policy by
// reference rather than inlining retry/fallback literals, so every safety
// net in the system stays auditable from one registry.
type CircuitBreakerPolicy struct {
	PolicyID        string
	FallbackStepRef string
}

// Workflow is a Step DAG plus its entry point.
type Workflow struct {
	WorkflowID  string
	EntryStepID string
	Steps       map[string]Step
}

// Prober is the interface the engine expects from a pre-execution sandbox.
type Prober interface {
	Probe(entities.ProbeRequest) (entities.ProbeResult, error)
}

// Engine walks a registered Workflow's Step DAG to completion. Sandbox
// probing and circuit-breaking are the only two ways a step's forward
// progress can be redirected.
type Engine struct {
	sandbox   Prober
	workflows map[string]*Workflow
	policies  map[string]CircuitBreakerPolicy
}

// NewEngine builds an Engine. sandbox may be nil as long as no registered
// step ever declares a SandboxProbeRef.
func NewEngine(sandbox Prober) *Engine {
	return &Engine{
		sandbox:   sandbox,
		workflows: make(map[string]*Workflow),
		policies:  make(map[string]CircuitBreakerPolicy),
	}
}

func (e *Engine) RegisterWorkflow(w *Workflow) {
	e.workflows[w.WorkflowID] = w
}

func (e *Engine) RegisterPolicy(p CircuitBreakerPolicy) {
	e.policies[p.PolicyID] = p
}

// Run walks workflowRef's Step DAG starting from its entry step, threading
// context through each step's output.
func (e *Engine) Run(workflowRef string, ctx map[string]any) (entities.WorkflowResult, error) {
	workflow, ok := e.workflows[workflowRef]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf("no workflow registered for %q", workflowRef)
	}

	stepID := workflow.EntryStepID
	var trace []string

	for stepID != "" {
		step, ok := workflow.Steps[stepID]
		if !ok {
			return entities.WorkflowResult{}, fmt.Errorf("workflow %q has no step %q", workflowRef, stepID)
		}
		trace = append(trace, step.ID())

		switch s := step.(type) {
		case *CompositeStep:
			subResult, err := e.Run(s.SubWorkflowRef, ctx)
			if err != nil {
				next, breakErr := e.onFailure(s, err.Error())
				if breakErr != nil {
					return entities.WorkflowResult{}, breakErr
				}
				stepID = next
				continue
			}
			trace = append(trace, subResult.Trace...)
			ctx = merge(ctx, subResult.Output)
			stepID = s.OnSuccess

		case *LeafStep:
			if s.SandboxProbeRef != "" {
				if e.sandbox == nil {
					return entities.WorkflowResult{}, fmt.Errorf(
						"step %q declares sandbox_probe_ref %q but no sandbox is configured",
						s.StepID, s.SandboxProbeRef,
					)
				}
				probeResult, err := e.sandbox.Probe(entities.ProbeRequest{
					ProbeRef:    s.SandboxProbeRef,
					DomainID:    workflowRef,
					StepContext: ctx,
				})
				if err != nil || !probeResult.Healthy {
					reason := probeResult.DetectedFailureMode
					if err != nil {
						reason = err.Error()
					}
					next, breakErr := e.onFailure(s, reason)
					if breakErr != nil {
						return entities.WorkflowResult{}, breakErr
					}
					stepID = next
					continue
				}
			}

			output, err := s.Run(ctx)
			if err != nil {
				next, breakErr := e.onFailure(s, err.Error())
				if breakErr != nil {
					return entities.WorkflowResult{}, breakErr
				}
				stepID = next
				continue
			}
			ctx = merge(ctx, output)
			stepID = s.OnSuccess

		default:
			return entities.WorkflowResult{}, fmt.Errorf("unknown step type for %q", stepID)
		}
	}

	return entities.WorkflowResult{Output: ctx, StatePointer: workflowRef, Trace: trace}, nil
}

func (e *Engine) onFailure(step Step, reason string) (string, error) {
	if ref := step.circuitBreakerPolicyRef(); ref != "" {
		if policy, ok := e.policies[ref]; ok && policy.FallbackStepRef != "" {
			return policy.FallbackStepRef, nil
		}
	}
	return "", &CircuitBreakerTripped{StepID: step.ID(), Reason: reason}
}

func merge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}
