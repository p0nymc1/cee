// Package execution implements the Deterministic Execution Engine (DEE): the
// DAG walker at CEE's core. Steps are either atomic (LeafStep) or a
// reference to a nested workflow (CompositeStep). Every leaf step optionally
// gates on a sandbox probe before it runs, and optionally declares a circuit
// breaker policy to consult if the probe or the action itself fails --
// there is no blind retry, and no LLM anywhere in this loop.
package execution

import (
	"errors"
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

// Default ceilings on a single Run. They exist to make a malformed DAG fail
// as a reportable error rather than as a hang or a process kill: an
// on_success cycle would otherwise spin forever, and a sub_workflow_ref
// cycle would otherwise recurse until the Go runtime aborts with a stack
// overflow, which cannot be recovered. Both are set well above any
// legitimate workflow -- a DAG walk normally visits each step at most once.
const (
	DefaultMaxSteps = 10_000
	DefaultMaxDepth = 64
)

// StepLimitExceeded is returned when one workflow executes more steps than
// the engine's ceiling allows -- in practice, an on_success cycle.
// RecentSteps holds the tail of the trace, which is where the cycle is.
type StepLimitExceeded struct {
	WorkflowID  string
	Limit       int
	RecentSteps []string
}

func (e *StepLimitExceeded) Error() string {
	return fmt.Sprintf(
		"workflow %q exceeded the %d step limit (likely an on_success cycle); most recent steps: %v",
		e.WorkflowID, e.Limit, e.RecentSteps,
	)
}

// DepthLimitExceeded is returned when CompositeStep nesting goes deeper than
// the engine's ceiling -- in practice, a sub_workflow_ref cycle.
type DepthLimitExceeded struct {
	WorkflowID string
	Limit      int
}

func (e *DepthLimitExceeded) Error() string {
	return fmt.Sprintf(
		"workflow %q exceeded the %d sub-workflow nesting limit (likely a sub_workflow_ref cycle)",
		e.WorkflowID, e.Limit,
	)
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

// Workflow is a Step DAG plus its entry point. DomainID names the domain
// that contributed it and is passed through to sandbox probes so a prober
// can scope itself per domain; registry.RegisterDomain stamps it from the
// domain's own name, so a workflow registered directly on the Engine (as in
// tests) simply carries no domain.
type Workflow struct {
	WorkflowID  string
	DomainID    string
	EntryStepID string
	Steps       map[string]Step
}

// Prober is the interface the engine expects from a pre-execution sandbox.
type Prober interface {
	Probe(entities.ProbeRequest) (entities.ProbeResult, error)
}

// Observer receives one callback per engine event so a scorecard recorder
// can measure a run without the engine depending on any metrics package.
// Every leaf step the engine executes is, by construction, a deterministic
// operation -- that is exactly the quantity a scorecard needs to prove how
// many LLM calls a naive per-step agent would have made and CEE did not.
type Observer interface {
	ObserveStep(workflowID, stepID string)
	ObserveSandboxProbe(workflowID, stepID string)
	ObserveCircuitBreaker(workflowID, stepID string)
}

// Engine walks a registered Workflow's Step DAG to completion. Sandbox
// probing and circuit-breaking are the only two ways a step's forward
// progress can be redirected.
type Engine struct {
	sandbox   Prober
	observer  Observer
	workflows map[string]*Workflow
	policies  map[string]CircuitBreakerPolicy
	maxSteps  int
	maxDepth  int
}

// NewEngine builds an Engine. sandbox may be nil as long as no registered
// step ever declares a SandboxProbeRef.
func NewEngine(sandbox Prober) *Engine {
	return &Engine{
		sandbox:   sandbox,
		workflows: make(map[string]*Workflow),
		policies:  make(map[string]CircuitBreakerPolicy),
		maxSteps:  DefaultMaxSteps,
		maxDepth:  DefaultMaxDepth,
	}
}

// SetLimits overrides the runaway ceilings. A non-positive value leaves that
// ceiling at its default -- the ceilings cannot be switched off, because an
// unbounded walk is a process-level hazard rather than a workflow-level one.
func (e *Engine) SetLimits(maxSteps, maxDepth int) {
	if maxSteps > 0 {
		e.maxSteps = maxSteps
	}
	if maxDepth > 0 {
		e.maxDepth = maxDepth
	}
}

// SetObserver attaches an Observer for metrics collection. Passing nil (the
// default) disables observation with zero overhead.
func (e *Engine) SetObserver(o Observer) {
	e.observer = o
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
	return e.run(workflowRef, ctx, 0)
}

// run is Run plus the current sub-workflow nesting depth. Steps are counted
// per workflow, depth across them.
func (e *Engine) run(workflowRef string, ctx map[string]any, depth int) (entities.WorkflowResult, error) {
	if depth > e.maxDepth {
		return entities.WorkflowResult{}, &DepthLimitExceeded{WorkflowID: workflowRef, Limit: e.maxDepth}
	}

	workflow, ok := e.workflows[workflowRef]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf("no workflow registered for %q", workflowRef)
	}

	stepID := workflow.EntryStepID
	var trace []string
	steps := 0

	for stepID != "" {
		steps++
		if steps > e.maxSteps {
			return entities.WorkflowResult{}, &StepLimitExceeded{
				WorkflowID:  workflowRef,
				Limit:       e.maxSteps,
				RecentSteps: tail(trace, 10),
			}
		}

		step, ok := workflow.Steps[stepID]
		if !ok {
			return entities.WorkflowResult{}, fmt.Errorf("workflow %q has no step %q", workflowRef, stepID)
		}
		trace = append(trace, step.ID())

		switch s := step.(type) {
		case *CompositeStep:
			subResult, err := e.run(s.SubWorkflowRef, ctx, depth+1)
			if err != nil {
				// A runaway sub-workflow is a defect in the DAG's shape, not
				// a business failure the breaker exists to absorb. Letting a
				// fallback swallow it would hide the bug and let the outer
				// loop re-enter the same broken sub-workflow, so it goes
				// straight up instead.
				if isRunaway(err) {
					return entities.WorkflowResult{}, err
				}
				e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
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
				e.observe(func(o Observer) { o.ObserveSandboxProbe(workflow.WorkflowID, s.StepID) })
				probeResult, err := e.sandbox.Probe(entities.ProbeRequest{
					ProbeRef:    s.SandboxProbeRef,
					DomainID:    workflow.DomainID,
					StepContext: ctx,
				})
				if err != nil || !probeResult.Healthy {
					reason := probeResult.DetectedFailureMode
					if err != nil {
						reason = err.Error()
					}
					e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
					next, breakErr := e.onFailure(s, reason)
					if breakErr != nil {
						return entities.WorkflowResult{}, breakErr
					}
					stepID = next
					continue
				}
			}

			e.observe(func(o Observer) { o.ObserveStep(workflow.WorkflowID, s.StepID) })
			output, err := s.Run(ctx)
			if err != nil {
				e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
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

// observe invokes fn with the attached observer, if any. Keeping the nil
// check in one place lets the Run loop stay readable.
func (e *Engine) observe(fn func(Observer)) {
	if e.observer != nil {
		fn(e.observer)
	}
}

func (e *Engine) onFailure(step Step, reason string) (string, error) {
	if ref := step.circuitBreakerPolicyRef(); ref != "" {
		if policy, ok := e.policies[ref]; ok && policy.FallbackStepRef != "" {
			return policy.FallbackStepRef, nil
		}
	}
	return "", &CircuitBreakerTripped{StepID: step.ID(), Reason: reason}
}

// isRunaway reports whether err is one of the structural ceilings, which
// bypass circuit breakers entirely.
func isRunaway(err error) bool {
	var stepLimit *StepLimitExceeded
	var depthLimit *DepthLimitExceeded
	return errors.As(err, &stepLimit) || errors.As(err, &depthLimit)
}

// tail returns at most the last n elements of s.
func tail(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
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
