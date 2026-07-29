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

	"github.com/cee-project/cee/entities"
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
	store     Store
	maxSteps  int
	maxDepth  int
}

// SetStore attaches the Store that suspended runs are parked in. Without
// one, a step that suspends fails loudly rather than silently behaving like
// an ordinary failure. Use NewMemoryStore for development.
func (e *Engine) SetStore(s Store) {
	e.store = s
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
	return e.runFrom(workflowRef, "", ctx, depth)
}

// runFrom walks a workflow starting at startStepID, or at the workflow's
// declared entry step when startStepID is empty. Resume uses the explicit
// form to re-enter a DAG partway through.
func (e *Engine) runFrom(workflowRef, startStepID string, ctx map[string]any, depth int) (entities.WorkflowResult, error) {
	if depth > e.maxDepth {
		return entities.WorkflowResult{}, &DepthLimitExceeded{WorkflowID: workflowRef, Limit: e.maxDepth}
	}

	workflow, ok := e.workflows[workflowRef]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf("no workflow registered for %q", workflowRef)
	}

	stepID := startStepID
	if stepID == "" {
		stepID = workflow.EntryStepID
	}
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
				// A runaway or misconfigured sub-workflow is a defect in the
				// DAG's shape, not a business failure the breaker exists to
				// absorb. Letting a fallback swallow it would hide the bug
				// and let the outer loop re-enter the same broken
				// sub-workflow, so it goes straight up instead.
				if bypassesBreaker(err) {
					return entities.WorkflowResult{}, err
				}
				e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
				next, nextCtx, breakErr := e.onFailure(s, err.Error(), ctx)
				if breakErr != nil {
					return entities.WorkflowResult{}, breakErr
				}
				ctx = nextCtx
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
					next, nextCtx, breakErr := e.onFailure(s, reason, ctx)
					if breakErr != nil {
						return entities.WorkflowResult{}, breakErr
					}
					ctx = nextCtx
					stepID = next
					continue
				}
			}

			e.observe(func(o Observer) { o.ObserveStep(workflow.WorkflowID, s.StepID) })
			output, err := s.Run(ctx)
			if err != nil {
				// A suspension is a pause, not a fault: it must reach the
				// caller with a resume pointer rather than be absorbed by a
				// breaker as though the step had failed.
				var suspended *Suspended
				if errors.As(err, &suspended) {
					return e.suspend(workflow, s.StepID, suspended, ctx, trace, depth)
				}
				e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
				next, nextCtx, breakErr := e.onFailure(s, err.Error(), ctx)
				if breakErr != nil {
					return entities.WorkflowResult{}, breakErr
				}
				ctx = nextCtx
				stepID = next
				continue
			}
			ctx = merge(ctx, output)
			stepID = s.OnSuccess

		default:
			return entities.WorkflowResult{}, fmt.Errorf("unknown step type for %q", stepID)
		}
	}

	// A run that reached the end has nothing to resume, so it carries no
	// pointer. StatePointer used to echo workflowRef here, from before
	// suspension existed, which left the field meaning two different things
	// -- a resume token when parked, an identifier when finished -- and made
	// the obvious "did this park?" test silently wrong for every completed
	// run.
	return entities.WorkflowResult{Output: ctx, Trace: trace}, nil
}

// suspend parks a run: it saves the context at the suspension point and
// reports the pointer to resume from. The returned WorkflowResult carries
// the context accumulated so far, so a caller can act on partial output
// (for instance, tell an operator what is awaiting their decision) without
// loading the state back.
func (e *Engine) suspend(
	workflow *Workflow, stepID string, s *Suspended,
	ctx map[string]any, trace []string, depth int,
) (entities.WorkflowResult, error) {
	if depth > 0 {
		return entities.WorkflowResult{}, &NestedSuspensionUnsupported{
			WorkflowID: workflow.WorkflowID, StepID: stepID,
		}
	}
	if e.store == nil {
		return entities.WorkflowResult{}, &NoSuspensionSupport{
			WorkflowID: workflow.WorkflowID, StepID: stepID,
		}
	}

	pointer, err := newPointer()
	if err != nil {
		return entities.WorkflowResult{}, err
	}
	state := State{
		Pointer:    pointer,
		WorkflowID: workflow.WorkflowID,
		StepID:     stepID,
		Reason:     s.Reason,
		Ctx:        ctx,
		Trace:      trace,
	}
	if err := e.store.Save(state); err != nil {
		return entities.WorkflowResult{}, fmt.Errorf("could not save suspended workflow: %w", err)
	}

	if so, ok := e.observer.(SuspensionObserver); ok {
		so.ObserveSuspension(workflow.WorkflowID, stepID)
	}
	return entities.WorkflowResult{Output: ctx, StatePointer: pointer, Trace: trace}, nil
}

// Resume continues a suspended run from the pointer Run reported. resolution
// carries whatever the external event decided (an approval, a callback
// payload) and is merged into the saved context, so the step after the
// suspension point can branch on it like any other context field.
//
// Execution restarts at the suspended step's OnSuccess: waiting is over, so
// the suspension point itself does not run again. A pointer is single-use --
// it is deleted once resumed, so the same decision cannot be replayed.
func (e *Engine) Resume(pointer string, resolution map[string]any) (entities.WorkflowResult, error) {
	if e.store == nil {
		return entities.WorkflowResult{}, fmt.Errorf("no Store is configured; call Engine.SetStore first")
	}
	state, err := e.store.Load(pointer)
	if err != nil {
		return entities.WorkflowResult{}, err
	}

	workflow, ok := e.workflows[state.WorkflowID]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf(
			"suspended workflow %q is no longer registered", state.WorkflowID)
	}
	step, ok := workflow.Steps[state.StepID]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf(
			"workflow %q no longer has the suspended step %q", state.WorkflowID, state.StepID)
	}
	leaf, ok := step.(*LeafStep)
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf(
			"suspended step %q is no longer a leaf step", state.StepID)
	}

	// Claim the pointer before running. This is one atomic step rather than
	// a load-then-delete pair, so when several processes resume the same
	// pointer at once exactly one gets past here -- the losers see the
	// pointer already gone rather than applying the same decision again.
	// The validation above deliberately used a non-claiming Load, so a run
	// that turns out to be unresumable is reported without being destroyed.
	claimed, err := e.store.Consume(pointer)
	if err != nil {
		return entities.WorkflowResult{}, fmt.Errorf("could not claim resume pointer: %w", err)
	}

	result, err := e.runFrom(claimed.WorkflowID, leaf.OnSuccess, merge(claimed.Ctx, resolution), 0)
	result.Trace = append(append([]string{}, claimed.Trace...), result.Trace...)
	return result, err
}

// observe invokes fn with the attached observer, if any. Keeping the nil
// check in one place lets the Run loop stay readable.
func (e *Engine) observe(fn func(Observer)) {
	if e.observer != nil {
		fn(e.observer)
	}
}

// Context keys the engine writes when a breaker diverts to a fallback. They
// are namespaced because they are the one case where the engine puts its own
// content into a domain's context, and a domain field must never collide with
// them by accident.
const (
	// FailureReasonKey holds why the diverted step failed -- an action's
	// error text, or a probe's DetectedFailureMode.
	FailureReasonKey = "cee.failure_reason"
	// FailedStepKey holds the step ID that failed.
	FailedStepKey = "cee.failed_step"
)

// onFailure decides where a failed step goes and, when it goes to a fallback,
// tells that fallback what happened.
//
// Without this the reason was dropped on the fallback path and only survived
// when there was no fallback at all -- which inverted the need, since a step
// that exists to handle failure is exactly the one that has to know which
// failure it is handling. Two different probe verdicts would otherwise land
// in the same fallback indistinguishably, and a manifest that reported a
// fixed message there would be confidently wrong about one of them.
func (e *Engine) onFailure(step Step, reason string, ctx map[string]any) (string, map[string]any, error) {
	if ref := step.circuitBreakerPolicyRef(); ref != "" {
		if policy, ok := e.policies[ref]; ok && policy.FallbackStepRef != "" {
			return policy.FallbackStepRef, merge(ctx, map[string]any{
				FailureReasonKey: reason,
				FailedStepKey:    step.ID(),
			}), nil
		}
	}
	return "", ctx, &CircuitBreakerTripped{StepID: step.ID(), Reason: reason}
}

// bypassesBreaker reports whether err describes a defect in how the
// workflow is built rather than a business action that failed. A circuit
// breaker exists to absorb the latter; absorbing the former would convert a
// misconfiguration into a silent fallback and hide it from the author.
func bypassesBreaker(err error) bool {
	var stepLimit *StepLimitExceeded
	var depthLimit *DepthLimitExceeded
	var nested *NestedSuspensionUnsupported
	var noStore *NoSuspensionSupport
	return errors.As(err, &stepLimit) ||
		errors.As(err, &depthLimit) ||
		errors.As(err, &nested) ||
		errors.As(err, &noStore)
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
